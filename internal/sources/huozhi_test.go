package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// newFixtureServer 模拟 huozhi /api/public/bills：
// 返回 2 页共 3 条账单，并记录每次请求的查询参数供断言。
func newFixtureServer(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	page1 := map[string]any{
		"code": 0,
		"data": map[string]any{
			"list": []map[string]any{
				{"id": "101", "description": "午饭", "amount": 23.5, "currency": "CNY", "type": "expense",
					"tx_date": "2026-10-14 12:30:00", "merchant": "沙县小吃"},
				{"id": "102", "description": "工资", "amount": 10000, "currency": "CNY", "type": "income",
					"tx_date": "2026-10-15 09:00:00", "merchant": ""},
			},
			"pagination": map[string]any{"page": 1, "page_size": 2, "total": 3},
		},
	}
	page2 := map[string]any{
		"code": 0,
		"data": map[string]any{
			"list": []map[string]any{
				{"id": "103", "description": "退款", "amount": 58, "currency": "CNY", "type": "refund",
					"tx_date": "2026-10-15 18:45:12", "merchant": "京东"},
			},
			"pagination": map[string]any{"page": 2, "page_size": 2, "total": 3},
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.URL.RawQuery)
		if r.Header.Get("X-API-Key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		page := r.URL.Query().Get("page")
		var body map[string]any
		if page == "2" {
			body = page2
		} else {
			body = page1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestHuozhiSyncPaginationAndMapping(t *testing.T) {
	var seen []string
	srv := newFixtureServer(t, &seen)
	defer srv.Close()

	model.SetTimezone("Asia/Shanghai")
	src := NewHuozhiSource(config.SourceConfig{
		BaseURL:      srv.URL,
		APIKey:       "test-key",
		LookbackDays: 90,
	})

	var events []*model.Event
	cursor, count, err := src.Sync(context.Background(), "", func(ev *model.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if count != 3 {
		t.Fatalf("期望同步 3 条，实际 %d", count)
	}
	// 翻页断言：发起了 2 次请求，第 2 次带 page=2。
	if len(seen) != 2 {
		t.Fatalf("期望 2 次请求，实际 %d: %v", len(seen), seen)
	}
	if !strings.Contains(seen[0], "page=1") || !strings.Contains(seen[1], "page=2") {
		t.Fatalf("翻页参数异常: %v", seen)
	}
	// 日期范围参数存在。
	if !strings.Contains(seen[0], "start_date=") || !strings.Contains(seen[0], "end_date=") {
		t.Fatalf("缺少日期范围参数: %v", seen)
	}
	// cursor 形如 YYYY-MM-DD。
	if _, err := time.Parse("2006-01-02", cursor); err != nil {
		t.Fatalf("cursor 格式异常: %q", cursor)
	}

	// 映射断言：第一条支出。
	e := events[0]
	if e.Source != "huozhi" || e.ExternalID != "101" {
		t.Fatalf("幂等键异常: %s/%s", e.Source, e.ExternalID)
	}
	if e.EventType != model.EventTransaction {
		t.Fatalf("event_type 异常: %s", e.EventType)
	}
	if e.AmountCents != 2350 {
		t.Fatalf("金额换算异常: %d（期望 2350 分）", e.AmountCents)
	}
	if e.Currency != "CNY" {
		t.Fatalf("币种异常: %s", e.Currency)
	}
	if e.LocalDate != "2026-10-14" {
		t.Fatalf("local_date 异常: %s", e.LocalDate)
	}
	// occurred_at 为 UTC 等价：2026-10-14 12:30 +08 = 04:30Z。
	want := time.Date(2026, 10, 14, 4, 30, 0, 0, time.UTC).Unix()
	if e.OccurredAt != want {
		t.Fatalf("occurred_at 异常: %d（期望 %d）", e.OccurredAt, want)
	}
	if e.Title != "沙县小吃" || e.Summary != "支出 ¥23.50" {
		t.Fatalf("标题/摘要异常: %q / %q", e.Title, e.Summary)
	}
	if e.Detail["type"] != "expense" || e.Detail["merchant"] != "沙县小吃" {
		t.Fatalf("detail 异常: %v", e.Detail)
	}
	// 收入无商家时标题回落描述。
	if events[1].Title != "工资" {
		t.Fatalf("收入标题回落异常: %q", events[1].Title)
	}
}

func TestHuozhiSyncCursorIncrement(t *testing.T) {
	var seen []string
	srv := newFixtureServer(t, &seen)
	defer srv.Close()

	src := NewHuozhiSource(config.SourceConfig{
		BaseURL: srv.URL, APIKey: "test-key", LookbackDays: 90,
	})
	if _, _, err := src.Sync(context.Background(), "2026-10-10", func(*model.Event) error { return nil }); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	// cursor 次日开始：start_date=2026-10-11。
	if !strings.Contains(seen[0], "start_date=2026-10-11") {
		t.Fatalf("增量起点异常: %v", seen)
	}
}

func TestHuozhiSyncUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid api key"}`))
	}))
	defer srv.Close()

	src := NewHuozhiSource(config.SourceConfig{BaseURL: srv.URL, APIKey: "bad", LookbackDays: 90})
	_, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil })
	if err == nil {
		t.Fatal("期望 401 报错，实际成功")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误信息未含状态码: %v", err)
	}
}

func TestHuozhiUnconfigured(t *testing.T) {
	src := NewHuozhiSource(config.SourceConfig{})
	_, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("期望未配置错误，实际: %v", err)
	}
}

func TestBillTitleFallback(t *testing.T) {
	b := &huozhiBill{Type: "expense"}
	if got := billTitle(b); got != "支出" {
		t.Fatalf("类型兜底异常: %q", got)
	}
	if got := billSummary(b); got != "支出 ¥0.00" {
		t.Fatalf("摘要异常: %q", got)
	}
}
