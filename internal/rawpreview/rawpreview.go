// Package rawpreview 从相机 RAW 文件里找出并取出它内嵌的 JPEG，好让解不了 RAW 的
// 浏览器有东西可显示。
//
// 每种消费级相机的 RAW 容器里都至少存着一张完整的 JPEG 预览，而找到它只需要读容器的
// 元数据，所以哪怕文件在网盘上、有 20-150 MiB，也只要几次 HTTP 分段请求就能拿到预览，
// 不必把原文件整个下载下来。
//
// 包内分工：
//
//	rawpreview.go  设置开关（扩展名清单、缩略图边长、各种上限）与扩展名判断
//	extract.go     总入口 FindPreviews / Locate，候选的整理与挑选（Pick）
//	containers.go  非 TIFF 系容器：Fujifilm RAF、Canon CR3/CRW、Minolta MRW、Sigma X3F
//	tiff.go        TIFF/EXIF 的头部与 IFD 读取
//	tiff_walk.go   遍历 TIFF 系容器（CR2、NEF、ARW、DNG、RW2、ORF…）的 IFD 树找预览
//	jpeg.go        顺着 JPEG 的 marker 走，量出长度、尺寸、EXIF 方向和内嵌小图
//	reader.go      把「按区间读」包装成随机读：块对齐、合并请求、缓存并统计流量
//	thumb.go       把取出来的 JPEG 缩成列表页要的小图
//
// 除本包注释外，包内注释保持英文：那些代码逐行对应各家 RAW 容器的二进制格式，
// 术语（SOI、EOI、IFD、BMFF、CIFF…）本身也是英文的。
package rawpreview

import (
	"strings"
	"sync/atomic"
)

// DefaultExtensions 是本包默认认得的 RAW 扩展名，管理员可以在设置里改。
var DefaultExtensions = []string{
	"3fr", "ari", "arw", "bay", "cap", "cr2", "cr3", "crw", "dcr", "dcs",
	"dng", "drf", "eip", "erf", "fff", "gpr", "iiq", "k25", "kdc", "mdc",
	"mef", "mos", "mrw", "nef", "nrw", "obm", "orf", "ori", "pef", "ptx",
	"pxn", "raf", "raw", "rw2", "rwl", "rwz", "sr2", "srf", "srw", "x3f",
}

// 解析过程最多能碰多少字节。远端文件是按区间请求读的，所以这两个数字直接决定一次
// 预览最多花掉多少流量。
const (
	// MaxParseBytes 是定位一张预览期间允许读的总字节数。
	MaxParseBytes = 24 << 20
	// MaxScanBytes 是实在解析不了容器、退化成暴力搜 SOI 标记时的搜索范围上限。
	MaxScanBytes = 8 << 20
)

// settings 是一份设置快照：所有字段一起换。读的时候不用加锁，也不会撞上改了一半的
// 状态；要改就复制一份、改完再原子地换上去（见 mutate）。
type settings struct {
	enabled    bool
	negotiate  bool
	extensions map[string]struct{}
	thumbSize  int
	maxBytes   int64
	cacheBytes int64
}

var current atomic.Pointer[settings]

// 先摆上一套和 internal/bootstrap/data 里默认值一致的设置，这样数据库还没读进来时
// （比如单元测试里）本包也是能用的。
func init() {
	s := &settings{
		enabled:    true,
		negotiate:  true,
		extensions: toSet(DefaultExtensions),
		thumbSize:  320,
		maxBytes:   24 << 20,
		cacheBytes: 64 << 20,
	}
	current.Store(s)
}

// toSet 把扩展名清单整理成集合：小写、去掉空白和前面的点。
func toSet(exts []string) map[string]struct{} {
	set := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		e = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(e, ".")))
		if e != "" {
			set[e] = struct{}{}
		}
	}
	return set
}

func load() *settings { return current.Load() }

// mutate 复制当前设置、改其中一处、再原子换上。CAS 失败说明有人同时也在改，重来一遍。
func mutate(fn func(*settings)) {
	for {
		old := current.Load()
		next := *old
		fn(&next)
		if current.CompareAndSwap(old, &next) {
			return
		}
	}
}

// 下面这些 Set 由 internal/op 的设置钩子调用，值都在这里夹到合理范围，免得后面每处
// 用到的地方都得再防一遍。

// SetEnabled 打开或关掉内嵌预览提取。
func SetEnabled(v bool) { mutate(func(s *settings) { s.enabled = v }) }

// SetNegotiate 控制 /d 和 /p 上要不要按 Accept 头做内容协商。
func SetNegotiate(v bool) { mutate(func(s *settings) { s.negotiate = v }) }

// SetExtensions 替换认得的 RAW 扩展名清单，传空清单则恢复内置默认值。
func SetExtensions(exts []string) {
	set := toSet(exts)
	if len(set) == 0 {
		set = toSet(DefaultExtensions)
	}
	mutate(func(s *settings) { s.extensions = set })
}

// SetThumbSize 设置生成的列表缩略图长边有多少像素。
func SetThumbSize(px int) {
	if px < 32 {
		px = 32
	} else if px > 4096 {
		px = 4096
	}
	mutate(func(s *settings) { s.thumbSize = px })
}

// SetMaxPreviewBytes 限制当作大图发出去的内嵌 JPEG 有多大。
func SetMaxPreviewBytes(n int64) {
	if n < 256<<10 {
		n = 256 << 10
	}
	mutate(func(s *settings) { s.maxBytes = n })
}

// SetCacheBytes 限制存放缩略图的内存缓存有多大，0 表示不缓存。
func SetCacheBytes(n int64) {
	if n < 0 {
		n = 0
	}
	mutate(func(s *settings) { s.cacheBytes = n })
}

// Enabled 报告内嵌预览提取是否开着。
func Enabled() bool { return load().enabled }

// Negotiate 报告 Accept 内容协商是否开着。总开关一关，协商也跟着作废。
func Negotiate() bool { s := load(); return s.enabled && s.negotiate }

// ThumbSize 返回设置里的缩略图长边像素数。
func ThumbSize() int { return load().thumbSize }

// MaxPreviewBytes 返回设置里的大图大小上限。
func MaxPreviewBytes() int64 { return load().maxBytes }

// CacheBytes 返回设置里的缩略图缓存上限。
func CacheBytes() int64 { return load().cacheBytes }

// Extensions 返回认得的 RAW 扩展名，小写、不带点。internal/op 用它把这些扩展名并进
// image_types，好让 RAW 照片在前端就是「图片」。
func Extensions() []string {
	s := load()
	out := make([]string, 0, len(s.extensions))
	for e := range s.extensions {
		out = append(out, e)
	}
	return out
}

// extOf 取出文件名的扩展名，小写、不带点；没有扩展名时返回空串。
func extOf(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// IsRaw 判断 name 的扩展名是不是认得的 RAW 格式。它不看总开关，所以关掉预览之后，
// 「这仍然是个 RAW 文件名」这件事照样问得出来。
func IsRaw(name string) bool {
	_, ok := load().extensions[extOf(name)]
	return ok
}

// Handles 判断 name 的预览该不该由本包接手：既是 RAW，开关也开着。
func Handles(name string) bool { return load().enabled && IsRaw(name) }
