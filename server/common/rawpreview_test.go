package common

import (
	"context"
	"io"
	"net/url"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/rawpreview"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

const testAPIURL = "http://localhost:5244"

// rawTestCtx carries the api url the URL builders read, and primes the two
// settings the signer consults so that signing needs no database.
func rawTestCtx(t *testing.T) context.Context {
	t.Helper()
	op.Cache.SetSetting(conf.Token, &model.SettingItem{Key: conf.Token, Value: "raw-preview-test-token"})
	op.Cache.SetSetting(conf.LinkExpiration, &model.SettingItem{Key: conf.LinkExpiration, Value: "0"})
	return context.WithValue(context.Background(), conf.ApiUrlKey, testAPIURL)
}

func rawFileObj(name string) model.Obj {
	return &model.Object{Name: name, Size: 26 << 20}
}

func rawDirObj(name string) model.Obj {
	return &model.Object{Name: name, IsFolder: true}
}

func rawThumbObj(name, thumb string) model.Obj {
	return &model.ObjThumb{
		Object:    model.Object{Name: name},
		Thumbnail: model.Thumbnail{Thumbnail: thumb},
	}
}

func TestWantsRawRendition(t *testing.T) {
	tests := []struct {
		name   string
		obj    model.Obj
		want   bool
		reason string
	}{
		{"nil obj", nil, false, "a missing object has nothing to render"},
		{"upper case cr2", rawFileObj("IMG_0001.CR2"), true, "extension matching is case insensitive"},
		{"raf", rawFileObj("DSCF1234.raf"), true, "fuji raf is a handled container"},
		{"nef", rawFileObj("DSC_0002.nef"), true, "nikon nef is a handled container"},
		{"cr3", rawFileObj("IMG_0003.cr3"), true, "canon cr3 is a handled container"},
		{"arw", rawFileObj("DSC00004.arw"), true, "sony arw is a handled container"},
		{"crw", rawFileObj("CRW_0005.crw"), true, "canon crw is a handled container"},
		{"rw2", rawFileObj("P1000006.rw2"), true, "panasonic rw2 is a handled container"},
		{"dng", rawFileObj("IMG_0007.dng"), true, "adobe dng is a handled container"},
		{"jpeg", rawFileObj("IMG_0001.jpg"), false, "browsers decode jpeg themselves"},
		{"png", rawFileObj("shot.png"), false, "browsers decode png themselves"},
		{"no extension", rawFileObj("IMG_0001"), false, "there is nothing to match on"},
		{"directory named like a raw file", rawDirObj("trip.dng"), false, "directories have no renditions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WantsRawRendition(tt.obj); got != tt.want {
				t.Errorf("WantsRawRendition() = %v, want %v\nReason: %s", got, tt.want, tt.reason)
			}
		})
	}
}

// TestWantsRawRenditionDisabled pins the admin switch down: with extraction off,
// nothing is rewritten and clients get plain original files everywhere.
func TestWantsRawRenditionDisabled(t *testing.T) {
	rawpreview.SetEnabled(false)
	t.Cleanup(func() { rawpreview.SetEnabled(true) })

	obj := rawFileObj("IMG_0001.cr2")
	if WantsRawRendition(obj) {
		t.Error("WantsRawRendition() = true with extraction disabled, want false")
	}
	if got := ThumbURL(rawTestCtx(t), "/photos", obj); got != "" {
		t.Errorf("ThumbURL() = %q with extraction disabled, want empty", got)
	}
	if got := SharingThumbURL(rawTestCtx(t), "/abc/IMG_0001.cr2", "", obj); got != "" {
		t.Errorf("SharingThumbURL() = %q with extraction disabled, want empty", got)
	}
	if !rawpreview.IsRaw("IMG_0001.cr2") {
		t.Error("IsRaw() = false with extraction disabled, want true: the name is still a raw name")
	}
}

type stubRanger struct{}

func (stubRanger) RangeRead(context.Context, http_range.Range) (io.ReadCloser, error) {
	return nil, io.EOF
}

func TestIsGeneratedLink(t *testing.T) {
	tests := []struct {
		name   string
		link   *model.Link
		want   bool
		reason string
	}{
		{"nil link", nil, false, "there is nothing to serve"},
		{"empty link", &model.Link{}, false, "no url and no reader is not servable either"},
		{
			"url only", &model.Link{URL: "https://cdn.example.com/IMG_0001.CR2"}, false,
			"the client can fetch a url on its own, so redirect",
		},
		{
			"reader only", &model.Link{RangeReader: stubRanger{}}, true,
			"a rendition exists only in memory here, so this server has to serve it",
		},
		{
			"url wins over reader",
			&model.Link{URL: "https://cdn.example.com/IMG_0001.CR2", RangeReader: stubRanger{}}, false,
			"a url means the storage can be reached directly, no proxying needed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsGeneratedLink(tt.link); got != tt.want {
				t.Errorf("IsGeneratedLink() = %v, want %v\nReason: %s", got, tt.want, tt.reason)
			}
		})
	}
}

// TestRawRenditionURL checks that the viewer is pointed at this server's own
// download endpoint, with the rendition type and a signature for the real path.
func TestRawRenditionURL(t *testing.T) {
	ctx := rawTestCtx(t)
	for _, reqPath := range []string{
		"/photos/IMG_0001.CR2",
		"/photos/2024 summer/DSCF#1 (raw).raf",
		"/照片/日本/IMG_0002.nef",
	} {
		t.Run(reqPath, func(t *testing.T) {
			raw := RawRenditionURL(ctx, reqPath, op.LinkTypePreview)
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", raw, err)
			}
			if got, want := u.Scheme+"://"+u.Host, testAPIURL; got != want {
				t.Errorf("origin = %q, want %q", got, want)
			}
			if got, want := u.Path, "/d"+reqPath; got != want {
				t.Errorf("decoded path = %q, want %q: every segment must survive escaping", got, want)
			}
			q := u.Query()
			if got, want := q.Get("type"), op.LinkTypePreview; got != want {
				t.Errorf("type = %q, want %q", got, want)
			}
			if err := sign.Verify(reqPath, q.Get("sign")); err != nil {
				t.Errorf("sign does not verify for %q: %v", reqPath, err)
			}
		})
	}
}

// TestThumbURL covers the grid: a storage supplied thumbnail is always preferred,
// and only raw files without one fall back to an extracted thumbnail.
func TestThumbURL(t *testing.T) {
	ctx := rawTestCtx(t)

	if got, want := ThumbURL(ctx, "/photos", rawThumbObj("IMG_0001.cr2", "https://cdn.example.com/t.jpg")), "https://cdn.example.com/t.jpg"; got != want {
		t.Errorf("ThumbURL() = %q, want %q: a storage thumbnail costs us nothing", got, want)
	}
	if got := ThumbURL(ctx, "/photos", rawThumbObj("IMG_0001.cr2", "")); got == "" {
		t.Error("ThumbURL() is empty for a raw file whose storage returned a blank thumbnail, want a fallback")
	}
	if got := ThumbURL(ctx, "/photos", rawFileObj("IMG_0001.jpg")); got != "" {
		t.Errorf("ThumbURL() = %q for a jpeg without a storage thumbnail, want empty", got)
	}
	if got := ThumbURL(ctx, "/photos", rawDirObj("trip")); got != "" {
		t.Errorf("ThumbURL() = %q for a directory, want empty", got)
	}

	raw := ThumbURL(ctx, "/photos/2024", rawFileObj("IMG_0001.cr2"))
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	const reqPath = "/photos/2024/IMG_0001.cr2"
	if got, want := u.Path, "/d"+reqPath; got != want {
		t.Errorf("decoded path = %q, want %q: the parent and the name must be joined", got, want)
	}
	if got, want := u.Query().Get("type"), op.LinkTypeThumb; got != want {
		t.Errorf("type = %q, want %q: the grid asks for the small variant", got, want)
	}
	if err := sign.Verify(reqPath, u.Query().Get("sign")); err != nil {
		t.Errorf("sign does not verify for %q: %v", reqPath, err)
	}
}

// TestSharingThumbURL covers the same fallback behind a share link, where the
// endpoint is /sd and the share password stands in for a signature.
func TestSharingThumbURL(t *testing.T) {
	ctx := rawTestCtx(t)

	if got, want := SharingThumbURL(ctx, "/abc/IMG_0001.cr2", "pw", rawThumbObj("IMG_0001.cr2", "https://cdn.example.com/t.jpg")), "https://cdn.example.com/t.jpg"; got != want {
		t.Errorf("SharingThumbURL() = %q, want %q", got, want)
	}
	if got := SharingThumbURL(ctx, "/abc/IMG_0001.jpg", "", rawFileObj("IMG_0001.jpg")); got != "" {
		t.Errorf("SharingThumbURL() = %q for a jpeg, want empty", got)
	}

	const fakePath = "/abc/2024 summer/IMG_0001.cr2"
	raw := SharingThumbURL(ctx, fakePath, "p&w=1", rawFileObj("IMG_0001.cr2"))
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	if got, want := u.Path, "/sd"+fakePath; got != want {
		t.Errorf("decoded path = %q, want %q", got, want)
	}
	q := u.Query()
	if got, want := q.Get("type"), op.LinkTypeThumb; got != want {
		t.Errorf("type = %q, want %q", got, want)
	}
	if got, want := q.Get("pwd"), "p&w=1"; got != want {
		t.Errorf("pwd = %q, want %q: an escaped password must not split the query", got, want)
	}
	if q.Has("sign") {
		t.Error("share thumbnail carries a sign parameter, want none: the share id and password authorise it")
	}

	noPwd, err := url.Parse(SharingThumbURL(ctx, "/abc/IMG_0001.cr2", "", rawFileObj("IMG_0001.cr2")))
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	if noPwd.Query().Has("pwd") {
		t.Error("pwd parameter present for a share without a password, want none")
	}
}

// TestAcceptsImageOnly pins down the header shapes that decide whether a request
// can only be satisfied by an image. Getting this wrong would either break RAW
// downloads or stop the viewer from showing anything.
func TestAcceptsImageOnly(t *testing.T) {
	tests := []struct {
		name   string
		accept string
		want   bool
		reason string
	}{
		{
			"img element", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8", true,
			"chrome sends this for <img>, and only an image can satisfy it",
		},
		{
			"img element firefox", "image/avif,image/webp,*/*", true,
			"firefox sends this for <img>",
		},
		{"image wildcard only", "image/*", true, "css backgrounds and preloads"},
		{"single concrete image", "image/jpeg", true, "an explicit jpeg request"},
		{
			"browser navigation",
			"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8", false,
			"opening the file in a tab must still download the raw original",
		},
		{
			"xhtml navigation", "application/xhtml+xml,image/webp", false,
			"a markup request is a navigation, whatever else it lists",
		},
		{"download client", "*/*", false, "curl, wget and most download managers"},
		{"empty", "", false, "webdav clients and plain fetches send nothing"},
		{"video element", "video/webm,video/ogg,video/*;q=0.9,*/*;q=0.5", false, "not an image request"},
		{"json", "application/json", false, "not an image request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AcceptsImageOnly(tt.accept); got != tt.want {
				t.Errorf("AcceptsImageOnly(%q) = %v, want %v\nReason: %s",
					tt.accept, got, tt.want, tt.reason)
			}
		})
	}
}

