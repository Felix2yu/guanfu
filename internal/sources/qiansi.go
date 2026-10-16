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

// qiansiOverlapDays 每次增量同步回退的天数（人情往来常事后补录，且
// 事件可被编辑；upsert 幂等，重查无副作用）。
const qiansiOverlapDays = 7

// qiansiPageSize 单页条数。上游 limit 无上限校验、store 在 limit<=0 时
// 兜底 100/200，这里显式传 200 并自行翻页，避免"传 0 拿 200"的隐式行为。
const qiansiPageSize = 200

// qiansiMaxPages 翻页上限（防御上游不推进 offset 时的死循环）。
const qiansiMaxPages = 50

// QiansiSource 牵丝（qiansi）人情往来适配器。
//
// 上游接口（2026-10-16 读源码确认，internal/api/records.go）：
//
//	GET {base}/api/v1/events/?from=YYYY-MM-DD&to=YYYY-MM-DD&limit&offset
//	  → 裸 JSON 数组 [{id,title,type_name,event_date,location,locations[],
//	    has_gift,gift(gift_amount_fen/gift_direction/gift_person_id),
//	    summary,participants[]{id,name},expense_fen,updated_at}]
//	GET {base}/api/v1/transactions/?from&to&kind&limit&offset
//	  → 裸数组 [{id,kind,direction(in|out),amount_fen,title,occurred_at,
//	    settled,person_name}]
//	GET {base}/api/v1/anniversaries/            （无任何参数、无分页）
//	  → 裸数组 [{id,title,date,is_lunar,repeat_yearly,person_name,
//	    next_date,days_until}]
//
// 三个端点都返回**裸数组**（无 {data:...} 包装）；起止参数名是 `from`/`to`
// 且强校验 `YYYY-MM-DD`（RFC3339 会 400）。鉴权：QIANSI_TOKEN 未配置时
// 服务端整体放行；配置后需 `Authorization: Bearer <token>`（无 X-API-Key）。
//
// 事件模型（幂等键加表前缀，避免跨表 ID 撞车）：
//   - social_event  `ev:<id>`      往来事件（可带礼金/开销金额）
//   - gift_money    `tx:<id>`      礼金收付（只收 kind=gift，借贷/开销与
//     货殖记账语义重叠，不重复入轴）
//   - anniversary   `ann:<id>:<next_date>`  按 next_date 投放；每年循环项
//     在服务端算好下一次日期，过期的那条自然留在去年那天成为历史
//
// cursor：YYYY-MM-DD；首轮回溯 LookbackDays（默认 180）。
type QiansiSource struct {
	cfg config.SourceConfig
	loc *time.Location
}

func NewQiansiSource(cfg config.SourceConfig) *QiansiSource {
	return &QiansiSource{cfg: cfg, loc: model.LocalLocation()}
}

func (q *QiansiSource) Name() string { return "qiansi" }

func (q *QiansiSource) Interval() time.Duration {
	if q.cfg.Interval > 0 {
		return q.cfg.Interval
	}
	return time.Hour
}

// qiansiEvent 往来事件（/api/v1/events/）。
type qiansiEvent struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	TypeName      *string  `json:"type_name"`
	EventDate     string   `json:"event_date"`
	Location      string   `json:"location"`
	Locations     []string `json:"locations"`
	HasGift       bool     `json:"has_gift"`
	Gift          string   `json:"gift"`
	Summary       string   `json:"summary"`
	ExpenseFen    int      `json:"expense_fen"`
	GiftAmountFen int      `json:"gift_amount_fen"`
	GiftDirection string   `json:"gift_direction"`
	UpdatedAt     string   `json:"updated_at"`
	Participants  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"participants"`
}

// qiansiTx 交易（/api/v1/transactions/）。金额单位分（非 NULL，>0）。
type qiansiTx struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Direction  string `json:"direction"`
	AmountFen  int    `json:"amount_fen"`
	Title      string `json:"title"`
	OccurredAt string `json:"occurred_at"`
	Settled    bool   `json:"settled"`
	PersonName string `json:"person_name"`
}

// qiansiAnniversary 纪念日（/api/v1/anniversaries/）。
type qiansiAnniversary struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Date         string `json:"date"`
	IsLunar      bool   `json:"is_lunar"`
	RepeatYearly bool   `json:"repeat_yearly"`
	PersonName   string `json:"person_name"`
	NextDate     string `json:"next_date"`
	DaysUntil    *int   `json:"days_until"`
}

// Sync 拉取区间内的往来事件与礼金，并全量对账纪念日。
func (q *QiansiSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if q.cfg.BaseURL == "" {
		return "", 0, fmt.Errorf("qiansi 未配置：需要 QIANSI_BASE_URL")
	}

	loc := q.loc
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -q.cfg.LookbackDays)
	if cursor != "" {
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			start = t.AddDate(0, 0, 1-qiansiOverlapDays)
		}
	}
	end := now
	if start.After(end) {
		return cursor, 0, nil
	}
	from, to := start.Format("2006-01-02"), end.Format("2006-01-02")

	var total int
	events, err := q.fetchEvents(ctx, from, to)
	if err != nil {
		return "", total, err
	}
	for i := range events {
		ev := q.mapEvent(&events[i])
		if ev == nil {
			continue
		}
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
	}

	gifts, err := q.fetchGifts(ctx, from, to)
	if err != nil {
		return "", total, err
	}
	for i := range gifts {
		ev := q.mapGift(&gifts[i])
		if ev == nil {
			continue
		}
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
	}

	// 纪念日无日期参数，每次全量对账（条目量小）；按 next_date 投放。
	anns, err := q.fetchAnniversaries(ctx)
	if err != nil {
		return "", total, err
	}
	for i := range anns {
		ev := q.mapAnniversary(&anns[i])
		if ev == nil {
			continue
		}
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
	}

	// 区间已整段查过（幂等），cursor 直接推到 end；迟到补录由重叠窗口覆盖。
	return to, total, nil
}

// get 带鉴权 GET 并把裸数组解码到 dst。
func (q *QiansiSource) get(ctx context.Context, reqURL string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if q.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+q.cfg.APIKey)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 qiansi 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("qiansi 鉴权失败（HTTP 401）：QIANSI_TOKEN 与服务端不一致")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qiansi 返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("解析 qiansi 响应失败: %w", err)
	}
	return nil
}

// fetchEvents 区间往来事件（自动翻页）。
func (q *QiansiSource) fetchEvents(ctx context.Context, from, to string) ([]qiansiEvent, error) {
	var all []qiansiEvent
	for page := 0; page < qiansiMaxPages; page++ {
		v := url.Values{}
		v.Set("from", from)
		v.Set("to", to)
		v.Set("limit", strconv.Itoa(qiansiPageSize))
		v.Set("offset", strconv.Itoa(page*qiansiPageSize))
		var batch []qiansiEvent
		if err := q.get(ctx, q.cfg.BaseURL+"/api/v1/events/?"+v.Encode(), &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < qiansiPageSize {
			break
		}
	}
	return all, nil
}

// fetchGifts 区间礼金（kind=gift，自动翻页）。
func (q *QiansiSource) fetchGifts(ctx context.Context, from, to string) ([]qiansiTx, error) {
	var all []qiansiTx
	for page := 0; page < qiansiMaxPages; page++ {
		v := url.Values{}
		v.Set("from", from)
		v.Set("to", to)
		v.Set("kind", "gift")
		v.Set("limit", strconv.Itoa(qiansiPageSize))
		v.Set("offset", strconv.Itoa(page*qiansiPageSize))
		var batch []qiansiTx
		if err := q.get(ctx, q.cfg.BaseURL+"/api/v1/transactions/?"+v.Encode(), &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < qiansiPageSize {
			break
		}
	}
	return all, nil
}

// fetchAnniversaries 全量纪念日。
func (q *QiansiSource) fetchAnniversaries(ctx context.Context) ([]qiansiAnniversary, error) {
	var all []qiansiAnniversary
	if err := q.get(ctx, q.cfg.BaseURL+"/api/v1/anniversaries/", &all); err != nil {
		return nil, err
	}
	return all, nil
}

// mapEvent 往来事件 → social_event。返回 nil 表示日期非法（跳过）。
func (q *QiansiSource) mapEvent(e *qiansiEvent) *model.Event {
	day := dayOnly(e.EventDate)
	if day == "" {
		return nil
	}
	ev := model.NewDayEvent(q.Name(), "ev:"+e.ID, model.EventSocialEvent, day)
	ev.Title = firstNonEmpty(e.Title, "人情往来")
	ev.SourceURL = q.cfg.BaseURL + "/events"

	detail := map[string]any{}
	if e.TypeName != nil && *e.TypeName != "" {
		detail["type_name"] = *e.TypeName
	}
	if len(e.Participants) > 0 {
		names := make([]string, 0, len(e.Participants))
		for _, p := range e.Participants {
			if p.Name != "" {
				names = append(names, p.Name)
			}
		}
		if len(names) > 0 {
			detail["participants"] = names
		}
	}
	// 地点：locations 是结构化数组，location 是服务端已拼好的兼容串（两者
	// 内容重叠，优先数组，避免出现「上海 · 静安 · 上海 · 静安」这样的重复）。
	locs := joinNonEmpty(e.Locations, " · ")
	if locs == "" {
		locs = strings.TrimSpace(e.Location)
	}
	if locs != "" {
		detail["locations"] = locs
		ev.LocationName = locs
	}
	if e.Summary != "" {
		detail["note"] = truncate(e.Summary, 2000)
	}
	if e.HasGift || e.GiftAmountFen > 0 {
		detail["has_gift"] = true
	}
	// 金额取"礼金优先、其次关联开销"。礼金方向 in=收（绿），out=付（红）。
	summary := make([]string, 0, 4)
	if e.GiftAmountFen > 0 {
		ev.AmountCents = int64(e.GiftAmountFen)
		ev.Currency = "CNY"
		income := e.GiftDirection == "in"
		detail["gift_amount_fen"] = e.GiftAmountFen
		detail["gift_direction"] = e.GiftDirection
		if income {
			detail["type"] = "income"
		}
		summary = append(summary, fmt.Sprintf("礼金 %s¥%s", giftSign(income), fmtYuan(e.GiftAmountFen)))
	} else if e.ExpenseFen > 0 {
		ev.AmountCents = int64(e.ExpenseFen)
		ev.Currency = "CNY"
		detail["expense_fen"] = e.ExpenseFen
		summary = append(summary, "开销 ¥"+fmtYuan(e.ExpenseFen))
	}
	if e.TypeName != nil && *e.TypeName != "" {
		summary = append([]string{*e.TypeName}, summary...)
	}
	if names, ok := detail["participants"].([]string); ok && len(names) > 0 {
		summary = append(summary, truncate(strings.Join(names, "、"), 40))
	}
	if e.Gift != "" {
		summary = append(summary, truncate(e.Gift, 40))
	}
	ev.Detail = detail
	ev.Summary = joinNonEmpty(summary, " · ")
	return ev
}

// mapGift 礼金交易 → gift_money。
func (q *QiansiSource) mapGift(t *qiansiTx) *model.Event {
	day := dayOnly(t.OccurredAt)
	if day == "" || t.AmountFen <= 0 {
		return nil
	}
	ev := model.NewDayEvent(q.Name(), "tx:"+t.ID, model.EventGiftMoney, day)
	income := t.Direction == "in"
	ev.Title = firstNonEmpty(t.Title, "礼金")
	if t.PersonName != "" {
		ev.Title = t.PersonName + " · " + ev.Title
	}
	ev.AmountCents = int64(t.AmountFen)
	ev.Currency = "CNY"
	ev.SourceURL = q.cfg.BaseURL + "/money"

	detail := map[string]any{
		"kind":      t.Kind,
		"direction": t.Direction,
		"settled":   t.Settled,
	}
	if income {
		detail["type"] = "income"
	}
	if t.PersonName != "" {
		detail["person_name"] = t.PersonName
	}
	ev.Detail = detail
	summary := []string{fmt.Sprintf("%s ¥%s", map[bool]string{true: "收礼", false: "送礼"}[income], fmtYuan(t.AmountFen))}
	if t.Settled {
		summary = append(summary, "已结清")
	}
	ev.Summary = joinNonEmpty(summary, " · ")
	return ev
}

// mapAnniversary 纪念日 → anniversary（按 next_date 投放；无下次则跳过）。
func (q *QiansiSource) mapAnniversary(a *qiansiAnniversary) *model.Event {
	day := dayOnly(a.NextDate)
	if day == "" {
		return nil
	}
	ev := model.NewDayEvent(q.Name(), "ann:"+a.ID+":"+day, model.EventAnniversary, day)
	ev.Title = titleCase(a.Title, "纪念日")
	if a.PersonName != "" {
		ev.Title = a.PersonName + " · " + ev.Title
	}
	ev.SourceURL = q.cfg.BaseURL + "/anniversaries"

	detail := map[string]any{
		"original_date": a.Date,
		"repeat_yearly": a.RepeatYearly,
		"is_lunar":      a.IsLunar,
	}
	if a.PersonName != "" {
		detail["person_name"] = a.PersonName
	}
	if a.DaysUntil != nil {
		detail["days_until"] = *a.DaysUntil
	}
	ev.Detail = detail

	summary := make([]string, 0, 3)
	switch {
	case a.IsLunar && a.RepeatYearly:
		summary = append(summary, "农历 · 每年")
	case a.IsLunar:
		summary = append(summary, "农历")
	case a.RepeatYearly:
		summary = append(summary, "每年")
	}
	if a.Date != "" && a.Date != day {
		summary = append(summary, "原日期 "+dayOnly(a.Date))
	}
	if a.DaysUntil != nil && *a.DaysUntil > 0 {
		summary = append(summary, fmt.Sprintf("还有 %d 天", *a.DaysUntil))
	} else if a.DaysUntil != nil && *a.DaysUntil == 0 {
		summary = append(summary, "就是今天")
	}
	ev.Summary = joinNonEmpty(summary, " · ")
	return ev
}

// dayOnly 从各种形态的时间串里取本地日期（YYYY-MM-DD）：日期串、"YYYY-MM-DD HH:MM:SS"、
// RFC3339 都支持；非法返回空串（调用方跳过该条）。
func dayOnly(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return ""
	}
	day := s[:10]
	if _, err := time.ParseInLocation("2006-01-02", day, model.LocalLocation()); err != nil {
		return ""
	}
	return day
}

// fmtYuan 分 → 元的展示串（保留 2 位，不带千分位，与货殖卡片口径一致）。
func fmtYuan(fen int) string {
	return strconv.FormatFloat(float64(fen)/100, 'f', 2, 64)
}

// giftSign 收礼 + / 送礼 −（与前端金额配色口径一致）。
func giftSign(income bool) string {
	if income {
		return "+"
	}
	return "−"
}

// titleCase 空标题兜底。
func titleCase(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// 编译期断言：QiansiSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*QiansiSource)(nil)
