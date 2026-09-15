// 媒体访问日志：记录谁在什么时候看了哪个图片或视频，并尽量如实写出那一次访问
// 究竟是什么行为——在线预览、下载、播放器还是缩略图。
//
// 打开一个文件在服务端不是一个请求，而是一串：
//
//	/api/fs/get      取元数据，前端据此渲染查看器   → LogMediaAccess（在线预览）
//	/d/…  /p/…       浏览器或播放器真正取内容       → LogMediaAccessAuto（自动判定）
//	/sd/…            分享链接取内容                 → LogMediaAccess
//
// 它们属于同一次访问，所以有两层去重：requestLoggedOnce 保证同一个 HTTP 请求只写
// 一条（Down 在需要时会转交给 Proxy，两个处理函数都会记），shouldLogAccess 保证
// 窗口内同一 IP 访问同一文件只写一条（一场播放里的 Range 请求不会反复刷屏）。
//
// 行为判定的可信度从高到低是：请求明说要派生图（?type=thumb|preview）> 请求只能
// 接受图片（Accept）> User-Agent 像播放器 > 路径前缀。RAW 照片的内嵌 JPEG 同样是
// 从 /d/ 发出去的，只看路径前缀会把「看图」记成「下载」。

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

// 访问行为类型，直接写进日志正文。
const (
	AccessTypePreview  = "在线预览"
	AccessTypeDownload = "下载"
	AccessTypePlayer   = "播放器"
	AccessTypeThumb    = "缩略图"
)

// ---------------------------------------------------------------- 文件类型判定

// 常见图片格式。相机 RAW 的扩展名由 rawpreview 统一维护，不在这里重复，免得以后
// 多支持一种格式，日志这边悄悄漏掉。
var imageExtensions = extSet(
	"jpg", "jpeg", "png", "gif", "bmp", "webp", "svg", "ico", "tiff", "tif",
	"heic", "heif", "avif",
)

// 常见视频格式。
var videoExtensions = extSet(
	"mp4", "mkv", "avi", "mov", "wmv", "flv", "webm", "m4v", "mpeg", "mpg",
	"3gp", "3g2", "ts", "mts", "m2ts", "vob", "ogv", "rm", "rmvb", "asf",
	"f4v", "divx", "xvid",
)

func extSet(exts ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		set[e] = struct{}{}
	}
	return set
}

// IsMediaFile 判断路径指向的是不是图片或视频：只有它们才写访问日志。判断只看最后
// 一段的扩展名，所以 /photos/trip.cr2/inner.txt 不会被父目录的名字带偏。
func IsMediaFile(filename string) bool {
	name := stdpath.Base(filename)
	if rawpreview.IsRaw(name) {
		return true
	}
	ext := utils.Ext(name) // 已经是小写、不带点
	if _, ok := imageExtensions[ext]; ok {
		return true
	}
	_, ok := videoExtensions[ext]
	return ok
}

// isImageFile 判断浏览器能不能自己解码这张图。相机 RAW 故意不算在内：它只有换成
// 内嵌 JPEG 才看得见，那一步由 renditionAccessType 单独判断。
func isImageFile(name string) bool {
	_, ok := imageExtensions[utils.Ext(stdpath.Base(name))]
	return ok
}

// ------------------------------------------------------------------ 对外入口

// LogMediaAccess 记录一次预览性质的访问，调用方是 /api/fs/get 和分享链接——请求它们
// 本身就意味着有人在看这个文件。username 可选：调用方已经拿到用户时传进来，省一次
// 上下文查找。返回是否真的写了一条，被去重吃掉时返回 false。
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

// LogMediaAccessAuto 记录一次取内容的访问（/d/、/p/、/sd/），行为按请求特征自动判定。
func LogMediaAccessAuto(c *gin.Context, rawPath string) bool {
	return LogMediaAccessWithType(c, rawPath, detectAccessType(c, rawPath))
}

// LogMediaAccessWithType 记录指定行为的访问，用户名从请求上下文里取。
func LogMediaAccessWithType(c *gin.Context, rawPath string, accessType string) bool {
	username := "Guest"
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		if user, ok := c.Request.Context().Value(conf.UserKey).(*model.User); ok && user != nil {
			username = user.Username
		}
	}
	return LogMediaAccessWithTypeAs(c, rawPath, accessType, username)
}

// LogMediaAccessWithTypeAs 用调用方给定的用户名记录访问，是真正落盘的那一层：先过
// 媒体文件这道门，再过两层去重，最后写一条 logrus 记录。
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

// consoleEchoNeeded 判断 logrus 的输出是不是去了运维看不见的地方，只有那时才由本包
// 再往终端打一行。判断依据和 bootstrap.Log 挑 writer 的条件一致：默认只写日志文件，
// 除非日志被关掉（转 stderr），或者 debug/dev/log-std 这些开关把 stdout 摆在前面。
func consoleEchoNeeded() bool {
	return conf.Conf != nil && conf.Conf.Log.Enable &&
		!flags.Debug && !flags.Dev && !flags.LogStd
}

// ------------------------------------------------------------------ 行为判定

// renditionAccessType 返回请求所要的「派生图」对应的行为，请求要的是文件本身时返回
// ""。从 RAW 里抽出来的 JPEG 是走下载接口发出去的，但取它是在看图，不是下载：
// ?type=preview 是查看器，?type=thumb 是目录列表，不带 type 而 Accept 只接受图片的
// 请求同样是查看器——内容协商正是在这种请求上把原文件换成内嵌 JPEG 的。
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
		if !AcceptsImageOnly(c.GetHeader("Accept")) {
			return ""
		}
		name := stdpath.Base(rawPath)
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

// 外部播放器的 User-Agent 特征。它们既不是下载工具，也不是浏览器预览，单列一类。
var playerKeywords = []string{
	"vlc", "mpv", "potplayer", "mpc-hc", "mpc-be", "kodi", "plex",
	"infuse", "iina", "nplayer", "oplayer", "avplayer", "kmplayer",
	"gom", "daum", "lavf", "ffmpeg", "libmpv", "exoplayer",
	"stagefright", "android.media", "quicktime", "windows-media",
}

// detectAccessType 判定一次取内容的请求算什么行为。
func detectAccessType(c *gin.Context, rawPath string) string {
	if c == nil || c.Request == nil {
		return AccessTypeDownload
	}
	// 显式要缩略图或预览图的请求意图明确，比 User-Agent 更可信，先判断。
	if t := renditionAccessType(c, rawPath); t != "" {
		return t
	}
	userAgent := strings.ToLower(c.Request.UserAgent())
	for _, keyword := range playerKeywords {
		if strings.Contains(userAgent, keyword) {
			return AccessTypePlayer
		}
	}
	// 剩下的按接口分：/p/ 生来就是给人看的，/d/ 和其它一律算下载。
	if strings.HasPrefix(c.Request.URL.Path, "/p/") {
		return AccessTypePreview
	}
	return AccessTypeDownload
}

// -------------------------------------------------------------------- 去重

// accessEntry 是窗口内已经记下的那一条：at 是最后一次命中的时间，kind 是当时写进
// 日志的行为，用来判断后来的请求有没有更值得记。
type accessEntry struct {
	at   time.Time
	kind string
}

var (
	accessCache     = make(map[string]accessEntry)
	accessCacheLock sync.Mutex
	// 去重窗口。取 5 分钟是因为「点开文件」和「外部播放器真正来取内容」之间可能隔
	// 很久；而窗口是滑动的，窗口内每次命中都把时间往后推，所以一场播放里的 Range
	// 请求无论持续多久都不会多出第二条。
	dedupeWindow = 5 * time.Minute
)

// 缓存条目上限。超过后清掉窗口外的条目，避免长期运行无限增长。窗口内的条目不能删，
// 删了等于放弃去重，所以短时间里同时访问很多文件时条目数可以暂时超过这个数。
const maxAccessCacheEntries = 1000

// shouldLogAccess 判断这次命中算不算一次新的访问，并把它记下来。窗口内的命中和已经
// 记过的那条属于同一次访问，于是被吃掉，同时把窗口往后推。唯一的例外是缩略图：列表
// 页自己就会去取缩略图，它不能替掉访客随后真正的查看或下载。
func shouldLogAccess(clientIP, rawPath, accessType string) bool {
	key := clientIP + "|" + rawPath
	now := time.Now()

	accessCacheLock.Lock()
	defer accessCacheLock.Unlock()

	prev, exists := accessCache[key]
	fresh := !exists || now.Sub(prev.at) >= dedupeWindow
	// 缩略图之后来了别的行为，就升级成那条更有意义的记录。升级后 kind 不再是缩略图，
	// 所以一个窗口里只会升级一次，窗口本身也不会被重新打开。
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

// 同一个 HTTP 请求只记一条：Down 在需要时会转交给 Proxy，两个处理函数都会记日志。
const accessLoggedKey = "media_access_logged"

// requestLoggedOnce 判断这个请求是不是已经写过一条，没写过就先把「写」这件事占下来。
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
