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

// Link types that ask for a derived image instead of the file itself.
const (
	LinkTypeThumb   = "thumb"
	LinkTypePreview = "preview"
)

// IsRenditionType reports whether a link type asks for a derived image.
func IsRenditionType(t string) bool {
	return t == LinkTypeThumb || t == LinkTypePreview
}

// rawLocators remembers where the renditions of a RAW file live. A locator is a
// few dozen bytes and stays valid for as long as the file itself does.
var rawLocators = cache.NewKeyedCache[[]rawpreview.Candidate](12 * time.Hour)

// rawThumbs holds generated thumbnails, bounded by the configured cache size.
var rawThumbs = &rawThumbCache{items: make(map[string][]byte)}

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

// RawPreviewCacheClear drops every cached RAW locator and thumbnail.
func RawPreviewCacheClear() {
	rawLocators.Clear()
	rawThumbs.mu.Lock()
	defer rawThumbs.mu.Unlock()
	rawThumbs.items = make(map[string][]byte)
	rawThumbs.order = nil
	rawThumbs.size = 0
}

func rawKey(storage driver.Driver, path string, file model.Obj) string {
	return fmt.Sprintf("%s|%d|%d", Key(storage, path), file.GetSize(), file.ModTime().Unix())
}

// rawStripped hides the browser's request headers from the sub range requests
// issued here: those requests are ours, not a pass through of the client's.
type rawStripped struct{ inner model.RangeReaderIF }

func (r rawStripped) RangeRead(ctx context.Context, hr http_range.Range) (io.ReadCloser, error) {
	return r.inner.RangeRead(context.WithValue(ctx, conf.RequestHeaderKey, http.Header(nil)), hr)
}

// rawBaseReader resolves the ordinary driver link for path and adapts it to a
// range reader. The returned link must be closed by the caller.
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

// rawBytes serves an in memory rendition.
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

// rawRenditionReader reads a byte range of a rendition that stays in the remote
// file. Every call resolves the driver link again, which the link cache serves,
// so no long lived reference to the storage is kept.
type rawRenditionReader struct {
	storage  driver.Driver
	path     string
	args     model.LinkArgs
	fileSize int64
	off      int64
	length   int64
}

func (r *rawRenditionReader) RangeRead(ctx context.Context, hr http_range.Range) (io.ReadCloser, error) {
	if hr.Start < 0 || hr.Start > r.length {
		return nil, fmt.Errorf("rawpreview: range start %d out of bounds (size %d)", hr.Start, r.length)
	}
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

// rawCandidates locates the renditions embedded in a RAW file, caching both
// successes and failures.
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
		// Remember the failure briefly, so browsing a folder of unsupported
		// files does not re-read every one of them on every listing.
		rawLocators.SetWithTTL(key, nil, 10*time.Minute)
		return nil, err
	}
	log.Debugf("rawpreview: %s: %d renditions, read %d bytes in %d requests, largest %s",
		path, len(cands), read, requests, cands[0])
	rawLocators.Set(key, cands)
	return cands, nil
}

func rawThumbLink(ctx context.Context, storage driver.Driver, path string, file model.Obj,
	args model.LinkArgs, cands []rawpreview.Candidate) (*model.Link, error) {
	px := rawpreview.ThumbSize()
	key := fmt.Sprintf("%s|thumb%d", rawKey(storage, path, file), px)
	data, ok := rawThumbs.get(key)
	if !ok {
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

// rawRenditionLink builds a link serving a JPEG rendition embedded in a RAW
// file. Only the bytes that rendition occupies are read from the storage, which
// is what distinguishes it from proxying the original file. It returns nil when
// the request should fall through to the storage driver.
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

func rawRendition(ctx context.Context, storage driver.Driver, path string, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	cands, err := rawCandidates(ctx, storage, path, file, args)
	if err != nil {
		return nil, err
	}
	if args.Type == LinkTypeThumb {
		return rawThumbLink(ctx, storage, path, file, args, cands)
	}
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

// rawHeader describes a rendition to the HTTP layer: it is a JPEG meant to be
// displayed, and its validator has to differ from the RAW file's own so a client
// never mistakes one variant of the path for the other.
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
