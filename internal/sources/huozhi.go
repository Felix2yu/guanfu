// Package sources 实现各数据源适配器。M1 只落地 huozhi（记账），
// 其余源按同样模式在 M2/M3 追加。
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// HuozhiSource 货殖（huozhi）记账数据适配器。
//
// 上游接口（2026-10-15 读源码确认）：
//
//	GET {base}/api/public/bills
//	  Header: X-API-Key: <key>
//	  Query:  page（默认1）, page_size（默认20，最大100）,
//	          start_date（YYYY-MM-DD，含当天00:00）,
//	          end_date（YYYY-MM-DD，服务端追加 23:59:59）
//	  Resp:   {code:0, data:{list:[…], pagination:{page,page_size,total}}}
//	  item:   {id, description, amount(元,float), currency, type, tx_date("2006-01-02 15:04:05"), merchant}
//	  type:   expense/income/transfer/refund/reimburse/adjust
//
// 幂等：external_id = strconv(id)；tx_date 为 huozhi 服务器本地时间字符串，
// 按本地时区（GUANFU_TZ）解析后转 UTC 存 occurred_at，并归日出 local_date。
type HuozhiSource struct {
	cfg config.SourceConfig
	loc *time.Location
}

// NewHuozhiSource 创建适配器。未配置 BaseURL/APIKey 时 Sync 返回明确错误，
// 引擎会把它记录到 sources.last_error，不影响其他源。
func NewHuozhiSource(cfg config.SourceConfig) *HuozhiSource {
	return &HuozhiSource{cfg: cfg, loc: model.LocalLocation()}
}

func (h *HuozhiSource) Name() string { return "huozhi" }

func (h *HuozhiSource) Interval() time.Duration {
	if h.cfg.Interval > 0 {
		return h.cfg.Interval
	}
	return 30 * time.Minute
}

// huozhiBill 上游精简账单字段（与 handlers.ListPublicBills 对齐）。
type huozhiBill struct {
	ID          uint    `json:"id"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"` // 元
	Currency    string  `json:"currency"`
	Type        string  `json:"type"`
	TxDate      string  `json:"tx_date"` // "2006-01-02 15:04:05"
	Merchant    string  `json:"merchant"`
}

type huozhiPagedResp struct {
	Code int `json:"code"`
	Data struct {
		List       []huozhiBill `json:"list"`
		Pagination struct {
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
			Total    int64 `json:"total"`
		} `json:"pagination"`
	} `json:"data"`
}

// Sync 增量拉取账单。cursor 为上次同步日期（YYYY-MM-DD）；
// 首轮无 cursor 时回溯 LookbackDays 天（默认 90），避免首次全量过大。
func (h *HuozhiSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if h.cfg.BaseURL == "" || h.cfg.APIKey == "" {
		return "", 0, fmt.Errorf("huozhi 未配置：需要 HUOZHI_BASE_URL 与 HUOZHI_API_KEY")
	}

	loc := h.loc
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -h.cfg.LookbackDays)
	if cursor != "" {
		// cursor 是上次成功同步的 end_date（含），次日开始即可。
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			start = t.AddDate(0, 0, 1)
		}
	}
	end := now
	if start.After(end) {
		return cursor, 0, nil // 游标已领先当天（时钟漂移），空转
	}

	var (
		newCursor = cursor
		total     int
	)
	const requestedPageSize = 100 // 上游上限 100
	for page := 1; ; page++ {
		bills, pageSize, pageTotal, err := h.fetchPage(ctx, start, end, page)
		if err != nil {
			return "", total, err
		}
		if pageSize <= 0 {
			pageSize = requestedPageSize
		}
		for i := range bills {
			ev, err := h.mapBill(&bills[i], loc)
			if err != nil {
				return "", total, fmt.Errorf("映射账单 #%d 失败: %w", bills[i].ID, err)
			}
			if err := upsert(ev); err != nil {
				return "", total, err
			}
			total++
		}
		// 翻页终止：不足一页即末页；或已翻页数 × 上游页大小覆盖 total。
		if len(bills) < pageSize || int64(page*pageSize) >= pageTotal {
			break
		}
		if page > 200 { // 防御性上限：200 页 ×100 = 2 万条/轮
			break
		}
	}
	newCursor = end.Format("2006-01-02")
	return newCursor, total, nil
}

func (h *HuozhiSource) fetchPage(ctx context.Context, start, end time.Time, page int) (bills []huozhiBill, pageSize int, pageTotal int64, err error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", "100") // 上游上限 100
	q.Set("start_date", start.Format("2006-01-02"))
	q.Set("end_date", end.Format("2006-01-02"))
	reqURL := h.cfg.BaseURL + "/api/public/bills?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("X-API-Key", h.cfg.APIKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("请求 huozhi 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, 0, 0, fmt.Errorf("huozhi 返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var pr huozhiPagedResp
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, 0, 0, fmt.Errorf("解析 huozhi 响应失败: %w", err)
	}
	if pr.Code != 0 {
		return nil, 0, 0, fmt.Errorf("huozhi 业务错误 code=%d", pr.Code)
	}
	return pr.Data.List, pr.Data.Pagination.PageSize, pr.Data.Pagination.Total, nil
}

// mapBill 把上游账单映射为统一事件。
func (h *HuozhiSource) mapBill(b *huozhiBill, loc *time.Location) (*model.Event, error) {
	txTime, err := time.ParseInLocation("2006-01-02 15:04:05", b.TxDate, loc)
	if err != nil {
		// 防御：个别历史数据格式异常时退回仅日期，不中断整轮同步。
		txTime, err = time.ParseInLocation("2006-01-02", b.TxDate, loc)
		if err != nil {
			return nil, fmt.Errorf("tx_date 解析失败: %q", b.TxDate)
		}
	}
	ev := &model.Event{
		Source:        h.Name(),
		ExternalID:    strconv.FormatUint(uint64(b.ID), 10),
		EventType:     model.EventTransaction,
		TimePrecision: model.PrecisionSecond,
		OccurredAt:    txTime.UTC().Unix(),
		LocalDate:     txTime.In(loc).Format("2006-01-02"),
		Title:         billTitle(b),
		Summary:       billSummary(b),
		AmountCents:   int64(math.Round(b.Amount * 100)),
		Currency:      b.Currency,
		Detail: map[string]any{
			"type":        b.Type,
			"description": b.Description,
			"merchant":    b.Merchant,
			"amount_yuan": b.Amount,
		},
		SourceURL: h.cfg.BaseURL + "/#/bill/" + strconv.FormatUint(uint64(b.ID), 10),
	}
	return ev, nil
}

// billTitle 时间轴卡片标题：优先商家，其次描述，最后类型兜底。
func billTitle(b *huozhiBill) string {
	if b.Merchant != "" {
		return b.Merchant
	}
	if b.Description != "" {
		return b.Description
	}
	return txTypeLabel(b.Type)
}

// billSummary 摘要行：类型中文 + 金额（元，两位小数）。
func billSummary(b *huozhiBill) string {
	return fmt.Sprintf("%s ¥%.2f", txTypeLabel(b.Type), b.Amount)
}

// txTypeLabel 交易类型中文化（前端直接展示，避免翻译腔）。
func txTypeLabel(t string) string {
	switch t {
	case "expense":
		return "支出"
	case "income":
		return "收入"
	case "transfer":
		return "转账"
	case "refund":
		return "退款"
	case "reimburse":
		return "报销"
	case "adjust":
		return "余额调整"
	default:
		if t == "" {
			return "账单"
		}
		return t
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// 编译期断言：HuozhiSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*HuozhiSource)(nil)
