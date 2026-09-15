// RAW 照片预览在 HTTP 层的那一小半：浏览器解不了相机 RAW，所以列表和查看器不能直接
// 用原文件的地址，得指向本站下载接口上带 ?type=thumb / ?type=preview 的那个变体，由
// internal/op 把内嵌 JPEG 抽出来当响应体。这里只负责拼地址和做判断，不读文件。
//
// 相关的地方：internal/rawpreview 解析容器，internal/op/rawpreview.go 抽图并缓存，
// server/handles/rawpreview.go 按 Accept 决定要不要换成派生图。

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

// AcceptsImageOnly 判断一个 Accept 头是不是只有图片才能满足——<img>、
// <link rel=preload as=image> 和 CSS 背景发的就是这种。浏览器打开网址时也接受图片，
// 但会把 HTML 摆在前面；下载工具则发 */* 或者什么都不发。
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

// WantsRawRendition 判断 obj 这个文件能不能改用从它里面抽出来的 JPEG 顶替——也就是
// 说，它是一张开着 RAW 预览开关、浏览器自己解不了的相机 RAW 照片。
func WantsRawRendition(obj model.Obj) bool {
	return obj != nil && !obj.IsDir() && rawpreview.Handles(obj.GetName())
}

// IsGeneratedLink 判断这个链接是不是只能由本机发出去：它没有客户端自己能取的 URL。
// RAW 派生图就是这么来的，那些只能穿透本机流式转发的存储也是。
func IsGeneratedLink(link *model.Link) bool {
	return link != nil && link.URL == "" && link.RangeReader != nil
}

// RawRenditionURL 指向本站下载接口，并带上派生图类型，于是响应体是内嵌 JPEG 而不是
// RAW 原文件。reqPath 是文件在本站里的完整路径，签名照它算。
func RawRenditionURL(ctx context.Context, reqPath, linkType string) string {
	return fmt.Sprintf("%s/d%s?type=%s&sign=%s",
		GetApiUrl(ctx), utils.EncodePath(reqPath, true), linkType, sign.Sign(reqPath))
}

// ThumbURL 是列表页该给 obj 显示的缩略图：存储自己给得出来就用它的，给不出来而又是
// 相机 RAW 时，才退回到从文件里抽出来的那张。其余情况返回空，前端按老样子显示图标。
func ThumbURL(ctx context.Context, parent string, obj model.Obj) string {
	if thumb, ok := model.GetThumb(obj); ok && thumb != "" {
		return thumb
	}
	if !WantsRawRendition(obj) {
		return ""
	}
	return RawRenditionURL(ctx, stdpath.Join(parent, obj.GetName()), op.LinkTypeThumb)
}

// SharingThumbURL 是分享链接里的 ThumbURL：接口换成 /sd/<分享 id>/…，并且用分享密码
// 代替签名。fakePath 形如 "/<分享 id>/<分享内的路径>"。
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
