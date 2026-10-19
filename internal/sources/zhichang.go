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
	"sync"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// zhichangOverlapDays 每次增量同步回退的天数（手表数据常有延迟）。
const zhichangOverlapDays = 3

// ZhichangSource 知常（zhichang）健康数据适配器。
//
// 上游接口（2026-10-16 读源码 + 在线实测确认，internal/api/dashboard.go:33-53）：
//
//	GET {base}/api/daily?metrics=a,b&from=YYYY-MM-DD&to=YYYY-MM-DD
//	  → {series:{<metric>:[{date,value,sample_count,source_count,sources}]}}
//	  （服务端已按日聚合，直接消费终值）
//	GET {base}/api/metrics → {metrics:[{metric,display_name_zh,preferred_unit}]}
//	  （series 无 unit/中文名，从此字典补）
//
// 读取端零鉴权（写接口才要 ZHICHANG_SETTINGS_TOKEN，观复只读）。
//
// 事件模型：**每天一条** event_type=health_day（time_precision=day），
// detail_json 内按指标存放 {value,unit,label,display}。日汇总而非每指标
// 一条事件——时间轴上每天一张健康卡片，信息密度合适。
//
// cursor：YYYY-MM-DD（已同步到的日期）；首轮回溯 LookbackDays（默认 30）。
type ZhichangSource struct {
	cfg config.SourceConfig
	loc *time.Location

	unitsMu    sync.Mutex
	unitsCache map[string]zhichangMeta // nil 表示尚未成功拉取（失败不缓存，下轮重试）
}

// zhichangMeta 指标字典条目（/api/metrics）。
type zhichangMeta struct {
	Label string `json:"-"` // display_name_zh
	Unit  string `json:"-"` // preferred_unit
}

func NewZhichangSource(cfg config.SourceConfig) *ZhichangSource {
	// 适配器自报家门：认证方式与 Key 请求头都靠它解析，
	// 不依赖调用方记得填 Name（测试与页面配置两条路径都会经过这里）。
	if cfg.Name == "" {
		cfg.Name = "zhichang"
	}
	return &ZhichangSource{cfg: cfg, loc: model.LocalLocation()}
}

func (z *ZhichangSource) Name() string { return "zhichang" }

func (z *ZhichangSource) Interval() time.Duration {
	if z.cfg.Interval > 0 {
		return z.cfg.Interval
	}
	return 6 * time.Hour
}

// defaultZhichangMetrics 默认关注的 8 个核心指标（可用 ZHICHANG_METRICS 覆盖，
// 逗号分隔；与 zhichang metric_defs 的 metric 名对齐）。
var defaultZhichangMetrics = []string{
	"step_count",             // 步数
	"active_energy",          // 活动能量
	"heart_rate",             // 心率
	"sleep_analysis",         // 睡眠时长
	"body_mass",              // 体重
	"body_fat_percentage",    // 体脂率
	"oxygen_saturation",      // 血氧
	"heart_rate_variability", // HRV
}

func (z *ZhichangSource) metrics() []string {
	if z.cfg.Metrics != "" {
		parts := strings.Split(z.cfg.Metrics, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return defaultZhichangMetrics
}

type zhichangDailyResp struct {
	From   string                          `json:"from"`
	To     string                          `json:"to"`
	Series map[string][]zhichangDailyPoint `json:"series"`
}

type zhichangDailyPoint struct {
	Date        string   `json:"date"`
	Metric      string   `json:"metric"`
	Value       float64  `json:"value"`
	SampleCount int      `json:"sample_count"`
	Sources     []string `json:"sources"`
}

// Sync 按范围拉取天级序列（一次请求覆盖整个待同步区间）。
func (z *ZhichangSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if z.cfg.BaseURL == "" {
		return "", 0, fmt.Errorf("zhichang 未配置：需要 ZHICHANG_BASE_URL")
	}

	loc := z.loc
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -z.cfg.LookbackDays)
	if cursor != "" {
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			// 重叠 3 天：Apple Watch 同步常有延迟（隔几天才补录），
			// upsert 幂等，重查无副作用。
			start = t.AddDate(0, 0, 1-zhichangOverlapDays)
		}
	}
	end := now
	if start.After(end) {
		return cursor, 0, nil
	}

	metrics := z.metrics()
	series, err := z.fetchDaily(ctx, metrics, start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return "", 0, err
	}

	// 单位/中文名字典（失败降级：用 metric 原名、无单位，不中断同步）。
	meta := z.units(ctx, metrics)

	// 按日期聚合：date → metric → point。
	byDate := make(map[string]map[string]zhichangDailyPoint)
	for metric, points := range series {
		for _, p := range points {
			if byDate[p.Date] == nil {
				byDate[p.Date] = make(map[string]zhichangDailyPoint)
			}
			byDate[p.Date][metric] = p
		}
	}

	total := 0
	for date, vals := range byDate {
		ev := z.mapDay(date, vals, meta, metrics, loc)
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
	}
	// cursor 直接推到 end：区间已整段查过（幂等 upsert），迟到的数据由
	// 下轮的重叠窗口覆盖；避免「无数据日」把 cursor 卡住反复重查。
	return end.Format("2006-01-02"), total, nil
}

func (z *ZhichangSource) fetchDaily(ctx context.Context, metrics []string, from, to string) (map[string][]zhichangDailyPoint, error) {
	q := url.Values{}
	q.Set("metrics", strings.Join(metrics, ","))
	q.Set("from", from)
	q.Set("to", to)
	reqURL := z.cfg.BaseURL + "/api/daily?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 zhichang 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zhichang 返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var dr zhichangDailyResp
	if err := json.Unmarshal(body, &dr); err != nil {
		return nil, fmt.Errorf("解析 zhichang 响应失败: %w", err)
	}
	return dr.Series, nil
}

// units 拉取指标字典并缓存（仅缓存成功结果；失败下轮重试）。
func (z *ZhichangSource) units(ctx context.Context, metrics []string) map[string]zhichangMeta {
	z.unitsMu.Lock()
	defer z.unitsMu.Unlock()
	if z.unitsCache != nil {
		return z.unitsCache
	}
	reqURL := z.cfg.BaseURL + "/api/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var mr struct {
		Metrics []struct {
			Metric        string `json:"metric"`
			DisplayNameZh string `json:"display_name_zh"`
			PreferredUnit string `json:"preferred_unit"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil
	}
	cache := make(map[string]zhichangMeta, len(mr.Metrics))
	for _, m := range mr.Metrics {
		cache[m.Metric] = zhichangMeta{Label: m.DisplayNameZh, Unit: m.PreferredUnit}
	}
	z.unitsCache = cache
	return cache
}

// mapDay 把某日全部指标映射为一条 health_day 事件。
// metrics 顺序即摘要展示顺序（配置顺序，非 map 随机序）。
func (z *ZhichangSource) mapDay(date string, vals map[string]zhichangDailyPoint, meta map[string]zhichangMeta, metrics []string, loc *time.Location) *model.Event {
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		day = time.Now().In(loc)
	}
	// 全天事件：当天中午 12:00（与 model.NewDayEvent 一致，保证排序可预期）。
	noon := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc)

	detail := make(map[string]any, len(vals))
	summaryParts := make([]string, 0, len(vals))
	for _, metric := range metrics {
		p, ok := vals[metric]
		if !ok {
			continue
		}
		m := meta[metric]
		label := m.Label
		if label == "" {
			label = metric
		}
		detail[metric] = map[string]any{
			"value":   p.Value,
			"unit":    m.Unit,
			"label":   label,
			"display": formatMetricValue(p.Value, m.Unit),
		}
		summaryParts = append(summaryParts, fmt.Sprintf("%s %s", label, formatMetricValue(p.Value, m.Unit)))
	}
	if len(summaryParts) > 4 {
		summaryParts = summaryParts[:4] // 摘要最多 4 项，避免卡片过长
	}

	return &model.Event{
		Source:        z.Name(),
		ExternalID:    date, // 一天一条，日期即幂等键
		EventType:     model.EventHealthDay,
		TimePrecision: model.PrecisionDay,
		OccurredAt:    noon.UTC().Unix(),
		LocalDate:     date,
		Title:         "健康日报",
		Summary:       strings.Join(summaryParts, " · "),
		Detail:        detail,
		SourceURL:     z.cfg.BaseURL + "/#/day/" + date,
	}
}

// formatMetricValue 按单位格式化指标值（前端直接展示）。
// 一律先按常规四舍五入（half-up）归到目标精度再格式化——%.1f 的银行家
// 舍入会让 7.25 显示成 7.2，与用户预期不符。
func formatMetricValue(v float64, unit string) string {
	switch unit {
	case "count", "steps":
		return formatThousands(v)
	case "hr":
		return fmt.Sprintf("%.1fh", roundHalfUp(v, 1))
	case "min":
		return fmt.Sprintf("%.0fmin", roundHalfUp(v, 0))
	case "bpm":
		return fmt.Sprintf("%.0f", roundHalfUp(v, 0))
	case "kg":
		return fmt.Sprintf("%.1fkg", roundHalfUp(v, 1))
	case "%":
		return fmt.Sprintf("%.0f%%", roundHalfUp(v, 0))
	case "kcal", "kJ":
		return fmt.Sprintf("%.0f%s", roundHalfUp(v, 0), unit)
	case "ms":
		return fmt.Sprintf("%.0fms", roundHalfUp(v, 0))
	case "":
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprintf("%.1f%s", roundHalfUp(v, 1), unit)
	}
}

// roundHalfUp 常规四舍五入到 digits 位小数。
func roundHalfUp(v float64, digits int) float64 {
	pow := 1.0
	for i := 0; i < digits; i++ {
		pow *= 10
	}
	if v >= 0 {
		return float64(int64(v*pow+0.5)) / pow
	}
	return float64(int64(v*pow-0.5)) / pow
}

func formatThousands(v float64) string {
	n := int64(v + 0.5)
	s := strconv.FormatInt(n, 10)
	// 手工加千分位（避免 strconv 对负号/大数的边角问题）。
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}

// 编译期断言：ZhichangSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*ZhichangSource)(nil)
