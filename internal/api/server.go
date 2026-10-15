// Package api 实现观复的 HTTP API（标准库 net/http，无框架依赖）。
//
// 路由：
//
//	GET  /api/v1/health          健康检查
//	GET  /api/v1/events          事件列表（date/from/to/source/type/limit/offset）
//	GET  /api/v1/events/counts   某日各源事件数（date）或范围每日总数（from/to，热力图）
//	GET  /api/v1/range           库日期范围（日历边界）
//	GET  /api/v1/sources         源同步状态
//	POST /api/v1/sync/{source}   手动同步（{source}=all 表示全部）
//	GET  /api/v1/media/...       媒体代理（见 media.go）
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/Felix2yu/guanfu/internal/sync"
)

// Server API 服务。
type Server struct {
	store  *store.Store
	engine *sync.Engine
	media  *MediaProxy
	mux    *http.ServeMux
}

// NewServer 创建 API 服务并注册路由。sources 用于媒体代理回源。
func NewServer(st *store.Store, engine *sync.Engine, sources map[string]config.SourceConfig) *Server {
	s := &Server{store: st, engine: engine, mux: http.NewServeMux()}
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

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	states, err := s.store.ListSourceStates()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": states})
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
