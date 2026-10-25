package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Felix2yu/guanfu/internal/model"
)

// 导入：把 /api/v1/export 的 JSON 搬回观复（迁移实例、误清后回填）。
//
//	POST /api/v1/import
//	Body: {"meta":{…},"events":[…]}（export 原样格式）
//
// - 按记录 upsert：同 (source, external_id) 更新，**不删除**未包含的现有数据；
// - source 只收已登记的数据源名与 "ingest"，防止手滑把脏源名写进库；
// - 与设置页同级的管理面：无鉴权，只内网暴露（见 README 安全约定）。
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Meta   json.RawMessage `json:"meta"`
		Events []*model.Event  `json:"events"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&payload); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if payload.Events == nil {
		writeErr(w, http.StatusBadRequest, "缺少 events 数组（应上传 /api/v1/export 的原样输出）")
		return
	}

	known := s.sources()
	loc := model.LocalLocation()
	var (
		total, changed int
		skipped        []map[string]string
	)
	for _, ev := range payload.Events {
		if ev == nil {
			continue
		}
		total++
		if ev.Source == "" || ev.ExternalID == "" {
			skipped = append(skipped, skipReason(ev, "缺少 source 或 external_id"))
			continue
		}
		if _, ok := known[ev.Source]; !ok && ev.Source != "ingest" {
			skipped = append(skipped, skipReason(ev, "未知数据源 "+ev.Source))
			continue
		}
		if !normalizeImported(ev, loc) {
			skipped = append(skipped, skipReason(ev, "无法确定日期（local_date 与 occurred_at 均缺失）"))
			continue
		}
		if ev.EventType == "" {
			ev.EventType = "note"
		}
		if ev.TimePrecision == "" {
			ev.TimePrecision = model.PrecisionSecond
		}
		isNew, err := s.store.UpsertEvent(ev)
		if err != nil {
			skipped = append(skipped, skipReason(ev, "写入失败: "+err.Error()))
			continue
		}
		if isNew {
			changed++
		}
	}
	if skipped == nil {
		skipped = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
		"total": total, "changed": changed, "skipped": skipped,
	}})
}

// normalizeImported 补齐导出时可能有、手写时可能缺的时间字段。
// 返回 false 表示这条事件的日期完全无法确定。
func normalizeImported(ev *model.Event, loc *time.Location) bool {
	if ev.LocalDate == "" && ev.OccurredAt > 0 {
		ev.LocalDate = time.Unix(ev.OccurredAt, 0).In(loc).Format("2006-01-02")
	}
	if ev.OccurredAt == 0 && ev.LocalDate != "" {
		if day, err := time.ParseInLocation("2006-01-02", ev.LocalDate, loc); err == nil {
			noon := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc)
			ev.OccurredAt = noon.UTC().Unix()
			ev.TimePrecision = model.PrecisionDay
		}
	}
	return ev.LocalDate != "" && ev.OccurredAt > 0
}

func skipReason(ev *model.Event, reason string) map[string]string {
	return map[string]string{
		"source": ev.Source, "external_id": ev.ExternalID, "reason": reason,
	}
}
