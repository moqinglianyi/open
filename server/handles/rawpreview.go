package handles

import (
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/rawpreview"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// rawLinkType 决定该向存储层要哪种链接。原则上就是请求里的 type 原样传下去，只有相机
// RAW 照片例外：开着内容协商时，只有图片才能满足的请求会拿到内嵌 JPEG，而下载工具、
// WebDAV 客户端和浏览器直接打开网址这三种，仍旧拿到原始 RAW 文件。
//
// 两个变体共用同一个网址，所以要告诉中间的缓存按 Accept 区分，否则 <img> 取到的那张
// JPEG 会被当成这个网址的唯一答案，回头下载 RAW 时拿到的就是图。
func rawLinkType(c *gin.Context, filename string) string {
	t := c.Query("type")
	if !rawpreview.Handles(filename) || !rawpreview.Negotiate() {
		return t
	}
	c.Header("Vary", "Accept")
	if t == "" && common.AcceptsImageOnly(c.GetHeader("Accept")) {
		return op.LinkTypePreview
	}
	return t
}
