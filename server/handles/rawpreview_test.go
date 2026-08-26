package handles

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/rawpreview"
	"github.com/gin-gonic/gin"
)

// TestAcceptsImageOnly lives in server/common alongside common.AcceptsImageOnly,
// which the access log consults as well.

// rawCtx builds a request for /d/<name> with the given query string and Accept
// header, plus the recorder that captures the response headers.
func rawCtx(query, accept string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	target := "/d/photos/IMG_0001.CR2"
	if query != "" {
		target += "?" + query
	}
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if accept != "" {
		c.Request.Header.Set("Accept", accept)
	}
	return c, w
}

const imgAccept = "image/avif,image/webp,*/*"

func TestRawLinkType(t *testing.T) {
	const navAccept = "text/html,application/xhtml+xml,image/avif,*/*;q=0.8"
	tests := []struct {
		name     string
		filename string
		query    string
		accept   string
		want     string
		reason   string
	}{
		{
			"raw file requested by an img element", "IMG_0001.CR2", "", imgAccept, op.LinkTypePreview,
			"the viewer cannot decode a raw file, so hand it the embedded jpeg",
		},
		{
			"raw file opened in a tab", "IMG_0001.CR2", "", navAccept, "",
			"navigation keeps the original, so the download link still works",
		},
		{
			"raw file fetched by a download client", "IMG_0001.CR2", "", "*/*", "",
			"curl and webdav must receive the untouched raw file",
		},
		{
			"raw file with no accept header", "IMG_0001.CR2", "", "", "",
			"nothing to negotiate on means no rewrite",
		},
		{
			"explicit thumb type wins", "IMG_0001.CR2", "type=thumb", imgAccept, op.LinkTypeThumb,
			"the grid asks for the small variant by name and must keep getting it",
		},
		{
			"explicit preview type is kept", "IMG_0001.CR2", "type=preview", "*/*", op.LinkTypePreview,
			"an explicit rendition request needs no accept header",
		},
		{
			"foreign type is passed through", "IMG_0001.CR2", "type=video", imgAccept, "video",
			"types this package does not own must reach the driver unchanged",
		},
		{
			"jpeg is left alone", "IMG_0001.jpg", "", imgAccept, "",
			"browsers decode jpeg themselves, so never rewrite it",
		},
		{
			"jpeg keeps its explicit type", "IMG_0001.jpg", "type=thumb", imgAccept, op.LinkTypeThumb,
			"the storage thumbnail path is untouched by raw handling",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := rawCtx(tt.query, tt.accept)
			if got := rawLinkType(c, tt.filename); got != tt.want {
				t.Errorf("rawLinkType() = %q, want %q\nReason: %s", got, tt.want, tt.reason)
			}
		})
	}
}

// TestRawLinkTypeVary guards the caching contract: two different responses share
// one URL, so anything in between has to key on Accept. Files this package does
// not touch must not pay for a needless cache split.
func TestRawLinkTypeVary(t *testing.T) {
	c, w := rawCtx("", imgAccept)
	rawLinkType(c, "IMG_0001.CR2")
	if got := w.Header().Get("Vary"); got != "Accept" {
		t.Errorf("Vary = %q, want %q: the same url yields the rendition or the original", got, "Accept")
	}

	c, w = rawCtx("", imgAccept)
	rawLinkType(c, "IMG_0001.jpg")
	if got := w.Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q for a jpeg, want empty: its response never varies", got)
	}
}

// TestRawLinkTypeSettings covers the two admin switches. With negotiation off the
// viewer still works, because FsGet hands out an explicit type=preview url, but
// nothing is decided from the Accept header any more.
func TestRawLinkTypeSettings(t *testing.T) {
	t.Run("negotiate off", func(t *testing.T) {
		rawpreview.SetNegotiate(false)
		t.Cleanup(func() { rawpreview.SetNegotiate(true) })

		c, w := rawCtx("", imgAccept)
		if got := rawLinkType(c, "IMG_0001.CR2"); got != "" {
			t.Errorf("rawLinkType() = %q with negotiation off, want empty", got)
		}
		if got := w.Header().Get("Vary"); got != "" {
			t.Errorf("Vary = %q with negotiation off, want empty", got)
		}
		c, _ = rawCtx("type=thumb", "")
		if got := rawLinkType(c, "IMG_0001.CR2"); got != op.LinkTypeThumb {
			t.Errorf("rawLinkType() = %q, want %q: an explicit type still reaches the driver", got, op.LinkTypeThumb)
		}
	})

	t.Run("extraction off", func(t *testing.T) {
		rawpreview.SetEnabled(false)
		t.Cleanup(func() { rawpreview.SetEnabled(true) })

		c, w := rawCtx("", imgAccept)
		if got := rawLinkType(c, "IMG_0001.CR2"); got != "" {
			t.Errorf("rawLinkType() = %q with extraction off, want empty", got)
		}
		if got := w.Header().Get("Vary"); got != "" {
			t.Errorf("Vary = %q with extraction off, want empty", got)
		}
	})
}

