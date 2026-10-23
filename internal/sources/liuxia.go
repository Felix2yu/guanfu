package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// liuxiaForecastDays 每轮多拉的未来天数（预报会随新数据修正，未来段每轮重拉）。
const liuxiaForecastDays = 3

// liuxiaOverlapDays 已过去日期的回退天数（数据可能补录/重算）。
const liuxiaOverlapDays = 2

// LiuxiaSource 流霞（liuxia）朝霞/晚霞预报适配器。
//
// 上游接口（2026-10-16 读源码确认，web.go:299-327 / store.go:19-30）：
//
//	GET {base}/api/export?format=json&start=YYYY-MM-DD&end=YYYY-MM-DD
//	  → **裸 JSON 数组**（无记录时是 `null`，需容错）：
//	    [{id,city,date(YYYY-MM-DD),time(HH:MM),event_type(morning|evening),
//	      model(GFS|EC),quality(float|null),aod(float|null),created_at,updated_at}]
//	参数：city / event_type / start / end（event_type 非法 400，日期只收
//	YYYY-MM-DD）。**全站零鉴权**（无任何中间件）。时间均为中国本地时间。
//
// 表唯一键是 (city,date,event_type,model)，一天最多 2 时段 × 2 模型 = 4 条。
//
// 事件模型：按 (城市, 日期) 聚合为一条 sky_forecast（全天），detail 保留
// 全部原始记录（含模型与 AOD），摘要给出各时段代表质量。
//
// cursor：YYYY-MM-DD，语义是"已同步到的**今天**"——未来预报段每轮重拉
// （cursor 不因未来日期推进），过去段用重叠窗口兜补录。
type LiuxiaSource struct {
	cfg config.SourceConfig
	loc *time.Location
}

func NewLiuxiaSource(cfg config.SourceConfig) *LiuxiaSource {
	// 适配器自报家门：认证方式与 Key 请求头都靠它解析，
	// 不依赖调用方记得填 Name（测试与页面配置两条路径都会经过这里）。
	if cfg.Name == "" {
		cfg.Name = "liuxia"
	}
	return &LiuxiaSource{cfg: cfg, loc: model.LocalLocation()}
}

func (l *LiuxiaSource) Name() string { return "liuxia" }

func (l *LiuxiaSource) Interval() time.Duration {
	if l.cfg.Interval > 0 {
		return l.cfg.Interval
	}
	return 6 * time.Hour
}

// liuxiaRecord 一条预报记录（与上游 SunsetRecord 对应；上游 id 不参与聚合键，不取）。
type liuxiaRecord struct {
	City      string   `json:"city"`
	Date      string   `json:"date"`
	Time      string   `json:"time"`
	EventType string   `json:"event_type"`
	Model     string   `json:"model"`
	Quality   *float64 `json:"quality"`
	AOD       *float64 `json:"aod"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// Sync 拉取 [起点, 今天+N] 的预报并按天聚合。
func (l *LiuxiaSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if l.cfg.BaseURL == "" {
		return "", 0, fmt.Errorf("liuxia 未配置：需要 LIUXIA_BASE_URL")
	}

	loc := l.loc
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	start := now.AddDate(0, 0, -l.cfg.LookbackDays)
	if cursor != "" {
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			start = t.AddDate(0, 0, -liuxiaOverlapDays)
		}
	}
	end := now.AddDate(0, 0, liuxiaForecastDays)
	if start.After(end) {
		return cursor, 0, nil
	}

	recs, err := l.fetch(ctx, start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return "", 0, err
	}

	// 按 (city, date) 分组，聚合为一天一条。
	type key struct{ city, day string }
	groups := map[key][]liuxiaRecord{}
	for _, r := range recs {
		day := dayOnly(r.Date)
		if day == "" {
			continue
		}
		k := key{city: firstNonEmpty(r.City, "默认"), day: day}
		groups[k] = append(groups[k], r)
	}

	total := 0
	for k, items := range groups {
		ev := l.mapDay(k.city, k.day, items)
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
	}

	// cursor 固定为今天：过去段靠重叠窗口兜补录，未来段（预报修正）每轮重拉。
	return today, total, nil
}

func (l *LiuxiaSource) fetch(ctx context.Context, start, end string) ([]liuxiaRecord, error) {
	v := url.Values{}
	v.Set("format", "json")
	v.Set("start", start)
	v.Set("end", end)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		l.cfg.BaseURL+"/api/export?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 liuxia 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("liuxia 返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	// 无记录时上游输出字面量 null，json.Unmarshal 到 slice 会保持 nil，不报错。
	var recs []liuxiaRecord
	if err := json.Unmarshal(body, &recs); err != nil {
		return nil, fmt.Errorf("解析 liuxia 响应失败: %w", err)
	}
	return recs, nil
}

// mapDay 把某城市某日的全部记录聚合为一条 sky_forecast 事件。
func (l *LiuxiaSource) mapDay(city, day string, items []liuxiaRecord) *model.Event {
	ev := model.NewDayEvent(l.Name(), "sky:"+city+":"+day, model.EventSkyForecast, day)
	ev.Title = skyTitle(items)
	ev.SourceURL = l.cfg.BaseURL + "/"

	// 记录按 时段(morning 先) → 模型 排序，输出稳定。
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].EventType != items[j].EventType {
			return items[i].EventType == "morning"
		}
		return items[i].Model < items[j].Model
	})

	records := make([]map[string]any, 0, len(items))
	best := map[string]liuxiaRecord{} // event_type → 质量最高的那条
	summaryParts := make([]string, 0, 2)
	for _, r := range items {
		rec := map[string]any{
			"event_type": r.EventType,
			"time":       r.Time,
			"model":      r.Model,
		}
		if r.Quality != nil {
			rec["quality"] = *r.Quality
		}
		if r.AOD != nil {
			rec["aod"] = *r.AOD
		}
		records = append(records, rec)

		cur, ok := best[r.EventType]
		if !ok || betterQuality(r, cur) {
			best[r.EventType] = r
		}
	}
	for _, slot := range []string{"morning", "evening"} {
		r, ok := best[slot]
		if !ok {
			continue
		}
		label := map[string]string{"morning": "朝霞", "evening": "晚霞"}[slot]
		part := label
		if r.Quality != nil {
			part += " " + formatQuality(*r.Quality)
		}
		if r.Model != "" {
			part += "（" + r.Model + "）"
		}
		if r.AOD != nil {
			part += " · AOD " + formatQuality(*r.AOD)
		}
		summaryParts = append(summaryParts, part)
	}
	if city != "" && city != "默认" {
		summaryParts = append(summaryParts, city)
	}

	ev.Detail = map[string]any{"records": records, "city": city}
	if minAOD := minAOD(items); minAOD != nil {
		ev.Detail["aod_min"] = *minAOD
	}
	ev.Summary = joinNonEmpty(summaryParts, " · ")
	return ev
}

// skyTitle 按当天包含的时段生成标题。
func skyTitle(items []liuxiaRecord) string {
	hasMorning, hasEvening := false, false
	for _, r := range items {
		switch r.EventType {
		case "morning":
			hasMorning = true
		case "evening":
			hasEvening = true
		}
	}
	switch {
	case hasMorning && hasEvening:
		return "朝霞/晚霞预报"
	case hasEvening:
		return "晚霞预报"
	case hasMorning:
		return "朝霞预报"
	default:
		return "霞光预报"
	}
}

// betterQuality a 的质量是否优于 b（nil 视为最差）。
func betterQuality(a, b liuxiaRecord) bool {
	if a.Quality == nil {
		return false
	}
	if b.Quality == nil {
		return true
	}
	return *a.Quality > *b.Quality
}

// minAOD 取当天最小 AOD（越小越通透）。
func minAOD(items []liuxiaRecord) *float64 {
	var min *float64
	for _, r := range items {
		if r.AOD == nil {
			continue
		}
		if min == nil || *r.AOD < *min {
			v := *r.AOD
			min = &v
		}
	}
	return min
}

// formatQuality 数值指标统一 2 位小数（half-up，避免 %.2f 的银行家舍入）。
func formatQuality(v float64) string {
	return strconv.FormatFloat(roundHalfUp(v, 2), 'f', 2, 64)
}

// 编译期断言：LiuxiaSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*LiuxiaSource)(nil)
