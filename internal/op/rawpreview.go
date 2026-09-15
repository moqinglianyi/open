// RAW 照片的派生图（内嵌 JPEG）在存储层这一侧的实现。
//
// 相机 RAW 里本来就存着整张 JPEG 预览，找到它只要读容器的元数据，所以这里不必把几十
// 兆的原文件整个拉下来，只按需读那几段字节——这也是它和「开代理转发原文件」的根本
// 区别。
//
// 一次派生图请求的走向：
//
//	Link(args.Type = thumb | preview)
//	  └─ rawRenditionLink          只接派生图请求，其余原样交给存储驱动
//	       └─ rawRendition
//	            ├─ rawCandidates   读容器元数据，定位内嵌 JPEG（结果缓存 12 小时）
//	            ├─ thumb   → rawThumbLink        抽图、缩放、编码，成品进内存缓存
//	            └─ preview → rawRenditionReader  边发边从原文件读那一段，不落地
//
// 任何一步失败都返回 nil 或错误，调用方会退回存储驱动原本的链接：页面顶多没有预览，
// 不会坏掉。

package op

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/rawpreview"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

// 要的不是文件本身、而是由它派生出来的图片时，链接类型取这两个值之一。
const (
	LinkTypeThumb   = "thumb"
	LinkTypePreview = "preview"
)

// IsRenditionType 判断这个链接类型要的是不是派生图。
func IsRenditionType(t string) bool {
	return t == LinkTypeThumb || t == LinkTypePreview
}

// ---------------------------------------------------------------------- 入口

// rawRenditionLink 为 RAW 照片里的内嵌 JPEG 造一个链接。只读派生图占的那几段字节，
// 不动原文件，这是它和代理转发的区别所在。返回 nil 表示这个请求该照常交给存储驱动：
// 要么它要的不是派生图，要么这个文件里没抽出东西来。
func rawRenditionLink(ctx context.Context, storage driver.Driver, path string, file model.Obj, args model.LinkArgs) *model.Link {
	if !IsRenditionType(args.Type) || file.IsDir() || !rawpreview.Handles(file.GetName()) {
		return nil
	}
	link, err := rawRendition(ctx, storage, path, file, args)
	if err != nil {
		log.Warnf("rawpreview: %s: %v", path, err)
		return nil
	}
	return link
}

// rawRendition 先定位文件里的各张内嵌 JPEG，再按要的类型给出链接：缩略图要重新编码，
// 大图直接把原文件里的那一段发出去。
func rawRendition(ctx context.Context, storage driver.Driver, path string, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	cands, err := rawCandidates(ctx, storage, path, file, args)
	if err != nil {
		return nil, err
	}
	if args.Type == LinkTypeThumb {
		return rawThumbLink(ctx, storage, path, file, args, cands)
	}
	// 大图挑分辨率最高、又没超过设置上限的那张。
	c, ok := rawpreview.Pick(cands, 0, rawpreview.MaxPreviewBytes())
	if !ok {
		return nil, fmt.Errorf("no rendition for %s", file.GetName())
	}
	exp := time.Hour
	return &model.Link{
		Header:        rawHeader(file, fmt.Sprintf("view%d-%d", c.Offset, c.Length)),
		ContentLength: c.Length,
		Expiration:    &exp,
		RangeReader: &rawRenditionReader{
			storage:  storage,
			path:     path,
			args:     args,
			fileSize: file.GetSize(),
			off:      c.Offset,
			length:   c.Length,
		},
	}, nil
}

// ------------------------------------------------------------------ 定位

// rawLocators 记住某个 RAW 文件里各张内嵌 JPEG 的位置。一条记录只有几十字节，而且文件
// 不变它就一直有效，所以缓存久一点很划算：省掉的是一轮读容器元数据的网络往返。
var rawLocators = cache.NewKeyedCache[[]rawpreview.Candidate](12 * time.Hour)

// rawKey 把「哪个存储的哪个文件、多大、什么时候改的」拼成缓存键，文件一变键就变。
func rawKey(storage driver.Driver, path string, file model.Obj) string {
	return fmt.Sprintf("%s|%d|%d", Key(storage, path), file.GetSize(), file.ModTime().Unix())
}

// rawCandidates 定位文件里的内嵌 JPEG，成功和失败都进缓存。
func rawCandidates(ctx context.Context, storage driver.Driver, path string, file model.Obj, args model.LinkArgs) ([]rawpreview.Candidate, error) {
	key := rawKey(storage, path, file)
	if c, ok := rawLocators.Get(key); ok {
		if len(c) == 0 {
			return nil, fmt.Errorf("no embedded JPEG in %s", file.GetName())
		}
		return c, nil
	}
	rr, base, err := rawBaseReader(ctx, storage, path, args, file.GetSize())
	if err != nil {
		return nil, err
	}
	defer base.Close()
	cands, read, requests, err := rawpreview.Locate(ctx, rr, file.GetSize(), file.GetName())
	if err != nil {
		// 失败也短暂记一下，免得翻到一个装满不支持的文件的目录时，每次列表都把它们
		// 重新读一遍。
		rawLocators.SetWithTTL(key, nil, 10*time.Minute)
		return nil, err
	}
	log.Debugf("rawpreview: %s: %d renditions, read %d bytes in %d requests, largest %s",
		path, len(cands), read, requests, cands[0])
	rawLocators.Set(key, cands)
	return cands, nil
}

// ---------------------------------------------------------------- 缩略图

// rawThumbs 存放已经生成好的缩略图，总量受设置里的缓存大小限制。缩略图要解码、缩放、
// 重新编码，比定位贵得多，所以成品值得留着：列表页翻回来时就不必再算一遍。
var rawThumbs = &rawThumbCache{items: make(map[string][]byte)}

// rawThumbCache 是个按写入顺序淘汰（FIFO）的定量缓存。用不着 LRU：缩略图便宜到重算
// 也不心疼，这里只要保证内存有个上限。
type rawThumbCache struct {
	mu    sync.Mutex
	items map[string][]byte
	order []string
	size  int64
}

func (c *rawThumbCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.items[key]
	return b, ok
}

func (c *rawThumbCache) put(key string, data []byte) {
	limit := rawpreview.CacheBytes()
	// 缓存被关掉（0），或者这一张自己就超了上限，就不存。
	if limit <= 0 || int64(len(data)) > limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; ok {
		return
	}
	c.items[key] = data
	c.order = append(c.order, key)
	c.size += int64(len(data))
	for c.size > limit && len(c.order) > 0 {
		k := c.order[0]
		c.order = c.order[1:]
		c.size -= int64(len(c.items[k]))
		delete(c.items, k)
	}
}

// rawThumbLink 给出列表页要的那张小图：缓存里有就直接用，没有就挑一张够大的内嵌 JPEG
// 读下来，缩放并重新编码。成品本身就在内存里，所以链接直接带着字节走。
func rawThumbLink(ctx context.Context, storage driver.Driver, path string, file model.Obj,
	args model.LinkArgs, cands []rawpreview.Candidate) (*model.Link, error) {
	px := rawpreview.ThumbSize()
	key := fmt.Sprintf("%s|thumb%d", rawKey(storage, path, file), px)
	data, ok := rawThumbs.get(key)
	if !ok {
		// 挑短边够 px 的最小那张：缩放的活儿越少越好。
		c, found := rawpreview.Pick(cands, px, 0)
		if !found {
			return nil, fmt.Errorf("no rendition for %s", file.GetName())
		}
		rr, base, err := rawBaseReader(ctx, storage, path, args, file.GetSize())
		if err != nil {
			return nil, err
		}
		defer base.Close()
		jpg, err := rawReadAll(ctx, rr, c.Offset, c.Length)
		if err != nil {
			return nil, err
		}
		if data, err = rawpreview.MakeThumb(jpg, px, c.Orientation); err != nil {
			return nil, err
		}
		rawThumbs.put(key, data)
	}
	exp := 12 * time.Hour
	return &model.Link{
		Header:        rawHeader(file, fmt.Sprintf("thumb%d-%d", px, len(data))),
		ContentLength: int64(len(data)),
		Expiration:    &exp,
		RangeReader:   rawBytes(data),
	}, nil
}

// RawPreviewCacheClear 清掉所有缓存的位置记录和缩略图。改动 RAW 预览的设置后调用，
// 免得旧尺寸的图继续被端出来。
func RawPreviewCacheClear() {
	rawLocators.Clear()
	rawThumbs.mu.Lock()
	defer rawThumbs.mu.Unlock()
	rawThumbs.items = make(map[string][]byte)
	rawThumbs.order = nil
	rawThumbs.size = 0
}

// ------------------------------------------------------ 从原文件里读那几段字节

// rawStripped 把浏览器的请求头从这里发出的分段请求上摘掉：这些请求是我们自己为了抽图
// 发的，不是转发客户端的那一个，带上人家的 Range、If-None-Match 只会取错东西。
type rawStripped struct{ inner model.RangeReaderIF }

func (r rawStripped) RangeRead(ctx context.Context, hr http_range.Range) (io.ReadCloser, error) {
	return r.inner.RangeRead(context.WithValue(ctx, conf.RequestHeaderKey, http.Header(nil)), hr)
}

// rawBaseReader 拿到 path 平常那个下载链接，并包成能按区间读的样子。返回的 link 由调用
// 方负责 Close：直链往往有有效期，握着不放没有意义。
func rawBaseReader(ctx context.Context, storage driver.Driver, path string, args model.LinkArgs, fileSize int64) (model.RangeReaderIF, *model.Link, error) {
	base, obj, err := Link(ctx, storage, path, model.LinkArgs{IP: args.IP, Header: args.Header})
	if err != nil {
		return nil, nil, err
	}
	if fileSize <= 0 {
		fileSize = obj.GetSize()
	}
	rr, err := stream.GetRangeReaderFromLink(fileSize, base)
	if err != nil {
		base.Close()
		return nil, nil, err
	}
	return rawStripped{rr}, base, nil
}

// rawReadAll 读满 [off, off+length) 这一段，用来把整张内嵌 JPEG 取进内存。
func rawReadAll(ctx context.Context, rr model.RangeReaderIF, off, length int64) ([]byte, error) {
	rc, err := rr.RangeRead(ctx, http_range.Range{Start: off, Length: length})
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	buf := make([]byte, length)
	if _, err := io.ReadFull(rc, buf); err != nil {
		return nil, fmt.Errorf("read rendition at %d+%d: %w", off, length, err)
	}
	return buf, nil
}

// rawBytes 直接把内存里的一段字节当作可按区间读的内容发出去，缩略图走这条路。
type rawBytes []byte

func (b rawBytes) RangeRead(_ context.Context, hr http_range.Range) (io.ReadCloser, error) {
	total := int64(len(b))
	if hr.Start < 0 || hr.Start > total {
		return nil, fmt.Errorf("rawpreview: range start %d out of bounds (size %d)", hr.Start, total)
	}
	end := total
	if hr.Length >= 0 && hr.Start+hr.Length < end {
		end = hr.Start + hr.Length
	}
	return io.NopCloser(bytes.NewReader(b[hr.Start:end])), nil
}

// rawRenditionReader 读一张仍然留在远端文件里的派生图（大图），把客户端要的区间换算成
// 原文件里的区间。它记的是「哪个存储的哪个路径」而不是一个具体的直链：每次读都重新取
// 一次链接（走链接缓存，通常不额外花网络），这样直链过期也不会读到一半断掉。
type rawRenditionReader struct {
	storage  driver.Driver
	path     string
	args     model.LinkArgs
	fileSize int64
	off      int64 // 派生图在原文件里的起点
	length   int64 // 派生图的长度，也就是客户端看到的文件大小
}

func (r *rawRenditionReader) RangeRead(ctx context.Context, hr http_range.Range) (io.ReadCloser, error) {
	if hr.Start < 0 || hr.Start > r.length {
		return nil, fmt.Errorf("rawpreview: range start %d out of bounds (size %d)", hr.Start, r.length)
	}
	// 客户端说到底、或者要过了头，都按「读到派生图结束」处理，绝不越界读进原始数据。
	if hr.Length < 0 || hr.Start+hr.Length > r.length {
		hr.Length = r.length - hr.Start
	}
	if hr.Length == 0 {
		return http.NoBody, nil
	}
	rr, base, err := rawBaseReader(ctx, r.storage, r.path, r.args, r.fileSize)
	if err != nil {
		return nil, err
	}
	rc, err := rr.RangeRead(ctx, http_range.Range{Start: r.off + hr.Start, Length: hr.Length})
	if err != nil {
		base.Close()
		return nil, err
	}
	return &rawReadCloser{ReadCloser: rc, base: base}, nil
}

// rawReadCloser 让底层链接跟着这次读一起关掉：调用方只认识 ReadCloser，不知道背后还有
// 一个 link 要收尾。once 是防着上层重复 Close。
type rawReadCloser struct {
	io.ReadCloser
	base *model.Link
	once sync.Once
}

func (r *rawReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(func() { r.base.Close() })
	return err
}

// --------------------------------------------------------------- 响应头

// rawHeader 告诉 HTTP 层这次发出去的是什么：一张用来看的 JPEG，文件名把 .cr2 换成
// .jpg。Etag 必须和 RAW 原文件的不一样，而且缩略图和大图之间也要不一样（variant 就是
// 为此），否则同一个网址的几个变体会在缓存里互相冒名顶替。
func rawHeader(file model.Obj, variant string) http.Header {
	name := file.GetName()
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	return http.Header{
		"Content-Type":        []string{"image/jpeg"},
		"Content-Disposition": []string{"inline; " + strings.TrimPrefix(utils.GenerateContentDisposition(name+".jpg"), "attachment; ")},
		"Etag":                []string{fmt.Sprintf(`"raw-%s-%x-%x"`, variant, file.GetSize(), file.ModTime().Unix())},
	}
}
