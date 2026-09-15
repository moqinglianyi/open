package common

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/rawpreview"
	"github.com/gin-gonic/gin"
)

// accessCtx builds a GET request for target, optionally carrying the Accept and
// User-Agent headers the classifier reads.
func accessCtx(target, accept, ua string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if accept != "" {
		c.Request.Header.Set("Accept", accept)
	}
	if ua != "" {
		c.Request.Header.Set("User-Agent", ua)
	}
	return c
}

// imgOnlyAccept is what a browser sends for <img>, and the only reason a RAW
// request with no type parameter is a preview rather than a download.
const imgOnlyAccept = "image/avif,image/webp,*/*"

// navAccept is a browser navigating to the file: it accepts images, but asks for
// markup first, so it is a download.
const navAccept = "text/html,application/xhtml+xml,image/avif,image/webp,*/*;q=0.8"

// TestIsMediaFile guards the gate every log line passes through. RAW extensions
// come from the rawpreview setting rather than a second list here, so that adding
// a format in one place does not silently drop it from the log.
func TestIsMediaFile(t *testing.T) {
	tests := []struct {
		name   string
		want   bool
		reason string
	}{
		{"/photos/canon.cr2", true, "canon cr2 is a raw photo"},
		{"/photos/canon.CR3", true, "extension matching is case insensitive"},
		{"/photos/fuji.raf", true, "fuji raf used to be missing from the log entirely"},
		{"/photos/lumix.rw2", true, "panasonic rw2 used to be missing from the log entirely"},
		{"/photos/old.crw", true, "canon crw used to be missing from the log entirely"},
		{"/photos/olympus.orf", true, "olympus orf used to be missing from the log entirely"},
		{"/photos/nikon.nef", true, "nikon nef is a raw photo"},
		{"/photos/sony.arw", true, "sony arw is a raw photo"},
		{"/photos/adobe.dng", true, "adobe dng is a raw photo"},
		{"/photos/holiday.jpg", true, "plain images are logged as before"},
		{"/photos/holiday.heic", true, "phone photos are images too"},
		{"/movies/clip.mkv", true, "videos are logged as before"},
		{"/movies/clip.MP4", true, "extension matching is case insensitive"},
		{"/docs/readme.txt", false, "documents are not media"},
		{"/docs/archive.zip", false, "archives are not media"},
		{"/photos/noext", false, "there is nothing to match on"},
		{"/photos/trip.cr2/inner.txt", false, "only the file name decides, not a parent directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMediaFile(tt.name); got != tt.want {
				t.Errorf("IsMediaFile(%q) = %v, want %v\nReason: %s", tt.name, got, tt.want, tt.reason)
			}
		})
	}
}

// TestDetectAccessType is the regression test for the reported bug: viewing a RAW
// photo was written to the log as a download, because the rendition it fetches is
// served by /d/ like any download. What the request asks for now decides.
func TestDetectAccessType(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		rawPath string
		accept  string
		ua      string
		want    string
		reason  string
	}{
		{
			"raw preview rendition", "/d/photos/canon.cr2?type=preview&sign=x", "/photos/canon.cr2",
			imgOnlyAccept, "", AccessTypePreview,
			"the viewer asks for the embedded jpeg by name: this is viewing, not downloading",
		},
		{
			"raw thumbnail rendition", "/d/photos/canon.cr2?type=thumb&sign=x", "/photos/canon.cr2",
			imgOnlyAccept, "", AccessTypeThumb,
			"the listing grid asks for the small variant, which is neither a download nor a full preview",
		},
		{
			"raw negotiated by accept", "/d/photos/fuji.raf", "/photos/fuji.raf",
			imgOnlyAccept, "", AccessTypePreview,
			"only an image can satisfy this request, so the server hands back a rendition",
		},
		{
			"raw opened in a tab", "/d/photos/fuji.raf", "/photos/fuji.raf",
			navAccept, "", AccessTypeDownload,
			"navigation receives the untouched raw file, so it really is a download",
		},
		{
			"raw fetched by curl", "/d/photos/fuji.raf", "/photos/fuji.raf",
			"*/*", "curl/8.5.0", AccessTypeDownload,
			"a download client gets the original file",
		},
		{
			"raw fetched with no accept header", "/d/photos/fuji.raf", "/photos/fuji.raf",
			"", "", AccessTypeDownload,
			"webdav clients send nothing and receive the original file",
		},
		{
			"jpeg thumbnail", "/d/photos/holiday.jpg?type=thumb", "/photos/holiday.jpg",
			imgOnlyAccept, "", AccessTypeThumb,
			"storage thumbnails go through the same endpoint and are not downloads either",
		},
		{
			"jpeg download", "/d/photos/holiday.jpg", "/photos/holiday.jpg",
			"*/*", "", AccessTypeDownload,
			"the unchanged behaviour for plain files",
		},
		{
			"jpeg fetched by an img tag", "/d/photos/holiday.jpg", "/photos/holiday.jpg",
			imgOnlyAccept, "", AccessTypePreview,
			"the image grid fetches images through /d/ with no type: that is viewing, not downloading",
		},
		{
			"video is not an image", "/d/movies/clip.mkv", "/movies/clip.mkv",
			imgOnlyAccept, "", AccessTypeDownload,
			"nothing hands a video to an <img>, so an image-only accept cannot make it a preview",
		},
		{
			"preview endpoint", "/p/photos/holiday.jpg", "/photos/holiday.jpg",
			imgOnlyAccept, "", AccessTypePreview,
			"the /p/ endpoint exists to be previewed",
		},
		{
			"player user agent", "/d/movies/clip.mkv", "/movies/clip.mkv",
			"", "VLC/3.0.20 LibVLC/3.0.20", AccessTypePlayer,
			"an external player is neither a download nor a browser preview",
		},
		{
			"explicit rendition beats the player user agent",
			"/d/photos/canon.cr2?type=thumb", "/photos/canon.cr2",
			"", "VLC/3.0.20 LibVLC/3.0.20", AccessTypeThumb,
			"the type parameter states the intent outright, so it wins over a guess",
		},
		{
			"foreign type is not a rendition", "/d/movies/clip.mkv?type=video", "/movies/clip.mkv",
			"", "", AccessTypeDownload,
			"types this code does not own must not be mistaken for renditions",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := accessCtx(tt.target, tt.accept, tt.ua)
			if got := detectAccessType(c, tt.rawPath); got != tt.want {
				t.Errorf("detectAccessType() = %q, want %q\nReason: %s", got, tt.want, tt.reason)
			}
		})
	}

	if got := detectAccessType(nil, "/photos/canon.cr2"); got != AccessTypeDownload {
		t.Errorf("detectAccessType(nil) = %q, want %q", got, AccessTypeDownload)
	}
}

// TestDetectAccessTypeNegotiateOff pins the admin switches down. With negotiation
// off, a plain /d/ request really does return the RAW file, so logging it as a
// download is correct; the viewer still uses an explicit type=preview url.
func TestDetectAccessTypeNegotiateOff(t *testing.T) {
	rawpreview.SetNegotiate(false)
	t.Cleanup(func() { rawpreview.SetNegotiate(true) })

	c := accessCtx("/d/photos/canon.cr2", imgOnlyAccept, "")
	if got := detectAccessType(c, "/photos/canon.cr2"); got != AccessTypeDownload {
		t.Errorf("detectAccessType() = %q with negotiation off, want %q", got, AccessTypeDownload)
	}
	c = accessCtx("/d/photos/canon.cr2?type=preview", "", "")
	if got := detectAccessType(c, "/photos/canon.cr2"); got != AccessTypePreview {
		t.Errorf("detectAccessType() = %q, want %q: an explicit rendition is still a preview", got, AccessTypePreview)
	}
}

// TestLogMediaAccessType covers the callers that know the access is a preview:
// /api/fs/get and share links. Those must keep saying 在线预览, except when the
// request is for a thumbnail, which the share grid fetches through the same code.
func TestLogMediaAccessType(t *testing.T) {
	tests := []struct {
		name    string
		c       *gin.Context
		rawPath string
		want    string
		reason  string
	}{
		{
			"share thumbnail", accessCtx("/sd/abc/canon.cr2?type=thumb&pwd=x", imgOnlyAccept, ""),
			"/abc/canon.cr2", AccessTypeThumb,
			"the share listing grid asks for the small variant",
		},
		{
			"share preview", accessCtx("/sd/abc/canon.cr2?type=preview", imgOnlyAccept, ""),
			"/abc/canon.cr2", AccessTypePreview,
			"the share viewer asks for the embedded jpeg",
		},
		{
			"share download", accessCtx("/sd/abc/canon.cr2", "*/*", ""),
			"/abc/canon.cr2", AccessTypePreview,
			"fs/get and plain share hits stay 在线预览 as they always were",
		},
		{
			"single file share has no name in the path", accessCtx("/sd/abc?type=thumb", "", ""),
			"/", AccessTypeThumb,
			"the type parameter decides, so a rootless share path is still classified",
		},
		{"no context", nil, "/photos/canon.cr2", AccessTypePreview, "nothing to inspect means the default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AccessTypePreview
			if r := renditionAccessType(tt.c, tt.rawPath); r != "" {
				got = r
			}
			if got != tt.want {
				t.Errorf("LogMediaAccess type = %q, want %q\nReason: %s", got, tt.want, tt.reason)
			}
		})
	}
}

// TestRenditionQueryNames keeps the literal query strings used in these tests, and
// in the urls the listing and the viewer are handed, tied to the link types the
// storage layer answers to.
func TestRenditionQueryNames(t *testing.T) {
	if op.LinkTypeThumb != "thumb" || op.LinkTypePreview != "preview" {
		t.Errorf("rendition link types are %q/%q, want \"thumb\"/\"preview\": the urls in the pages say those",
			op.LinkTypeThumb, op.LinkTypePreview)
	}
}

// setLastAccess backdates the entry a test just created, standing in for time
// passing between two requests for the same file.
func setLastAccess(ip, path string, at time.Time, kind string) {
	accessCacheLock.Lock()
	defer accessCacheLock.Unlock()
	accessCache[ip+"|"+path] = accessEntry{at: at, kind: kind}
}

// TestShouldLogAccess covers the dedupe window that keeps a player's Range storm
// from filling the log, and that a different file or client is never swallowed.
func TestShouldLogAccess(t *testing.T) {
	const ip = "203.0.113.7"
	path := "/photos/dedupe-" + time.Now().Format("150405.000000000") + ".cr2"

	if !shouldLogAccess(ip, path, AccessTypePreview) {
		t.Fatal("shouldLogAccess() = false for a first hit, want true")
	}
	if shouldLogAccess(ip, path, AccessTypePreview) {
		t.Error("shouldLogAccess() = true for an immediate repeat, want false: range requests must not spam the log")
	}
	if !shouldLogAccess("198.51.100.9", path, AccessTypePreview) {
		t.Error("shouldLogAccess() = false for another client, want true: each viewer is its own access")
	}
	if !shouldLogAccess(ip, path+"x", AccessTypePreview) {
		t.Error("shouldLogAccess() = false for another file, want true")
	}

	setLastAccess(ip, path, time.Now().Add(-dedupeWindow-time.Second), AccessTypePreview)
	if !shouldLogAccess(ip, path, AccessTypePreview) {
		t.Error("shouldLogAccess() = false after the window elapsed, want true: a later visit is a new access")
	}
}

// TestThumbnailDoesNotStandInForAView keeps the widened window from hiding the
// access that matters: the listing grid fetches a thumbnail on its own, and the
// visitor opening or downloading that same file right after is a real access.
func TestThumbnailDoesNotStandInForAView(t *testing.T) {
	const ip = "203.0.113.31"
	path := uniqueAccessPath(".cr2")

	if !shouldLogAccess(ip, path, AccessTypeThumb) {
		t.Fatal("shouldLogAccess() = false for the grid's thumbnail, want true")
	}
	if !shouldLogAccess(ip, path, AccessTypePreview) {
		t.Error("shouldLogAccess() = false for the view right after the thumbnail, want true: " +
			"a thumbnail the page fetched by itself must not swallow the visitor's own access")
	}
	if shouldLogAccess(ip, path, AccessTypeDownload) {
		t.Error("shouldLogAccess() = true for the bytes of the file being viewed, want false: " +
			"the upgrade happens once, it does not reopen the window")
	}
	if shouldLogAccess(ip, path, AccessTypeThumb) {
		t.Error("shouldLogAccess() = true for a thumbnail after the view, want false: " +
			"going back to the listing is not a new access")
	}
}

// accessSeq keeps the paths these tests use out of each other's dedupe entries,
// which live in a process-wide cache.
var accessSeq atomic.Int64

func uniqueAccessPath(ext string) string {
	return fmt.Sprintf("/photos/one-line-%d-%d%s", time.Now().UnixNano(), accessSeq.Add(1), ext)
}

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/126.0"

// TestOneLinePerAccess is the regression test for the duplicate the log showed for
// a single file open: 在线预览 written by /api/fs/get, and then 下载 written by the
// /d/ request the browser or the player makes for the bytes. Both belong to one
// access, so only the first may reach the log.
func TestOneLinePerAccess(t *testing.T) {
	path := uniqueAccessPath(".mp4")

	meta := accessCtx("/api/fs/get", "application/json", chromeUA)
	if !LogMediaAccess(meta, path, "admin") {
		t.Fatal("LogMediaAccess() = false for /api/fs/get, want true: opening a file must be logged")
	}

	bytes := accessCtx("/d"+path+"?sign=x", "*/*", chromeUA)
	if LogMediaAccessAuto(bytes, path) {
		t.Error("LogMediaAccessAuto() = true for the follow-up /d/ request, want false: " +
			"fetching the bytes of a file that was just opened is the same access, not a download")
	}
}

// TestOneLinePerAccessOutlivesTheGap covers the case the old 20 second window let
// through: the user picks a file, an external player takes its time starting up,
// and the byte request arrives much later. It is still the same access.
func TestOneLinePerAccessOutlivesTheGap(t *testing.T) {
	if dedupeWindow < time.Minute {
		t.Fatalf("dedupeWindow = %v, want at least a minute: launching a player takes longer than that", dedupeWindow)
	}
	path := uniqueAccessPath(".mkv")
	const ip = "192.0.2.1"

	if !shouldLogAccess(ip, path, AccessTypePreview) {
		t.Fatal("shouldLogAccess() = false for the first hit, want true")
	}
	setLastAccess(ip, path, time.Now().Add(-45*time.Second), AccessTypePreview)

	if shouldLogAccess(ip, path, AccessTypeDownload) {
		t.Error("shouldLogAccess() = true 45s after the file was opened, want false: " +
			"the player fetching the file is the access that was already logged")
	}
}

// TestDedupeWindowSlides pins the window down as sliding rather than fixed: every
// swallowed hit pushes the expiry out, so a long playback's Range requests never
// grow into a second line no matter how long it runs.
func TestDedupeWindowSlides(t *testing.T) {
	path := uniqueAccessPath(".mp4")
	const ip = "203.0.113.21"

	if !shouldLogAccess(ip, path, AccessTypePreview) {
		t.Fatal("shouldLogAccess() = false for the first hit, want true")
	}
	setLastAccess(ip, path, time.Now().Add(-dedupeWindow+time.Second), AccessTypePreview)

	if shouldLogAccess(ip, path, AccessTypePreview) {
		t.Fatal("shouldLogAccess() = true just inside the window, want false")
	}

	accessCacheLock.Lock()
	last := accessCache[ip+"|"+path]
	accessCacheLock.Unlock()
	if time.Since(last.at) > time.Second {
		t.Errorf("the swallowed hit left the timestamp at %v, want it refreshed: "+
			"a fixed window starts logging again in the middle of one playback", last.at)
	}
	if last.kind != AccessTypePreview {
		t.Errorf("the entry now reads %q, want %q: the line already written is what this access is", last.kind, AccessTypePreview)
	}
}

// TestRequestLogsOnce covers Down handing the request over to Proxy when the
// storage has to be streamed through this server: both handlers log, one request.
func TestRequestLogsOnce(t *testing.T) {
	path := uniqueAccessPath(".mkv")
	c := accessCtx("/d"+path, "*/*", chromeUA)

	if !LogMediaAccessAuto(c, path) {
		t.Fatal("LogMediaAccessAuto() = false for a fresh request, want true")
	}
	if LogMediaAccessAuto(c, path) {
		t.Error("LogMediaAccessAuto() = true on the second call for the same request, want false: " +
			"Down passing the request to Proxy must not double the log")
	}
}
