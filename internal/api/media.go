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
//	immich       GET /api/v1/media/immich/{assetID}?size= → {immich}/api/assets/{id}/thumbnail?size=
//	             thumbnail|preview|fullsize|original      （original 走 /original 端点）

// mediaKeyRe mujian uploads 存储键字符集（covers/<hash>.<ext>），
// 同时挡住路径穿越（无 ".."、不允 Leading slash 由路由保证）。
var mediaKeyRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/._-]*$`)

// biliCoverHostRe B 站封面 CDN 域名（防开放代理被挪作 SSRF 用途）。
var biliCoverHostRe = regexp.MustCompile(`(^|\.)hdslb\.com$`)

// immichAssetIDRe Immich 资产 ID 为 UUID v4（防路径穿越/注入）。
var immichAssetIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// immichSizes 允许代理的缩略图尺寸（original 走独立端点）。
var immichSizes = map[string]bool{"thumbnail": true, "preview": true, "fullsize": true}

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
	mux.HandleFunc("GET /api/v1/media/immich/{assetID}", p.handleImmichMedia)
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
	case "immich":
		// mediaID 为资产 UUID；默认缩略图（卡片网格），前端可按需追加
		// ?size=preview|fullsize|original（灯箱/原图）。
		if !immichAssetIDRe.MatchString(mediaID) {
			return ""
		}
		return "/api/v1/media/immich/" + mediaID
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
	// 缩略图/预览 32MB 足够；原图放宽到 256MB（RAW/长视频仍可能超，属可接受降级）。
	limit := int64(32 << 20)
	if req.URL.Query().Get("size") == "original" || strings.HasSuffix(req.URL.Path, "/original") {
		limit = 256 << 20
	}
	_, _ = io.CopyN(w, resp.Body, limit)
}

// handleImmichMedia 代理 Immich 资产媒体。
//
//	?size=thumbnail|preview|fullsize → {immich}/api/assets/{id}/thumbnail?size=<size>
//	?size=original                   → {immich}/api/assets/{id}/original
//	缺省 thumbnail。
//
// 上游 thumbnail 端点在素材缺失/需更大尺寸时会 302 重定向（相对路径），
// http.Client 自动跟随，无需特殊处理。
func (p *MediaProxy) handleImmichMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("assetID")
	if !immichAssetIDRe.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "非法资产 ID")
		return
	}
	origin, ok := p.origins["immich"]
	if !ok || origin.BaseURL == "" || origin.APIKey == "" {
		writeErr(w, http.StatusNotFound, "immich 未配置")
		return
	}
	size := r.URL.Query().Get("size")
	if size == "" {
		size = "thumbnail"
	}
	var target string
	switch {
	case size == "original":
		target = origin.BaseURL + "/api/assets/" + id + "/original"
	case immichSizes[size]:
		target = origin.BaseURL + "/api/assets/" + id + "/thumbnail?size=" + url.QueryEscape(size)
	default:
		writeErr(w, http.StatusBadRequest, "非法 size 参数（thumbnail/preview/fullsize/original）")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "构造请求失败")
		return
	}
	req.Header.Set("x-api-key", origin.APIKey)
	p.pipe(w, req)
}
