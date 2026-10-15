package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/Felix2yu/guanfu/internal/model"
)

// UpsertEvent 幂等写入一条事件。
// 冲突键 (source, external_id) 已存在时更新业务字段（源侧可能被修订），
// 但保留 created_at。返回是否有实际变化（用于统计「本次同步新增/更新数」）。
func (s *Store) UpsertEvent(e *model.Event) (changed bool, err error) {
	now := time.Now().Unix()
	media := ""
	if len(e.Media) > 0 {
		if b, err := json.Marshal(e.Media); err == nil {
			media = string(b)
		}
	}
	detail := ""
	if e.Detail != nil {
		if b, err := json.Marshal(e.Detail); err == nil {
			detail = string(b)
		}
	}
	e.CreatedAt, e.UpdatedAt = now, now

	// upsert + 变化检测：用 changes() 判断是否真正写入了不同内容。
	res, err := s.DB.Exec(`INSERT INTO events
		(source, external_id, event_type, time_precision, occurred_at, local_date,
		 title, summary, amount_cents, currency, latitude, longitude, location_name,
		 media_json, detail_json, source_url, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(source, external_id) DO UPDATE SET
		 event_type=excluded.event_type,
		 time_precision=excluded.time_precision,
		 occurred_at=excluded.occurred_at,
		 local_date=excluded.local_date,
		 title=excluded.title,
		 summary=excluded.summary,
		 amount_cents=excluded.amount_cents,
		 currency=excluded.currency,
		 latitude=excluded.latitude,
		 longitude=excluded.longitude,
		 location_name=excluded.location_name,
		 media_json=excluded.media_json,
		 detail_json=excluded.detail_json,
		 source_url=excluded.source_url,
		 updated_at=excluded.updated_at`,
		e.Source, e.ExternalID, e.EventType, e.TimePrecision, e.OccurredAt, e.LocalDate,
		e.Title, e.Summary, e.AmountCents, e.Currency, e.Latitude, e.Longitude, e.LocationName,
		media, detail, e.SourceURL, e.CreatedAt, e.UpdatedAt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// EventQuery 事件列表查询条件。
type EventQuery struct {
	Date      string // local_date 精确匹配（时间轴主场景）
	From      string // local_date >= From
	To        string // local_date <= To
	Source    string
	EventType string
	Limit     int
	Offset    int
}

// ListEvents 按条件查询事件，按 occurred_at 正序（当天内按时刻排列）。
func (s *Store) ListEvents(q EventQuery) ([]*model.Event, error) {
	var (
		conds []string
		args  []any
	)
	if q.Date != "" {
		conds = append(conds, "local_date = ?")
		args = append(args, q.Date)
	}
	if q.From != "" {
		conds = append(conds, "local_date >= ?")
		args = append(args, q.From)
	}
	if q.To != "" {
		conds = append(conds, "local_date <= ?")
		args = append(args, q.To)
	}
	if q.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, q.Source)
	}
	if q.EventType != "" {
		conds = append(conds, "event_type = ?")
		args = append(args, q.EventType)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.DB.Query(`SELECT id, source, external_id, event_type, time_precision,
			occurred_at, local_date, title, summary, amount_cents, currency, latitude, longitude,
			location_name, media_json, detail_json, source_url, created_at, updated_at
		FROM events `+where+` ORDER BY occurred_at ASC, id ASC LIMIT ? OFFSET ?`,
		append(args, limit, q.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Event
	// 保证空结果序列化为 [] 而非 null（前端直接 .filter/.map，不判空）。
	out = make([]*model.Event, 0, 16)
	for rows.Next() {
		var (
			e      model.Event
			media  string
			detail string
		)
		if err := rows.Scan(&e.ID, &e.Source, &e.ExternalID, &e.EventType, &e.TimePrecision,
			&e.OccurredAt, &e.LocalDate, &e.Title, &e.Summary, &e.AmountCents, &e.Currency,
			&e.Latitude, &e.Longitude, &e.LocationName, &media, &detail, &e.SourceURL,
			&e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		if media != "" {
			_ = json.Unmarshal([]byte(media), &e.Media)
		}
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// CountByDate 返回某日各源事件数（前端日期热力图/摘要用）。
func (s *Store) CountByDate(date string) (map[string]int, error) {
	rows, err := s.DB.Query(`SELECT source, COUNT(*) FROM events WHERE local_date = ? GROUP BY source`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var src string
		var n int
		if err := rows.Scan(&src, &n); err != nil {
			return nil, err
		}
		out[src] = n
	}
	return out, rows.Err()
}

// DateRange 返回库中最早与最晚 local_date（日历组件边界）。
func (s *Store) DateRange() (min, max string, err error) {
	row := s.DB.QueryRow(`SELECT COALESCE(MIN(local_date),''), COALESCE(MAX(local_date),'') FROM events`)
	err = row.Scan(&min, &max)
	return
}

// GetSourceCursor 读取某源的同步游标（增量位点）。
func (s *Store) GetSourceCursor(name string) (string, error) {
	var cur string
	err := s.DB.QueryRow(`SELECT cursor FROM sources WHERE name = ?`, name).Scan(&cur)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return cur, err
}

// SaveSourceState 持久化某源的同步状态（游标/时间/错误/条数）。
func (s *Store) SaveSourceState(name string, enabled bool, intervalSecs int, cursor string, lastSync int64, lastErr string, count int) error {
	_, err := s.DB.Exec(`INSERT INTO sources (name, enabled, interval_secs, cursor, last_sync_at, last_error, last_count, updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET
		 enabled=excluded.enabled,
		 interval_secs=excluded.interval_secs,
		 cursor=excluded.cursor,
		 last_sync_at=excluded.last_sync_at,
		 last_error=excluded.last_error,
		 last_count=excluded.last_count,
		 updated_at=excluded.updated_at`,
		name, boolToInt(enabled), intervalSecs, cursor, lastSync, lastErr, count, time.Now().Unix())
	return err
}

// SourceState 源同步状态（API 展示用）。
type SourceState struct {
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	IntervalSecs int    `json:"interval_secs"`
	Cursor       string `json:"cursor"`
	LastSyncAt   int64  `json:"last_sync_at"`
	LastError    string `json:"last_error"`
	LastCount    int    `json:"last_count"`
}

// GetSourceState 读取单个源的同步状态（不存在返回 sql.ErrNoRows）。
func (s *Store) GetSourceState(name string) (SourceState, error) {
	var st SourceState
	var enabled int
	err := s.DB.QueryRow(`SELECT name, enabled, interval_secs, cursor, last_sync_at, last_error, last_count FROM sources WHERE name = ?`, name).
		Scan(&st.Name, &enabled, &st.IntervalSecs, &st.Cursor, &st.LastSyncAt, &st.LastError, &st.LastCount)
	st.Enabled = enabled != 0
	return st, err
}

// ListSourceStates 列出所有源的同步状态。
func (s *Store) ListSourceStates() ([]SourceState, error) {	rows, err := s.DB.Query(`SELECT name, enabled, interval_secs, cursor, last_sync_at, last_error, last_count FROM sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SourceState, 0, 8)
	for rows.Next() {
		var st SourceState
		var enabled int
		if err := rows.Scan(&st.Name, &enabled, &st.IntervalSecs, &st.Cursor, &st.LastSyncAt, &st.LastError, &st.LastCount); err != nil {
			return nil, err
		}
		st.Enabled = enabled != 0
		out = append(out, st)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
