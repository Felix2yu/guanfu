package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Felix2yu/guanfu/internal/model"
)

// 手动补录通道（规划 §3.2 P2-9 的 ingest）：无源可归的事件（临时想法、
// Dawarich 断档时的地点补点）从这里进 events 表；页面顶栏「补一笔」表单也走这里。
//
//	POST /api/v1/ingest/event
//
// - 默认无鉴权，与 /api/v1/import、设置页同级的内网管理面（见 README 安全约定）；
// - 配了 GUANFU_INGEST_TOKEN 时要求 Bearer——用于经反代把端点暴露给
//   快捷指令 / curl 等外部自动化的场景（此时页面表单也需带同样的 token）；
// - source 固定为 "ingest"（伪源：不参与同步调度，只出现在事件里）；
// - external_id 必填且幂等：同 id 重复提交 = 更新同一条事件（补录的修改就走这条）。
func (s *Server) handleIngestEvent(w http.ResponseWriter, r *http.Request) {
	if s.ingestToken != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.ingestToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="guanfu-ingest"`)
			writeErr(w, http.StatusUnauthorized, "补录凭据不符（Authorization: Bearer <GUANFU_INGEST_TOKEN>）")
			return
		}
	}
	var req ingestEventReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	ev, err := req.toEvent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	changed, err := s.store.UpsertEvent(ev)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "写入失败: "+err.Error())
		return
	}
	ev.FillLocalTime()
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
		"changed": changed, "event": ev,
	}})
}

// ingestEventReq 补录请求体。date 必填；time（HH:MM）可省，省了算全天事件。
type ingestEventReq struct {
	ExternalID   string         `json:"external_id"`
	EventType    string         `json:"event_type"`
	Date         string         `json:"date"`
	Time         string         `json:"time"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	AmountCents  int64          `json:"amount_cents"`
	Currency     string         `json:"currency"`
	LocationName string         `json:"location_name"`
	Detail       map[string]any `json:"detail"`
	SourceURL    string         `json:"source_url"`
}

func (req *ingestEventReq) toEvent() (*model.Event, error) {
	id := strings.TrimSpace(req.ExternalID)
	if id == "" {
		return nil, fmt.Errorf("external_id 必填（幂等键：同 id 重复提交 = 更新同一条）")
	}
	if len(id) > 200 {
		return nil, fmt.Errorf("external_id 过长（≤200 字符）")
	}
	if !validExportDate(req.Date) {
		return nil, fmt.Errorf("date 需是有效日期（YYYY-MM-DD），如 2026-10-01")
	}
	typ := strings.TrimSpace(req.EventType)
	if typ == "" {
		typ = "note"
	}
	if len(typ) > 64 {
		return nil, fmt.Errorf("event_type 过长（≤64 字符）")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "补录"
	}

	loc := model.LocalLocation()
	day, _ := time.ParseInLocation("2006-01-02", req.Date, loc)
	ev := &model.Event{
		Source:       "ingest",
		ExternalID:   id,
		EventType:    typ,
		LocalDate:    req.Date,
		Title:        title,
		Summary:      req.Summary,
		AmountCents:  req.AmountCents,
		Currency:     req.Currency,
		LocationName: req.LocationName,
		Detail:       req.Detail,
		SourceURL:    req.SourceURL,
	}
	if t := strings.TrimSpace(req.Time); t != "" {
		hm, err := time.Parse("15:04", t)
		if err != nil {
			return nil, fmt.Errorf("time 需是 HH:MM（如 14:30）")
		}
		at := time.Date(day.Year(), day.Month(), day.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
		ev.TimePrecision = model.PrecisionSecond
		ev.OccurredAt = at.UTC().Unix()
	} else {
		// 全天事件：时刻固定当地中午，与其余 day 精度事件同口径。
		noon := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc)
		ev.TimePrecision = model.PrecisionDay
		ev.OccurredAt = noon.UTC().Unix()
	}
	if ev.AmountCents > 0 && ev.Currency == "" {
		ev.Currency = "CNY"
	}
	return ev, nil
}
