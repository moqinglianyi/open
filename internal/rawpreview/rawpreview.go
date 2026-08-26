// Package rawpreview extracts the JPEG renditions that camera RAW files embed,
// so browsers (which cannot decode RAW) can display them.
//
// Every consumer camera RAW container carries at least one full JPEG rendition
// of the shot. Locating it only requires reading container metadata, so a
// preview can be produced from a remote file with a handful of HTTP range
// requests instead of downloading the whole 20-150 MiB original.
package rawpreview

import (
	"strings"
	"sync/atomic"
)

// DefaultExtensions lists the RAW containers handled by this package.
var DefaultExtensions = []string{
	"3fr", "ari", "arw", "bay", "cap", "cr2", "cr3", "crw", "dcr", "dcs",
	"dng", "drf", "eip", "erf", "fff", "gpr", "iiq", "k25", "kdc", "mdc",
	"mef", "mos", "mrw", "nef", "nrw", "obm", "orf", "ori", "pef", "ptx",
	"pxn", "raf", "raw", "rw2", "rwl", "rwz", "sr2", "srf", "srw", "x3f",
}

// Limits that bound how much of a remote file the parsers may touch.
const (
	// MaxParseBytes caps the total bytes read while locating a preview.
	MaxParseBytes = 24 << 20
	// MaxScanBytes caps the brute force SOI search used as a last resort.
	MaxScanBytes = 8 << 20
	// MinPreviewEdge rejects candidates too small to be useful as a big image.
	MinPreviewEdge = 160
)

type settings struct {
	enabled    bool
	negotiate  bool
	extensions map[string]struct{}
	thumbSize  int
	maxBytes   int64
	cacheBytes int64
}

var current atomic.Pointer[settings]

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

// SetEnabled turns embedded preview extraction on or off.
func SetEnabled(v bool) { mutate(func(s *settings) { s.enabled = v }) }

// SetNegotiate controls Accept header based content negotiation on /d and /p.
func SetNegotiate(v bool) { mutate(func(s *settings) { s.negotiate = v }) }

// SetExtensions replaces the recognised RAW extension list. An empty list
// restores the built in defaults.
func SetExtensions(exts []string) {
	set := toSet(exts)
	if len(set) == 0 {
		set = toSet(DefaultExtensions)
	}
	mutate(func(s *settings) { s.extensions = set })
}

// SetThumbSize sets the long edge, in pixels, of generated grid thumbnails.
func SetThumbSize(px int) {
	if px < 32 {
		px = 32
	} else if px > 4096 {
		px = 4096
	}
	mutate(func(s *settings) { s.thumbSize = px })
}

// SetMaxPreviewBytes caps the size of an embedded JPEG served as a big image.
func SetMaxPreviewBytes(n int64) {
	if n < 256<<10 {
		n = 256 << 10
	}
	mutate(func(s *settings) { s.maxBytes = n })
}

// SetCacheBytes caps the in memory cache holding generated thumbnails.
func SetCacheBytes(n int64) {
	if n < 0 {
		n = 0
	}
	mutate(func(s *settings) { s.cacheBytes = n })
}

// Enabled reports whether RAW preview extraction is active.
func Enabled() bool { return load().enabled }

// Negotiate reports whether Accept header negotiation is active.
func Negotiate() bool { s := load(); return s.enabled && s.negotiate }

// ThumbSize returns the configured thumbnail long edge in pixels.
func ThumbSize() int { return load().thumbSize }

// MaxPreviewBytes returns the configured big image size cap.
func MaxPreviewBytes() int64 { return load().maxBytes }

// CacheBytes returns the configured thumbnail cache size.
func CacheBytes() int64 { return load().cacheBytes }

// Extensions returns the recognised RAW extensions, lower case, without dots.
func Extensions() []string {
	s := load()
	out := make([]string, 0, len(s.extensions))
	for e := range s.extensions {
		out = append(out, e)
	}
	return out
}

// IsRaw reports whether name has a recognised RAW extension. It does not
// consult the setting switch, so callers can test the name independently.
func IsRaw(name string) bool {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return false
	}
	_, ok := load().extensions[strings.ToLower(name[i+1:])]
	return ok
}

// Handles reports whether this package should take over previews for name.
func Handles(name string) bool { return load().enabled && IsRaw(name) }
