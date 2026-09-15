package handles

import (
	"errors"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func Down(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)
	common.LogMediaAccessAuto(c, rawPath)
	filename := stdpath.Base(rawPath)
	storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorPage(c, err, 500)
		return
	}
	// A rendition (the JPEG embedded in a RAW photo) is built here from a few byte
	// ranges of the original, so it has to be served locally whatever the storage
	// would normally do with the file itself.
	linkType := rawLinkType(c, filename)
	if !op.IsRenditionType(linkType) && common.ShouldProxy(storage, filename) {
		Proxy(c)
		return
	}
	link, file, err := fs.Link(c.Request.Context(), rawPath, model.LinkArgs{
		IP:       c.ClientIP(),
		Header:   c.Request.Header,
		Type:     linkType,
		Redirect: true,
	})
	if err != nil {
		common.ErrorPage(c, err, 500)
		return
	}
	if common.IsGeneratedLink(link) {
		proxy(c, link, file, storage.GetStorage().ProxyRange)
		return
	}
	if common.ShouldProxy(storage, filename) {
		// The rendition could not be extracted; fall back to the usual handling.
		link.Close()
		Proxy(c)
		return
	}
	redirect(c, link)
}

func Proxy(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)
	common.LogMediaAccessAuto(c, rawPath)
	filename := stdpath.Base(rawPath)
	storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorPage(c, err, 500)
		return
	}
	// Renditions are small derived images built by this server, so they may be
	// served even where proxying the file itself is not allowed.
	linkType := rawLinkType(c, filename)
	rendition := op.IsRenditionType(linkType)
	allowed := canProxy(storage, filename)
	if !allowed && !rendition {
		common.ErrorPage(c, errors.New("proxy not allowed"), 403)
		return
	}
	if !rendition {
		if _, ok := c.GetQuery("d"); !ok {
			if url := common.GenerateDownProxyURL(storage.GetStorage(), rawPath); url != "" {
				c.Redirect(302, url)
				return
			}
		}
	}
	link, file, err := fs.Link(c.Request.Context(), rawPath, model.LinkArgs{
		Header: c.Request.Header,
		Type:   linkType,
	})
	if err != nil {
		common.ErrorPage(c, err, 500)
		return
	}
	if !allowed && !common.IsGeneratedLink(link) {
		// No rendition could be extracted, and streaming the original through the
		// server is not permitted here: let the client fetch it directly.
		redirect(c, link)
		return
	}
	proxy(c, link, file, storage.GetStorage().ProxyRange)
}

func redirect(c *gin.Context, link *model.Link) {
	defer link.Close()
	var err error
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Cache-Control", "max-age=0, no-cache, no-store, must-revalidate")
	if setting.GetBool(conf.ForwardDirectLinkParams) {
		query := c.Request.URL.Query()
		for _, v := range conf.SlicesMap[conf.IgnoreDirectLinkParams] {
			query.Del(v)
		}
		link.URL, err = utils.InjectQuery(link.URL, query)
		if err != nil {
			common.ErrorPage(c, err, 500)
			return
		}
	}
	c.Redirect(302, link.URL)
}

func proxy(c *gin.Context, link *model.Link, file model.Obj, proxyRange bool) {
	defer link.Close()
	var err error
	if link.URL != "" && setting.GetBool(conf.ForwardDirectLinkParams) {
		query := c.Request.URL.Query()
		for _, v := range conf.SlicesMap[conf.IgnoreDirectLinkParams] {
			query.Del(v)
		}
		link.URL, err = utils.InjectQuery(link.URL, query)
		if err != nil {
			common.ErrorPage(c, err, 500)
			return
		}
	}
	if proxyRange {
		link = common.ProxyRange(c, link, file.GetSize())
	}
	Writer := &common.WrittenResponseWriter{ResponseWriter: c.Writer}
	err = common.Proxy(Writer, c.Request, link, file)
	if err == nil {
		return
	}
	if Writer.IsWritten() {
		log.Errorf("%s %s local proxy error: %+v", c.Request.Method, c.Request.URL.Path, err)
	} else {
		if statusCode, ok := errs.UnwrapOrSelf(err).(net.HttpStatusCodeError); ok {
			common.ErrorPage(c, err, int(statusCode), true)
		} else {
			common.ErrorPage(c, err, 500, true)
		}
	}
}

// TODO need optimize
// when can be proxy?
// 1. text file
// 2. config.MustProxy()
// 3. storage.WebProxy
// 4. proxy_types
// solution: text_file + shouldProxy()
func canProxy(storage driver.Driver, filename string) bool {
	if storage.Config().MustProxy() || storage.GetStorage().WebProxy || storage.GetStorage().WebdavProxyURL() {
		return true
	}
	if utils.SliceContains(conf.SlicesMap[conf.ProxyTypes], utils.Ext(filename)) {
		return true
	}
	if utils.SliceContains(conf.SlicesMap[conf.TextTypes], utils.Ext(filename)) {
		return true
	}
	return false
}
