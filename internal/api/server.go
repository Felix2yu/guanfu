// Package api 实现观复的 HTTP API（标准库 net/http，无框架依赖）。
//
// 路由：
//
//	GET  /api/v1/health          健康检查
//	GET  /api/v1/events          事件列表（date/from/to/source/type/limit/offset）
//	GET  /api/v1/events/counts   某日各源事件数（date）或范围每日总数（from/to，热力图）
//	GET  /api/v1/range           库日期范围（日历边界）
//	GET  /api/v1/sources         源同步状态
//	GET  /api/v1/export          全量/区间导出（JSON，流式；防锁定）
//	POST /api/v1/sync/{source}   手动同步（{source}=all 表示全部）
//	GET  /api/v1/media/...       媒体代理（见 media.go）
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/Felix2yu/guanfu/internal/sync"
)

// Server API 服务。
type Server struct {
	store   *store.Store
	engine  *sync.Engine
	media   *MediaProxy
	mux     *http.ServeMux
	sources map[string]config.SourceConfig
}

// NewServer 创建 API 服务并注册路由。sources 用于媒体代理回源与设置页展示配置状态。
func NewServer(st *store.Store, engine *sync.Engine, sources map[string]config.SourceConfig) *Server {
	s := &Server{store: st, engine: engine, mux: http.NewServeMux(), sources: sources}
	s.media = NewMediaProxy(sources)
	s.routes()
	return s
}

var sourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/v1/events/counts", s.handleCounts)
	s.mux.HandleFunc("GET /api/v1/range", s.handleRange)
	s.mux.HandleFunc("GET /api/v1/sources", s.handleSources)
	s.mux.HandleFunc("GET /api/v1/export", s.handleExport)
	s.mux.HandleFunc("POST /api/v1/sync/{source}", s.handleSync)
	s.media.Routes(s.mux)
}

// Handler 实现 http.Handler（供 main 与测试复用）。
func (s *Server) Handler() http.Handler { return s.mux }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"code": code, "message": msg})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "status": "ok", "time": time.Now().Unix(),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	eq := store.EventQuery{
		Date:      q.Get("date"),
		From:      q.Get("from"),
		To:        q.Get("to"),
		Source:    q.Get("source"),
		EventType: q.Get("type"),
	}
	if n := q.Get("limit"); n != "" {
		if v, err := strconv.Atoi(n); err == nil {
			eq.Limit = v
		}
	}
	if n := q.Get("offset"); n != "" {
		if v, err := strconv.Atoi(n); err == nil {
			eq.Offset = v
		}
	}
	events, err := s.store.ListEvents(eq)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询事件失败: "+err.Error())
		return
	}
	// 本地时刻由服务端按 GUANFU_TZ 现算下发，前端不依赖浏览器时区。
	for _, ev := range events {
		ev.FillLocalTime()
		// 媒体代理路径按源拼装（前端只认观复自己的端点）。
		for i := range ev.Media {
			ev.Media[i].ProxyPath = ProxyPathFor(ev.Source, ev.Media[i].ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": events})
}

// handleCounts 两种模式：
//   - date=YYYY-MM-DD          → 该日各源事件数 {source: n}
//   - from&to=YYYY-MM-DD       → 范围内每日总数 {date: n}（日历热力图）
func (s *Server) handleCounts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from != "" || to != "" {
		if from == "" || to == "" {
			writeErr(w, http.StatusBadRequest, "范围模式需要同时提供 from 与 to（YYYY-MM-DD）")
			return
		}
		if from > to {
			from, to = to, from
		}
		counts, err := s.store.CountsByRange(from, to)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "查询失败: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": counts})
		return
	}
	date := q.Get("date")
	if date == "" {
		writeErr(w, http.StatusBadRequest, "缺少 date 参数（YYYY-MM-DD），或提供 from/to 范围")
		return
	}
	counts, err := s.store.CountByDate(date)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": counts})
}

func (s *Server) handleRange(w http.ResponseWriter, r *http.Request) {
	min, max, err := s.store.DateRange()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]string{"min": min, "max": max}})
}

// sourceView 设置页要看的：同步状态 + 连接配置（**永不含凭据值**，只给"配没配、配在哪"）。
type sourceView struct {
	store.SourceState
	BaseURL       string `json:"base_url"`
	BaseURLEnv    string `json:"base_url_env"`
	Auth          string `json:"auth"`
	CredentialEnv string `json:"credential_env"`
	CredentialSet bool   `json:"credential_set"`
	// Configured = 端点已填且凭据满足要求，即"这一源能跑起来了"。
	Configured bool `json:"configured"`
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	states, err := s.store.ListSourceStates()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	byName := make(map[string]store.SourceState, len(states))
	for _, st := range states {
		byName[st.Name] = st
	}
	// 配置里的源 + 库里出现过的源都要列出来：只同步过一次或从没跑过的源，
	// 在设置页消失等于让用户以为"没配上"。
	names := make([]string, 0, len(s.sources)+len(states))
	seen := map[string]bool{}
	for name := range s.sources {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, st := range states {
		if !seen[st.Name] {
			seen[st.Name] = true
			names = append(names, st.Name)
		}
	}
	sort.Strings(names)

	out := make([]sourceView, 0, len(names))
	for _, name := range names {
		cfg, hasCfg := s.sources[name]
		st := byName[name]
		v := sourceView{
			BaseURL:       cfg.BaseURL,
			BaseURLEnv:    config.BaseURLEnv(name),
			Auth:          config.AuthKind(name),
			CredentialEnv: config.CredentialEnv(name),
			CredentialSet: cfg.APIKey != "",
		}
		v.SourceState = st
		if v.Name == "" {
			v.Name = name
		}
		if hasCfg {
			// 从没跑过的源库里没有行，enabled/interval 直接按配置给。
			v.Enabled = cfg.Enabled && cfg.BaseURL != ""
			if st.IntervalSecs == 0 && cfg.Interval > 0 {
				v.IntervalSecs = int(cfg.Interval.Seconds())
			}
		}
		v.Configured = cfg.BaseURL != "" && (v.Auth == config.AuthNone || v.CredentialSet || hasOptionalCred(name))
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": out})
}

// hasOptionalCred 这些源的 token 是可选的（服务端没配 token 时直接放通），
// 不能因为没填凭据就在设置页标成"没配好"。
func hasOptionalCred(name string) bool {
	switch name {
	case "mujian", "qiansi", "shenshi":
		return true
	}
	return false
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("source")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "缺少 source 路径参数")
		return
	}
	if !sourceNameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "非法 source 名")
		return
	}
	s.engine.Trigger(name)
	log.Printf("[api] 手动触发同步: %s", name)
	writeJSON(w, http.StatusAccepted, map[string]any{"code": 0, "message": "已触发同步: " + name})
}
