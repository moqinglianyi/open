package common

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// 访问行为类型
const (
	AccessTypePreview  = "在线预览"
	AccessTypeDownload = "下载"
	AccessTypePlayer   = "播放器"
)

// 访问记录去重：20 秒内同一 IP 访问同一文件只记录一次，避免播放器 Range 请求刷屏。
var (
	accessCache     = make(map[string]time.Time)
	accessCacheLock sync.RWMutex
	dedupeWindow    = 20 * time.Second
)

// 常见图片格式
var imageExtensions = []string{
	"jpg", "jpeg", "png", "gif", "bmp", "webp", "svg", "ico", "tiff", "tif",
	"raw", "cr2", "nef", "arw", "dng", "heic", "heif", "avif",
}

// 常见视频格式
var videoExtensions = []string{
	"mp4", "mkv", "avi", "mov", "wmv", "flv", "webm", "m4v", "mpeg", "mpg",
	"3gp", "3g2", "ts", "mts", "m2ts", "vob", "ogv", "rm", "rmvb", "asf",
	"f4v", "divx", "xvid",
}

// IsMediaFile 检查文件是否为图片或视频格式。
func IsMediaFile(filename string) bool {
	ext := strings.ToLower(utils.Ext(filename))
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

// detectAccessType 按 User-Agent 和请求路径自动检测访问类型。
func detectAccessType(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return AccessTypeDownload
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
	if len(username) > 0 && username[0] != "" {
		LogMediaAccessWithTypeAs(c, rawPath, AccessTypePreview, username[0])
	} else {
		LogMediaAccessWithType(c, rawPath, AccessTypePreview)
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
	LogMediaAccessWithType(c, rawPath, detectAccessType(c))
}
