package api

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
)

// 媒体代理：把各源的媒体引用转为观复自有端点，前端不直连源站
// （源站 token/防盗链不外泄，URL 形态统一）。
//
//	mujian       GET /api/v1/media/mujian/{uploadsKey}   → {mujian}/uploads/{key}
//	bili-history GET /api/v1/media/bili-history?u=<url>  → {bili}/api/images/proxy?url=<url>
//
// M3 接入 immich 时在此追加分支（thumbnail/original 两尺寸）。

// mediaKeyRe mujian uploads 存储键字符集（covers/<hash>.<ext>），
// 同时挡住路径穿越（无 ".."、不允 Leading slash 由路由保证）。
var mediaKeyRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/._-]*$`)

// biliCoverHostRe B 站封面 CDN 域名（防开放代理被挪作 SSRF 用途）。
var biliCoverHostRe = regexp.MustCompile(`(^|\.)hdslb\.com$`)

// MediaProxy 媒体代理（持各源连接信息，仅用于回源）。
type MediaProxy struct {
	origins map[string]config.SourceConfig
	client  *http.Client
}

// NewMediaProxy 基于配置构建代理。未配置 BaseURL 的源返回 404。
func NewMediaProxy(sources map[string]config.SourceConfig) *MediaProxy {
	return &MediaProxy{
		origins: sources,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Routes 注册媒体路由。
func (p *MediaProxy) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/media/mujian/{key...}", p.handleMujianMedia)
	mux.HandleFunc("GET /api/v1/media/bili-history", p.handleBiliMedia)
}

// ProxyPathFor 为事件的媒体引用生成代理路径（API 输出时调用）。
// 返回空串表示该源暂无代理分支（前端应跳过渲染）。
func ProxyPathFor(source string, mediaID string) string {
	switch source {
	case "mujian":
		// mediaID 即 uploads 存储键（covers/<hash>.<ext>）。
		if !mediaKeyRe.MatchString(mediaID) || strings.Contains(mediaID, "..") {
			return ""
		}
		return "/api/v1/media/mujian/" + mediaID
	case "bili-history":
		// mediaID 为 B 站封面外链。
		u, err := url.Parse(mediaID)
		if err != nil || u.Scheme != "https" || !biliCoverHostRe.MatchString(u.Hostname()) {
			return ""
		}
		return "/api/v1/media/bili-history?u=" + url.QueryEscape(mediaID)
	default:
		return ""
	}
}

func (p *MediaProxy) handleMujianMedia(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" || !mediaKeyRe.MatchString(key) || strings.Contains(key, "..") {
		writeErr(w, http.StatusBadRequest, "非法媒体键")
		return
	}
	origin, ok := p.origins["mujian"]
	if !ok || origin.BaseURL == "" {
		writeErr(w, http.StatusNotFound, "mujian 未配置")
		return
	}
	target := origin.BaseURL + "/uploads/" + key
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "构造请求失败")
		return
	}
	// mujian uploads 静态路由无认证；配了 token 的实例带上更稳。
	if origin.APIKey != "" {
		req.Header.Set("X-Auth-Token", origin.APIKey)
	}
	p.pipe(w, req)
}

func (p *MediaProxy) handleBiliMedia(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("u")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !biliCoverHostRe.MatchString(u.Hostname()) {
		writeErr(w, http.StatusBadRequest, "非法封面地址")
		return
	}
	origin, ok := p.origins["bili-history"]
	if !ok || origin.BaseURL == "" {
		writeErr(w, http.StatusNotFound, "bili-history 未配置")
		return
	}
	// 回源上游 images/proxy（其带 Referer+SESSDATA，规避 B 站防盗链）。
	target := origin.BaseURL + "/api/images/proxy?url=" + url.QueryEscape(raw)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "构造请求失败")
		return
	}
	p.pipe(w, req)
}

// pipe 回源并流转给客户端（带上游 Content-Type，限长防滥用）。
func (p *MediaProxy) pipe(w http.ResponseWriter, req *http.Request) {
	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("[media] 回源失败 %s: %v", req.URL, err)
		writeErr(w, http.StatusBadGateway, "媒体回源失败")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeErr(w, resp.StatusCode, fmt.Sprintf("媒体上游返回 %d", resp.StatusCode))
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	// 媒体内容短期缓存（封面/照片不常变），降低回源压力。
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, resp.Body, 32<<20) // 32MB 上限：原图/视频封面够用
}
