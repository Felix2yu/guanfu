// Package api 实现观复的 HTTP API（标准库 net/http，无框架依赖）。
//
// 路由：
//
//	GET  /api/v1/health          健康检查
//	GET  /api/v1/events          事件列表（date/from/to/source/type/limit/offset）
//	GET  /api/v1/events/counts   某日各源事件数（date）或范围每日总数（from/to，热力图）
//	GET  /api/v1/overview        总览聚合（from/to：区间合计 + 逐日收支 + 来源分布）
//	GET  /api/v1/range           库日期范围（日历边界）
//	GET  /api/v1/sources         源同步状态
//	GET  /api/v1/settings        端点配置（源 + 通知渠道）、策略开关
//	PUT  /api/v1/settings/endpoints/{name}   保存端点配置（页面优先）
//	DELETE /api/v1/settings/endpoints/{name} 删除页面配置（回到环境变量）
//	POST /api/v1/settings/endpoints/{name}/verify  连通性探测
//	POST /api/v1/settings/notify/{name}/test       通知渠道测试发送
//	PUT  /api/v1/settings/policy                   应用级开关
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
	"github.com/Felix2yu/guanfu/internal/settings"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/Felix2yu/guanfu/internal/sync"
)

// Server API 服务。
type Server struct {
	store  *store.Store
	engine *sync.Engine
	media  *MediaProxy
	mux    *http.ServeMux
	// settings 是「env + 页面配置」的唯一真相源；sources 是它当前生效的快照读法。
	// 用函数而不是字段快照，页面保存后无需通知本层刷新。
	settings *settings.Manager
	sources  func() map[string]config.SourceConfig
}

// NewServer 创建 API 服务并注册路由。mgr 提供生效配置与页面保存能力。
func NewServer(st *store.Store, engine *sync.Engine, mgr *settings.Manager) *Server {
	s := &Server{store: st, engine: engine, mux: http.NewServeMux(), settings: mgr}
	s.sources = func() map[string]config.SourceConfig { return mgr.Sources() }
	s.media = NewMediaProxy(s.sources)
	s.routes()
	return s
}

var sourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/v1/events/counts", s.handleCounts)
	s.mux.HandleFunc("GET /api/v1/overview", s.handleOverview)
	s.mux.HandleFunc("GET /api/v1/range", s.handleRange)
	s.mux.HandleFunc("GET /api/v1/sources", s.handleSources)
	s.mux.HandleFunc("GET /api/v1/settings", s.handleSettings)
	s.mux.HandleFunc("PUT /api/v1/settings/endpoints/{name}", s.handleSaveEndpoint)
	s.mux.HandleFunc("DELETE /api/v1/settings/endpoints/{name}", s.handleResetEndpoint)
	s.mux.HandleFunc("POST /api/v1/settings/endpoints/{name}/verify", s.handleVerifyEndpoint)
	s.mux.HandleFunc("POST /api/v1/settings/notify/{name}/test", s.handleTestNotify)
	s.mux.HandleFunc("PUT /api/v1/settings/policy", s.handleSavePolicy)
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

// dateRe 日期参数的基本形状校验。只挡明显的垃圾输入（空串、多段路径、SQL 片段），
// 真正的边界判断留给 local_date 的字符串比较（YYYY-MM-DD 定长可直接比较）。
var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// handleOverview 总览页聚合：区间合计、逐日收支、来源分布一次取回。
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if !dateRe.MatchString(from) || !dateRe.MatchString(to) {
		writeErr(w, http.StatusBadRequest, "需要 from 与 to（YYYY-MM-DD）")
		return
	}
	ov, err := s.store.Overview(from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "聚合失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": ov})
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
	srcs := s.sources()

	names := make([]string, 0, len(srcs)+len(states))
	seen := map[string]bool{}
	for name := range srcs {
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
		cfg, hasCfg := srcs[name]
		st := byName[name]
		auth := config.AuthKind(name)
		if hasCfg && config.IsValidAuth(cfg.Auth) {
			auth = cfg.Auth // 页面可能改过认证方式，展示要跟实际发的一致
		}
		v := sourceView{
			BaseURL:       cfg.BaseURL,
			BaseURLEnv:    config.BaseURLEnv(name),
			Auth:          auth,
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
			// 配置里显式给了间隔就以配置为准：页面刚改完、还没跑过一轮时，
			// sources 表里存的是旧值，不覆盖会让两处显示对不上。
			if cfg.Interval > 0 {
				v.IntervalSecs = int(cfg.Interval.Seconds())
			}
		}
		v.Configured = cfg.BaseURL != "" && (auth == config.AuthNone || v.CredentialSet || hasOptionalCred(name))
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
