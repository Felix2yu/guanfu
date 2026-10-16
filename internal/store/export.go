package store

import (
	"encoding/json"
	"strings"

	"github.com/Felix2yu/guanfu/internal/model"
)

// ExportEvents 按 [from, to] 闭区间（local_date 口径）**流式**遍历事件。
//
// 为什么不复用 ListEvents：那个端点为时间轴服务，limit 封顶 1000，
// 导出要的是"全量、一条不落"，用分页拼既慢又容易在边界漏条。这里直接吃
// sql.Rows 游标，逐条回调，内存与库大小无关。
//
// fn 返回错误会中断遍历并把错误原样抛出（让调用方决定怎么收尾）。
func (s *Store) ExportEvents(from, to string, fn func(*model.Event) error) error {
	var (
		conds []string
		args  []any
	)
	if from != "" {
		conds = append(conds, "local_date >= ?")
		args = append(args, from)
	}
	if to != "" {
		conds = append(conds, "local_date <= ?")
		args = append(args, to)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	rows, err := s.DB.Query(`SELECT id, source, external_id, event_type, time_precision,
			occurred_at, local_date, title, summary, amount_cents, currency, latitude, longitude,
			location_name, media_json, detail_json, source_url, created_at, updated_at
		FROM events `+where+` ORDER BY local_date ASC, occurred_at ASC, id ASC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

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
			return err
		}
		if media != "" {
			_ = json.Unmarshal([]byte(media), &e.Media)
		}
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		e.FillLocalTime()
		if err := fn(&e); err != nil {
			return err
		}
	}
	return rows.Err()
}
