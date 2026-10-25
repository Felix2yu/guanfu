package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// dawarichOverlapDays 每次增量同步回退的天数（suggested→confirmed 状态变化迟到）。
// 与其余日期游标源统一为 3 天。
const dawarichOverlapDays = 3

// DawarichSource Dawarich（到访地点）适配器。
//
// 上游接口（2026-10-15 在线实测确认，Rails + PostGIS）：
//
//	GET {base}/api/v1/visits?start_at&end_at&page&per_page(≤500)
//	  Header: Authorization: Bearer <key>（全权凭证，观复只发 GET）
//	  Resp:   [{id,name,status,confidence,started_at,ended_at,duration,
//	           place:{latitude,longitude,id}}]
//	  start_at/end_at：ISO8601 带时区（用户 Asia/Shanghai），必填
//	  duration：**分钟**（timeline 的 journey duration 才是秒，别混）
//	  status：confirmed / suggested —— 只收 confirmed
//
// 坐标：设备原生 GPS 上报，WGS-84（源码无转换逻辑 + 样本比对证实），
// 观复 normalize 不做转换。
//
// cursor：YYYY-MM-DD（已同步到的日期）；首轮回溯 LookbackDays（默认 30）。
type DawarichSource struct {
	cfg config.SourceConfig
	loc *time.Location
}

func NewDawarichSource(cfg config.SourceConfig) *DawarichSource {
	// 适配器自报家门：认证方式与 Key 请求头都靠它解析，
	// 不依赖调用方记得填 Name（测试与页面配置两条路径都会经过这里）。
	if cfg.Name == "" {
		cfg.Name = "dawarich"
	}
	return &DawarichSource{cfg: cfg, loc: model.LocalLocation()}
}

func (d *DawarichSource) Name() string { return "dawarich" }

func (d *DawarichSource) Interval() time.Duration {
	if d.cfg.Interval > 0 {
		return d.cfg.Interval
	}
	return 30 * time.Minute // 位置数据时效性要求高
}

type dawarichVisit struct {
	ID         flexID  `json:"id"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Confidence float64 `json:"confidence"`
	StartedAt  string  `json:"started_at"` // ISO8601 带时区
	EndedAt    string  `json:"ended_at"`
	Duration   float64 `json:"duration"` // 分钟
	Place      *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"place"`
}

// Sync 按天增量拉取 visits。
func (d *DawarichSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if d.cfg.BaseURL == "" || d.cfg.APIKey == "" {
		return "", 0, fmt.Errorf("dawarich 未配置：需要 DAWARICH_BASE_URL 与 DAWARICH_API_KEY")
	}

	loc := d.loc
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -d.cfg.LookbackDays)
	if cursor != "" {
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			// 重叠 2 天：Dawarich 的 visit 可能先从 suggested 变 confirmed
			//（状态变化会迟到），upsert 幂等，重查无副作用。
			start = t.AddDate(0, 0, 1-dawarichOverlapDays)
		}
	}
	end := now
	if start.After(end) {
		return cursor, 0, nil
	}

	var (
		newCursor = cursor
		total     int
	)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		n, err := d.syncDay(ctx, day, upsert)
		if err != nil {
			return "", total, err
		}
		total += n
		newCursor = day.Format("2006-01-02")
	}
	return newCursor, total, nil
}

func (d *DawarichSource) syncDay(ctx context.Context, day time.Time, upsert func(*model.Event) error) (int, error) {
	// 当天 [00:00, 次日00:00) 本地时区；Dawarich 的 end_at 行为按天查实测可用。
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, d.loc)
	dayEnd := dayStart.AddDate(0, 0, 1)

	const perPage = 500 // 上游上限
	n := 0
	for page := 1; ; page++ {
		visits, err := d.fetchVisits(ctx, dayStart, dayEnd, page, perPage)
		if err != nil {
			return n, err
		}
		for i := range visits {
			v := &visits[i]
			if v.Status != "confirmed" {
				continue // suggested 是服务端推测，不进时间轴
			}
			ev, err := d.mapVisit(v)
			if err != nil {
				return n, fmt.Errorf("映射 visit #%s 失败: %w", v.ID, err)
			}
			if err := upsert(ev); err != nil {
				return n, err
			}
			n++
		}
		if len(visits) < perPage {
			break
		}
		if page > 20 {
			// 防御上限（单日 1 万条 visits）。触顶说明当天还有未拉取的数据，
			// 报错让游标停在原地——静默跳过会永久丢失。
			return n, fmt.Errorf(
				"dawarich 单日翻页达 20 页防御上限（%s），可能有未拉取的 visits；中止本轮且不推进游标",
				day.Format("2006-01-02"))
		}
	}
	return n, nil
}

func (d *DawarichSource) fetchVisits(ctx context.Context, start, end time.Time, page, perPage int) ([]dawarichVisit, error) {
	q := url.Values{}
	q.Set("start_at", start.Format(time.RFC3339))
	q.Set("end_at", end.Format(time.RFC3339))
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(perPage))
	reqURL := d.cfg.BaseURL + "/api/v1/visits?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if err := d.cfg.ApplyAuth(req); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 dawarich 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dawarich 返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var visits []dawarichVisit
	if err := json.Unmarshal(body, &visits); err != nil {
		return nil, fmt.Errorf("解析 dawarich 响应失败: %w", err)
	}
	return visits, nil
}

// placeholderNames Dawarich 未确认地点的占位名（用户在网页确认前显示这些）。
var placeholderNames = []string{"Suggested place", "Unknown Location", ""}

func isPlaceholderName(name string) bool {
	for _, p := range placeholderNames {
		if strings.EqualFold(strings.TrimSpace(name), p) {
			return true
		}
	}
	return false
}

// mapVisit 映射到访事件。duration 为分钟，detail 同时存分钟与秒（前端展示用）。
func (d *DawarichSource) mapVisit(v *dawarichVisit) (*model.Event, error) {
	started, err := time.Parse(time.RFC3339, v.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("started_at 解析失败: %q", v.StartedAt)
	}
	started = started.In(d.loc)

	title := strings.TrimSpace(v.Name)
	if isPlaceholderName(title) {
		// 未确认地点：坐标兜底（前端地图点仍可用）。
		if v.Place != nil {
			title = fmt.Sprintf("地点 %.4f,%.4f", v.Place.Latitude, v.Place.Longitude)
		} else {
			title = "到访地点"
		}
	}

	ev := &model.Event{
		Source:        d.Name(),
		ExternalID:    v.ID.String(),
		EventType:     model.EventLocationVisit,
		TimePrecision: model.PrecisionSecond,
		OccurredAt:    started.UTC().Unix(),
		LocalDate:     started.Format("2006-01-02"),
		Title:         title,
		Detail: map[string]any{
			"duration_minutes": v.Duration,
			"confidence":       v.Confidence,
		},
	}
	if !isPlaceholderName(strings.TrimSpace(v.Name)) {
		ev.LocationName = strings.TrimSpace(v.Name)
	}
	// 摘要：停留时长（小时级取 1 位小数）。
	ev.Summary = formatDuration(v.Duration)
	// 坐标：WGS-84 原生，直接用。
	if v.Place != nil {
		ev.Latitude = v.Place.Latitude
		ev.Longitude = v.Place.Longitude
	}
	return ev, nil
}

// formatDuration 分钟数 → 人类可读时长（「2 小时 5 分」/「45 分钟」）。
func formatDuration(minutes float64) string {
	total := int(minutes + 0.5)
	if total < 60 {
		return fmt.Sprintf("停留 %d 分钟", total)
	}
	h := total / 60
	m := total % 60
	if m == 0 {
		return fmt.Sprintf("停留 %d 小时", h)
	}
	return fmt.Sprintf("停留 %d 小时 %d 分", h, m)
}

// 编译期断言：DawarichSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*DawarichSource)(nil)
