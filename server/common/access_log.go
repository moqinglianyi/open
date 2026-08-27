package common

import (
	"fmt"
	stdpath "path"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/rawpreview"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// 访问行为类型
const (
	AccessTypePreview  = "在线预览"
	AccessTypeDownload = "下载"
	AccessTypePlayer   = "播放器"
	AccessTypeThumb    = "缩略图"
)

// 访问记录去重：窗口内同一 IP 访问同一文件只记录一条。
//
// 打开一个文件在服务端是好几个请求：先 /api/fs/get 拿元数据（记“在线预览”），
// 浏览器或播放器再去请求 /d/ 或 /p/ 取内容（按请求特征判断，可能记成“下载”）。
// 两者是同一次访问，所以只应留下一条日志。窗口是滑动的：窗口内每次命中都把
// 时间往后推，因此取内容的请求隔多久到达都不会多出一条，播放过程中的 Range
// 请求也不会每隔一段时间刷一条。
var (
	accessCache     = make(map[string]accessEntry)
	accessCacheLock sync.Mutex
	dedupeWindow    = 5 * time.Minute
)

// accessEntry 是窗口内已经记过的那一条，kind 用来判断后来的请求是不是更有意义。
type accessEntry struct {
	at   time.Time
	kind string
}

// 缓存条目上限。超过后清掉窗口外的条目，避免长期运行无限增长。
const maxAccessCacheEntries = 1000

// 同一个 HTTP 请求只记一条：Down 在需要时会转交给 Proxy，两个处理函数都会记日志。
const accessLoggedKey = "media_access_logged"

// 常见图片格式。相机 RAW 的扩展名由 rawpreview 统一维护，不在这里重复。
var imageExtensions = []string{
	"jpg", "jpeg", "png", "gif", "bmp", "webp", "svg", "ico", "tiff", "tif",
	"heic", "heif", "avif",
}

// 常见视频格式
var videoExtensions = []string{
	"mp4", "mkv", "avi", "mov", "wmv", "flv", "webm", "m4v", "mpeg", "mpg",
	"3gp", "3g2", "ts", "mts", "m2ts", "vob", "ogv", "rm", "rmvb", "asf",
	"f4v", "divx", "xvid",
}

// IsMediaFile 检查文件是否为图片或视频格式。
func IsMediaFile(filename string) bool {
	name := stdpath.Base(filename)
	if rawpreview.IsRaw(name) {
		return true
	}
	ext := strings.ToLower(utils.Ext(name))
	for _, e := range imageExtensions {
		if ext == e {
			return true
		}
	}
	for _, e := range videoExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// isImageFile reports whether name is an ordinary image the browser can decode by
// itself. Camera RAW is deliberately excluded: it is only viewable as a rendition,
// which renditionAccessType decides separately.
func isImageFile(name string) bool {
	ext := strings.ToLower(utils.Ext(stdpath.Base(name)))
	for _, e := range imageExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// shouldLogAccess reports whether this hit starts a new access, and records it.
// A hit inside the window is the same access as the one already logged, so it is
// swallowed and pushes the window out instead. The one exception is a thumbnail:
// the listing grid fetches those on its own, and it must not stand in for the
// view or download the visitor goes on to make.
func shouldLogAccess(clientIP, rawPath, accessType string) bool {
	key := clientIP + "|" + rawPath
	now := time.Now()

	accessCacheLock.Lock()
	defer accessCacheLock.Unlock()

	prev, exists := accessCache[key]
	fresh := !exists || now.Sub(prev.at) >= dedupeWindow
	upgrade := !fresh && prev.kind == AccessTypeThumb && accessType != AccessTypeThumb

	entry := accessEntry{at: now, kind: accessType}
	if !fresh && !upgrade {
		// 记在案的那条才是这次访问的定性，后续请求只顺延时间。
		entry.kind = prev.kind
	}
	accessCache[key] = entry

	if fresh && len(accessCache) > maxAccessCacheEntries {
		for k, v := range accessCache {
			if now.Sub(v.at) > dedupeWindow {
				delete(accessCache, k)
			}
		}
	}
	return fresh || upgrade
}

// requestLoggedOnce reports whether this request has already written a line, and
// claims the right to write one otherwise. Down hands the request over to Proxy
// when the storage has to be streamed, and both of them log.
func requestLoggedOnce(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if _, done := c.Get(accessLoggedKey); done {
		return true
	}
	c.Set(accessLoggedKey, true)
	return false
}

// renditionAccessType reports the access type implied by the rendition a request
// asks for, or "" when the request is for the file itself. A JPEG extracted from a
// RAW photo is served by the download endpoint, but fetching it is viewing, not
// downloading: ?type=preview is the image viewer, ?type=thumb is the directory
// listing, and a request with no type that only accepts images is the viewer too,
// because that is exactly what content negotiation hands a rendition to.
func renditionAccessType(c *gin.Context, rawPath string) string {
	if c == nil || c.Request == nil {
		return ""
	}
	switch c.Query("type") {
	case op.LinkTypeThumb:
		return AccessTypeThumb
	case op.LinkTypePreview:
		return AccessTypePreview
	case "":
		name := stdpath.Base(rawPath)
		if !AcceptsImageOnly(c.GetHeader("Accept")) {
			return ""
		}
		if rawpreview.Handles(name) {
			// RAW 只有开着内容协商时才会换成内嵌 JPEG，关掉时 /d/ 给的是原始文件。
			if rawpreview.Negotiate() {
				return AccessTypePreview
			}
			return ""
		}
		// 只接受图片的请求来自 <img>、CSS 背景和 preload：图片列表和查看器就是这样
		// 取图的，下载工具发的是 */* 或者什么都不发。
		if isImageFile(name) {
			return AccessTypePreview
		}
	}
	return ""
}

// detectAccessType 按请求要的画质、User-Agent 和请求路径自动检测访问类型。
func detectAccessType(c *gin.Context, rawPath string) string {
	if c == nil || c.Request == nil {
		return AccessTypeDownload
	}

	// 显式要缩略图或预览图的请求意图明确，比 User-Agent 更可信，先判断。
	if t := renditionAccessType(c, rawPath); t != "" {
		return t
	}

	userAgent := strings.ToLower(c.Request.UserAgent())
	requestPath := c.Request.URL.Path

	playerKeywords := []string{
		"vlc", "mpv", "potplayer", "mpc-hc", "mpc-be", "kodi", "plex",
		"infuse", "iina", "nplayer", "oplayer", "avplayer", "kmplayer",
		"gom", "daum", "lavf", "ffmpeg", "libmpv", "exoplayer",
		"stagefright", "android.media", "quicktime", "windows-media",
	}
	for _, keyword := range playerKeywords {
		if strings.Contains(userAgent, keyword) {
			return AccessTypePlayer
		}
	}

	if strings.HasPrefix(requestPath, "/d/") {
		return AccessTypeDownload
	}
	if strings.HasPrefix(requestPath, "/p/") {
		return AccessTypePreview
	}
	return AccessTypeDownload
}

// LogMediaAccess records preview access. An optional username can be supplied
// to skip the context lookup (useful when the caller already has the user).
func LogMediaAccess(c *gin.Context, rawPath string, username ...string) bool {
	accessType := AccessTypePreview
	// 分享链接和 /api/fs/get 都算预览，但取缩略图要如实记成缩略图。
	if t := renditionAccessType(c, rawPath); t != "" {
		accessType = t
	}
	if len(username) > 0 && username[0] != "" {
		return LogMediaAccessWithTypeAs(c, rawPath, accessType, username[0])
	}
	return LogMediaAccessWithType(c, rawPath, accessType)
}

// LogMediaAccessWithType 记录媒体文件访问日志（指定类型）。
// 返回是否真的写了一条：被去重吃掉时返回 false。
func LogMediaAccessWithType(c *gin.Context, rawPath string, accessType string) bool {
	username := "Guest"
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		if user, ok := c.Request.Context().Value(conf.UserKey).(*model.User); ok && user != nil {
			username = user.Username
		}
	}
	return LogMediaAccessWithTypeAs(c, rawPath, accessType, username)
}

// LogMediaAccessWithTypeAs records media access using the supplied username
// directly, bypassing the context lookup.
func LogMediaAccessWithTypeAs(c *gin.Context, rawPath string, accessType string, username string) bool {
	if !IsMediaFile(rawPath) {
		return false
	}
	if requestLoggedOnce(c) {
		return false
	}

	clientIP := "unknown"
	if c != nil {
		clientIP = c.ClientIP()
	}

	if !shouldLogAccess(clientIP, rawPath, accessType) {
		return false
	}

	timeStr := time.Now().Format("2006年01月02日 15:04:05")
	logMsg := fmt.Sprintf("时间：%s 访问IP：%s 用户：%s 行为：%s 访问路径：%s",
		timeStr, clientIP, username, accessType, rawPath)

	log.WithFields(log.Fields{
		"type":        "media_access",
		"ip":          clientIP,
		"user":        username,
		"access_type": accessType,
		"path":        rawPath,
	}).Info("[媒体访问] " + logMsg)

	// logrus 写的是日志文件时，终端还看不到这条，补一行；它本来就在往终端写的
	// 时候不能再补，否则同一次访问在终端上会出现两行。
	if consoleEchoNeeded() {
		fmt.Println("[媒体访问] " + logMsg)
	}
	return true
}

// consoleEchoNeeded reports whether logrus output is going somewhere the operator
// is not watching, which is the only case where this package prints as well. It
// mirrors the writer bootstrap.Log picks: the log file only, unless the log is
// disabled (stderr) or one of the flags puts stdout in front of the file.
func consoleEchoNeeded() bool {
	return conf.Conf != nil && conf.Conf.Log.Enable &&
		!flags.Debug && !flags.Dev && !flags.LogStd
}

// LogMediaAccessAuto 自动检测访问类型并记录日志。
func LogMediaAccessAuto(c *gin.Context, rawPath string) bool {
	return LogMediaAccessWithType(c, rawPath, detectAccessType(c, rawPath))
}
