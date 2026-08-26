package common

import (
	"fmt"
	stdpath "path"
	"strings"
	"sync"
	"time"

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

// 访问记录去重：20 秒内同一 IP 访问同一文件只记录一次，避免播放器 Range 请求刷屏。
var (
	accessCache     = make(map[string]time.Time)
	accessCacheLock sync.RWMutex
	dedupeWindow    = 20 * time.Second
)

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

// shouldLogAccess 检查是否应该记录此次访问（带去重）。
func shouldLogAccess(clientIP, rawPath string) bool {
	key := clientIP + "|" + rawPath
	now := time.Now()

	accessCacheLock.RLock()
	lastAccess, exists := accessCache[key]
	accessCacheLock.RUnlock()

	if exists && now.Sub(lastAccess) < dedupeWindow {
		return false
	}

	accessCacheLock.Lock()
	accessCache[key] = now
	if len(accessCache) > 1000 {
		for k, v := range accessCache {
			if now.Sub(v) > dedupeWindow*2 {
				delete(accessCache, k)
			}
		}
	}
	accessCacheLock.Unlock()

	return true
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
		if rawpreview.Handles(stdpath.Base(rawPath)) && rawpreview.Negotiate() &&
			AcceptsImageOnly(c.GetHeader("Accept")) {
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
func LogMediaAccess(c *gin.Context, rawPath string, username ...string) {
	accessType := AccessTypePreview
	// 分享链接和 /api/fs/get 都算预览，但取缩略图要如实记成缩略图。
	if t := renditionAccessType(c, rawPath); t != "" {
		accessType = t
	}
	if len(username) > 0 && username[0] != "" {
		LogMediaAccessWithTypeAs(c, rawPath, accessType, username[0])
	} else {
		LogMediaAccessWithType(c, rawPath, accessType)
	}
}

// LogMediaAccessWithType 记录媒体文件访问日志（指定类型）。
func LogMediaAccessWithType(c *gin.Context, rawPath string, accessType string) {
	if !IsMediaFile(rawPath) {
		return
	}

	clientIP := "unknown"
	if c != nil {
		clientIP = c.ClientIP()
	}

	if !shouldLogAccess(clientIP, rawPath) {
		return
	}

	username := "Guest"
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		if user, ok := c.Request.Context().Value(conf.UserKey).(*model.User); ok && user != nil {
			username = user.Username
		}
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

	fmt.Println("[媒体访问] " + logMsg)
}

// LogMediaAccessWithTypeAs records media access using the supplied username
// directly, bypassing the context lookup.
func LogMediaAccessWithTypeAs(c *gin.Context, rawPath string, accessType string, username string) {
	if !IsMediaFile(rawPath) {
		return
	}

	clientIP := "unknown"
	if c != nil {
		clientIP = c.ClientIP()
	}

	if !shouldLogAccess(clientIP, rawPath) {
		return
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

	fmt.Println("[媒体访问] " + logMsg)
}

// LogMediaAccessAuto 自动检测访问类型并记录日志。
func LogMediaAccessAuto(c *gin.Context, rawPath string) {
	LogMediaAccessWithType(c, rawPath, detectAccessType(c, rawPath))
}
