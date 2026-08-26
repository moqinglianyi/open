package common

import (
	"net/http"
	"net/http/httptest"
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

// TestShouldLogAccess covers the dedupe window that keeps a player's Range storm
// from filling the log, and that a different file or client is never swallowed.
func TestShouldLogAccess(t *testing.T) {
	const ip = "203.0.113.7"
	path := "/photos/dedupe-" + time.Now().Format("150405.000000000") + ".cr2"

	if !shouldLogAccess(ip, path) {
		t.Fatal("shouldLogAccess() = false for a first hit, want true")
	}
	if shouldLogAccess(ip, path) {
		t.Error("shouldLogAccess() = true for an immediate repeat, want false: range requests must not spam the log")
	}
	if !shouldLogAccess("198.51.100.9", path) {
		t.Error("shouldLogAccess() = false for another client, want true: each viewer is its own access")
	}
	if !shouldLogAccess(ip, path+"x") {
		t.Error("shouldLogAccess() = false for another file, want true")
	}

	accessCacheLock.Lock()
	accessCache[ip+"|"+path] = time.Now().Add(-dedupeWindow - time.Second)
	accessCacheLock.Unlock()
	if !shouldLogAccess(ip, path) {
		t.Error("shouldLogAccess() = false after the window elapsed, want true: a later visit is a new access")
	}
}
