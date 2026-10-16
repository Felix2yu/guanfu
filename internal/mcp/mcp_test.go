package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Felix2yu/guanfu/internal/model"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/mcptest"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "guanfu.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func put(t *testing.T, st *store.Store, e *model.Event) {
	t.Helper()
	if _, err := st.UpsertEvent(e); err != nil {
		t.Fatalf("写入 %s 失败: %v", e.ExternalID, err)
	}
}

// seed 两笔记账（一收一支）、一场演出、一条观看。
// 时间戳与 local_date 必须自洽：2026-10-10T05:30Z = 当地（+08）13:30。
func seed(t *testing.T, st *store.Store) {
	t.Helper()
	put(t, st, &model.Event{
		Source: "huozhi", ExternalID: "1", EventType: model.EventTransaction,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1_791_610_200, LocalDate: "2026-10-10",
		Title: "午饭", Summary: "食堂", AmountCents: 2350, Currency: "CNY",
		Detail: map[string]any{"type": "expense"},
	})
	put(t, st, &model.Event{
		Source: "huozhi", ExternalID: "2", EventType: model.EventTransaction,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1_791_640_800, LocalDate: "2026-10-10",
		Title: "稿费", AmountCents: 88000, Currency: "CNY",
		Detail: map[string]any{"type": "income"},
	})
	put(t, st, &model.Event{
		Source: "mujian", ExternalID: "9", EventType: model.EventPerformance,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1_791_720_000, LocalDate: "2026-10-11",
		Title: "《雷雨》", LocationName: "首都剧场",
		Detail: map[string]any{"rating": 5},
	})
	put(t, st, &model.Event{
		Source: "bili-history", ExternalID: "bv1", EventType: model.EventVideoWatch,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1_791_730_800, LocalDate: "2026-10-11",
		Title: "SvelteKit 5 迁移实录", Summary: "UP：某某",
		Media: []model.MediaRef{{ID: "cover-1", Kind: "cover"}},
	})
}

func call(t *testing.T, c interface {
	CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}}
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("调用 %s 失败: %v", name, err)
	}
	return res
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("工具返回 isError: %s", rawText(res))
	}
	return rawText(res)
}

func rawText(res *mcp.CallToolResult) string {
	parts := make([]string, 0, len(res.Content))
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func decode(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("结果不是可解析 JSON: %v\n%s", err, rawText(res))
	}
	return out
}

func TestToolsListed(t *testing.T) {
	st := newStore(t)
	seed(t, st)
	srv, err := mcptest.NewServer(t, Tools(st)...)
	if err != nil {
		t.Fatalf("mcptest 启动失败: %v", err)
	}
	defer srv.Close()

	tools, err := srv.Client().ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	got := make([]string, 0, len(tools.Tools))
	for _, tl := range tools.Tools {
		got = append(got, tl.Name)
	}
	want := []string{"guanfu_events", "guanfu_range", "guanfu_sources", "guanfu_stats", "guanfu_timeline"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("工具清单不符（须全部只读）: got=%v want=%v", got, want)
	}
	for _, tl := range tools.Tools {
		if !strings.HasPrefix(tl.Name, "guanfu_") {
			t.Fatalf("工具名缺少 guanfu_ 前缀: %s", tl.Name)
		}
	}
}

func TestRangeAndTimeline(t *testing.T) {
	st := newStore(t)
	seed(t, st)
	srv, err := mcptest.NewServer(t, Tools(st)...)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c := srv.Client()

	r := decode(t, call(t, c, "guanfu_range", nil))
	if r["min_date"] != "2026-10-10" || r["max_date"] != "2026-10-11" {
		t.Fatalf("日期范围有误: %v ~ %v", r["min_date"], r["max_date"])
	}
	if r["total_count"].(float64) != 4 {
		t.Fatalf("总条数期望 4: %v", r["total_count"])
	}
	if len(r["by_source"].([]any)) != 3 {
		t.Fatalf("by_source 期望 3 组: %v", r["by_source"])
	}

	d := decode(t, call(t, c, "guanfu_timeline", map[string]any{"date": "2026-10-10"}))
	if d["total_count"].(float64) != 2 {
		t.Fatalf("当日条数期望 2: %v", d["total_count"])
	}
	if d["weekday"] != "周六" {
		t.Fatalf("2026-10-10 应为周六，实际 %v", d["weekday"])
	}
	totals := d["totals"].(map[string]any)["CNY"].(map[string]any)
	if totals["income_cents"].(float64) != 88000 || totals["expense_cents"].(float64) != 2350 {
		t.Fatalf("当日收支持有误: %v", totals)
	}
	if d["truncated"] != false {
		t.Fatalf("未超限不应标记 truncated: %v", d["truncated"])
	}
	events := d["events"].([]any)
	first := events[0].(map[string]any)
	// local_time 由服务端按 GUANFU_TZ 下发：1_791_610_200 = 2026-10-10T05:30Z = 当地 13:30。
	if first["local_time"] != "13:30" {
		t.Fatalf("local_time 应为服务端换算的 13:30，实际 %v", first["local_time"])
	}

	// limit 生效时必须如实标记截断，避免模型把部分当全部。
	lim := decode(t, call(t, c, "guanfu_timeline", map[string]any{"date": "2026-10-10", "limit": 1}))
	if lim["returned"].(float64) != 1 || lim["truncated"] != true {
		t.Fatalf("limit/truncated 联动有误: returned=%v truncated=%v", lim["returned"], lim["truncated"])
	}

	// 空日期不报错，但必须给出「没有记录」的说明。
	empty := decode(t, call(t, c, "guanfu_timeline", map[string]any{"date": "2026-10-20"}))
	if empty["total_count"].(float64) != 0 || empty["note"] == nil {
		t.Fatalf("空日期应有 note: %v", empty)
	}
}

func TestStatsAndEvents(t *testing.T) {
	st := newStore(t)
	seed(t, st)
	srv, err := mcptest.NewServer(t, Tools(st)...)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c := srv.Client()

	s := decode(t, call(t, c, "guanfu_stats", map[string]any{
		"from": "2026-10-01", "to": "2026-10-31", "group_by": "event_type",
	}))
	rows := s["rows"].([]any)
	// transaction / performance / video_watch 三组。
	if len(rows) != 3 {
		t.Fatalf("event_type 分组期望 3 组: %v", rows)
	}
	byType := map[string]map[string]any{}
	for _, r := range rows {
		byType[r.(map[string]any)["key"].(string)] = r.(map[string]any)
	}
	if byType["performance"]["count"].(float64) != 1 {
		t.Fatalf("演出条数应为 1: %v", byType["performance"])
	}
	if s["amount_unit"] == nil || s["totals_rule"] == nil {
		t.Fatal("聚合结果必须自带金额单位与收支口径说明")
	}

	// source 收窄 + 起止写反时自动交换。
	swapped := decode(t, call(t, c, "guanfu_stats", map[string]any{
		"from": "2026-10-31", "to": "2026-10-01", "group_by": "source", "source": "huozhi",
	}))
	if swapped["total_count"].(float64) != 2 || swapped["from"] != "2026-10-01" {
		t.Fatalf("区间交换或过滤有误: %v", swapped)
	}

	list := decode(t, call(t, c, "guanfu_events", map[string]any{
		"from": "2026-10-01", "to": "2026-10-31", "q": "雷雨",
	}))
	if list["total_count"].(float64) != 1 {
		t.Fatalf("关键词命中数有误: %v", list)
	}
	ev := list["events"].([]any)[0].(map[string]any)
	if ev["source"] != "mujian" || ev["location_name"] != "首都剧场" {
		t.Fatalf("命中内容有误: %v", ev)
	}
	// detail 与媒体不进 MCP 视图，只给数量。
	if _, ok := ev["detail"]; ok {
		t.Fatal("compact 视图不应包含 detail")
	}

	// 分页：offset 越界返回空列表而不是报错。
	page := decode(t, call(t, c, "guanfu_events", map[string]any{
		"from": "2026-10-01", "to": "2026-10-31", "offset": 99,
	}))
	if page["returned"].(float64) != 0 || page["truncated"] != false {
		t.Fatalf("越界分页有误: %v", page)
	}
}

func TestSourcesHealth(t *testing.T) {
	st := newStore(t)
	if err := st.SaveSourceState("huozhi", true, 1800, "2026-10-10", 1_791_610_200, "", 12); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSourceState("dawarich", true, 1800, "", 0, "缺少 DAWARICH_API_KEY", 0); err != nil {
		t.Fatal(err)
	}
	srv, err := mcptest.NewServer(t, Tools(st)...)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	out := decode(t, call(t, srv.Client(), "guanfu_sources", nil))
	list := out["sources"].([]any)
	if len(list) != 2 {
		t.Fatalf("期望 2 个源: %v", list)
	}
	var h, d map[string]any
	for _, it := range list {
		m := it.(map[string]any)
		if m["name"] == "huozhi" {
			h = m
		} else {
			d = m
		}
	}
	if h["last_count"].(float64) != 12 || h["since_last_sync_secs"] == nil {
		t.Fatalf("huozhi 状态有误: %v", h)
	}
	if d["last_error"] != "缺少 DAWARICH_API_KEY" || d["since_last_sync_secs"] != nil {
		t.Fatalf("dawarich 状态有误: %v", d)
	}
}

func TestStrictDateValidation(t *testing.T) {
	st := newStore(t)
	seed(t, st)
	srv, err := mcptest.NewServer(t, Tools(st)...)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c := srv.Client()

	// 源侧时间格式五花八门，MCP 入口只认 YYYY-MM-DD，其余一律 isError 并给可照抄的示例。
	for _, bad := range []string{"2026-10-1", "2026/10/10", "1760000000", "2026-13-40"} {
		res := call(t, c, "guanfu_timeline", map[string]any{"date": bad})
		if !res.IsError {
			t.Fatalf("非法日期 %q 竟被接受: %s", bad, rawText(res))
		}
		if !strings.Contains(rawText(res), "YYYY-MM-DD") {
			t.Fatalf("错误文案应给出正确格式，实际: %s", rawText(res))
		}
	}
	// 聚合维度同理，非法值不得静默回落。
	res := call(t, c, "guanfu_stats", map[string]any{
		"from": "2026-10-01", "to": "2026-10-31", "group_by": "weekday",
	})
	if !res.IsError || !strings.Contains(rawText(res), "event_type") {
		t.Fatalf("非法维度应报错并列出可选值: %s", rawText(res))
	}
	// 缺必填参数。
	if r := call(t, c, "guanfu_stats", map[string]any{"from": "2026-10-01"}); !r.IsError {
		t.Fatal("缺 to 应报错")
	}
}

func TestEmptyStoreStillAnswers(t *testing.T) {
	st := newStore(t)
	srv, err := mcptest.NewServer(t, Tools(st)...)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	r := decode(t, call(t, srv.Client(), "guanfu_range", nil))
	if r["total_count"].(float64) != 0 || r["note"] == nil {
		t.Fatalf("空库应给出下一步提示: %v", r)
	}
}

func TestHTTPHandlerTokenGate(t *testing.T) {
	st := newStore(t)
	seed(t, st)
	srv := NewServer(st, "test")

	ts := httptest.NewServer(HTTPHandler(srv, "s3cret"))
	defer ts.Close()

	initBody := func() *strings.Reader {
		return strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	}
	post := func(auth string) int {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", initBody())
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := post(""); code != http.StatusUnauthorized {
		t.Fatalf("无凭据应为 401，实际 %d", code)
	}
	if code := post("Bearer nope"); code != http.StatusUnauthorized {
		t.Fatalf("错 token 应为 401，实际 %d", code)
	}
	if code := post("s3cret"); code != http.StatusUnauthorized {
		t.Fatalf("裸 token（缺 Bearer 前缀）应为 401，实际 %d", code)
	}
	if code := post("Bearer s3cret"); code == http.StatusUnauthorized {
		t.Fatal("正确 token 不应被拒")
	}
}

func TestHTTPHandlerNoTokenOpen(t *testing.T) {
	st := newStore(t)
	seed(t, st)
	srv := NewServer(st, "test")

	ts := httptest.NewServer(HTTPHandler(srv, ""))
	defer ts.Close()

	resp, err := ts.Client().Post(ts.URL+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Fatalf("未配 token 时内网应直接放行，实际 %d", resp.StatusCode)
	}
}
