package common

import (
	"context"
	"fmt"
	"net/url"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/rawpreview"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// AcceptsImageOnly reports whether an Accept header can only be satisfied by an
// image, which is what <img>, <link rel=preload as=image> and CSS backgrounds
// send. A navigating browser also accepts images, but asks for HTML first, and
// plain download clients send */* or nothing at all.
func AcceptsImageOnly(accept string) bool {
	image := false
	for part := range strings.SplitSeq(accept, ",") {
		mime := strings.TrimSpace(part)
		if i := strings.IndexByte(mime, ';'); i >= 0 {
			mime = strings.TrimSpace(mime[:i])
		}
		switch {
		case strings.EqualFold(mime, "text/html"), strings.EqualFold(mime, "application/xhtml+xml"):
			return false
		case len(mime) > 6 && strings.EqualFold(mime[:6], "image/"):
			image = true
		}
	}
	return image
}

// WantsRawRendition reports whether OpenList can serve a JPEG rendition extracted
// from obj instead of the original file, which no browser is able to decode.
func WantsRawRendition(obj model.Obj) bool {
	return obj != nil && !obj.IsDir() && rawpreview.Handles(obj.GetName())
}

// IsGeneratedLink reports whether a link has to be served by this server because
// it carries no URL a client could fetch on its own. RAW renditions are built
// this way, as are the links of storages that can only stream through us.
func IsGeneratedLink(link *model.Link) bool {
	return link != nil && link.URL == "" && link.RangeReader != nil
}

// RawRenditionURL points at this server's download endpoint with the rendition
// type set, so the response is the embedded JPEG instead of the RAW file.
// reqPath is the full internal path of the file.
func RawRenditionURL(ctx context.Context, reqPath, linkType string) string {
	return fmt.Sprintf("%s/d%s?type=%s&sign=%s",
		GetApiUrl(ctx), utils.EncodePath(reqPath, true), linkType, sign.Sign(reqPath))
}

// ThumbURL is the thumbnail the listing should show for obj: the one the storage
// supplies when it has one, and otherwise, for camera RAW photos, the thumbnail
// extracted from the file itself.
func ThumbURL(ctx context.Context, parent string, obj model.Obj) string {
	if thumb, ok := model.GetThumb(obj); ok && thumb != "" {
		return thumb
	}
	if !WantsRawRendition(obj) {
		return ""
	}
	return RawRenditionURL(ctx, stdpath.Join(parent, obj.GetName()), op.LinkTypeThumb)
}

// SharingThumbURL is ThumbURL for objects reached through a share link, where the
// download endpoint is /sd/<share id>/... and the share password stands in for a
// signature. fakePath is "/<share id>/<path within the share>".
func SharingThumbURL(ctx context.Context, fakePath, pwd string, obj model.Obj) string {
	if thumb, ok := model.GetThumb(obj); ok && thumb != "" {
		return thumb
	}
	if !WantsRawRendition(obj) {
		return ""
	}
	u := fmt.Sprintf("%s/sd%s?type=%s", GetApiUrl(ctx), utils.EncodePath(fakePath, true), op.LinkTypeThumb)
	if pwd != "" {
		u += "&pwd=" + url.QueryEscape(pwd)
	}
	return u
}
