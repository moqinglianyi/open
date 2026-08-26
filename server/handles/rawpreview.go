package handles

import (
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/rawpreview"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// rawLinkType is the link type the storage layer should be asked for. It is the
// requested type as is, except for camera RAW photos: with content negotiation on,
// a request that only an image can satisfy gets the embedded JPEG rendition, while
// downloads, WebDAV clients and browser navigation keep receiving the RAW file.
func rawLinkType(c *gin.Context, filename string) string {
	t := c.Query("type")
	if !rawpreview.Handles(filename) || !rawpreview.Negotiate() {
		return t
	}
	// Both variants live at the same URL, so caches have to key on Accept.
	c.Header("Vary", "Accept")
	if t == "" && common.AcceptsImageOnly(c.GetHeader("Accept")) {
		return op.LinkTypePreview
	}
	return t
}
