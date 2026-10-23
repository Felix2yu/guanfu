package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// collect 返回一个 upsert 回调与收集切片。
func collect() (func(*model.Event) error, *[]*model.Event) {
	var got []*model.Event
	return func(ev *model.Event) error {
		got = append(got, ev)
		return nil
	}, &got
}

// m4Today 按观复本地时区算「今天」，与各源 Sync 里的 time.Now().In(loc) 同源。
// 直接用 time.Now() 取的是进程本地时区，UTC runner 会在北京时间 00:00–08:00
// 窗口比生产侧少一天，cursor 断言每逢周五 00:00 依赖批次必挂。
func m4Today() string {
	return time.Now().In(model.LocalLocation()).Format("2006-01-02")
}

// ---------- qiansi ----------

// qiansiFixture 模拟牵丝：events 分两页（第一页满 200 触发翻页），
// transactions 只返回礼金，anniversaries 返回一条循环纪念日 + 一条已过期。
func qiansiFixture(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = append(*seen, r.URL.Path+"?"+r.URL.RawQuery+" auth="+r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/events/":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			// 上游返回裸数组（不是 {data:...}）。
			if offset == 0 {
				batch := make([]map[string]any, 0, qiansiPageSize)
				batch = append(batch, map[string]any{
					"id": "e1", "title": "老友聚会", "type_name": "聚会",
					"event_date": "2026-10-15",
					"location":   "上海 · 静安", "locations": []string{"上海", "静安"},
					"has_gift": true, "gift_amount_fen": 88800, "gift_direction": "in",
					"summary":      "顺带给小侄女塞了个红包",
					"participants": []map[string]any{{"id": "p1", "name": "张三"}, {"id": "p2", "name": "李四"}},
				})
				// 其余 199 条用非法日期，映射时应被跳过（验证过滤而非计数膨胀）。
				for i := 0; i < qiansiPageSize-1; i++ {
					batch = append(batch, map[string]any{"id": "x" + strconv.Itoa(i), "title": "占位", "event_date": "bad-date"})
				}
				_ = json.NewEncoder(w).Encode(batch)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{}) // 第二页空 → 结束翻页
		case r.URL.Path == "/api/v1/transactions/":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "t1", "kind": "gift", "direction": "out", "amount_fen": 20000,
					"title": "随礼", "occurred_at": "2026-10-14", "settled": true, "person_name": "王五"},
			})
		case r.URL.Path == "/api/v1/anniversaries/":
			days := 5
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "a1", "title": "生日", "date": "1990-03-08", "is_lunar": false,
					"repeat_yearly": true, "person_name": "张三", "next_date": "2027-03-08", "days_until": days},
				{"id": "a2", "title": "旧约定", "date": "2020-01-02", "repeat_yearly": false, "next_date": ""},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestQiansiSyncMappingAndPaging(t *testing.T) {
	var seen []string
	srv := qiansiFixture(t, &seen)
	src := NewQiansiSource(config.SourceConfig{
		BaseURL: srv.URL, APIKey: "tok", LookbackDays: 30, Interval: time.Hour,
	})
	upsert, got := collect()
	cursor, n, err := src.Sync(context.Background(), "", upsert)
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if n != 3 || len(*got) != 3 {
		t.Fatalf("应产出 3 条事件（社交+礼金+纪念日），实际 %d", n)
	}
	want := m4Today()
	if cursor != want {
		t.Fatalf("cursor 应为今天 %s，实际 %s", want, cursor)
	}

	// 翻页：events 第一页满 200 → 必须再请求第二页。
	pages := 0
	for _, s := range seen {
		if strings.HasPrefix(s, "/api/v1/events/") {
			pages++
		}
		if !strings.Contains(s, "auth=Bearer tok") {
			t.Fatalf("应带 Bearer 鉴权，实际请求 %q", s)
		}
	}
	if pages != 2 {
		t.Fatalf("events 应请求 2 页，实际 %d", pages)
	}

	byID := map[string]*model.Event{}
	for _, ev := range *got {
		byID[ev.ExternalID] = ev
	}

	// 社交事件：幂等键加表前缀，礼金方向 in → income，金额分。
	se := byID["ev:e1"]
	if se == nil {
		t.Fatal("缺少 ev:e1 事件")
	}
	if se.EventType != model.EventSocialEvent || se.TimePrecision != model.PrecisionDay || se.LocalDate != "2026-10-15" {
		t.Fatalf("社交事件字段异常: %+v", se)
	}
	if se.AmountCents != 88800 || se.Detail["type"] != "income" {
		t.Fatalf("礼金金额/方向异常: cents=%d detail=%v", se.AmountCents, se.Detail)
	}
	if se.LocationName != "上海 · 静安" {
		// locations 数组优先；不能把服务端拼好的 location 串再叠一次。
		t.Fatalf("地点应取 locations 数组，实际 %q", se.LocationName)
	}
	if !strings.Contains(se.Summary, "礼金 +¥888.00") || !strings.Contains(se.Summary, "张三") {
		t.Fatalf("摘要异常: %q", se.Summary)
	}

	// 礼金交易：direction out → 无 income 标记（前端按 − 显示）。
	gm := byID["tx:t1"]
	if gm == nil || gm.EventType != model.EventGiftMoney {
		t.Fatalf("礼金事件异常: %+v", gm)
	}
	if gm.AmountCents != 20000 || gm.Detail["type"] != nil {
		t.Fatalf("礼金方向异常: %+v", gm.Detail)
	}
	if !strings.Contains(gm.Summary, "送礼 ¥200.00") || !strings.Contains(gm.Summary, "已结清") {
		t.Fatalf("礼金摘要异常: %q", gm.Summary)
	}

	// 纪念日：按 next_date 投放，无限次的那条跳过。
	ann := byID["ann:a1:2027-03-08"]
	if ann == nil {
		t.Fatalf("纪念日应按 next_date 投放，实际 %v", byID)
	}
	if ann.LocalDate != "2027-03-08" || ann.EventType != model.EventAnniversary {
		t.Fatalf("纪念日字段异常: %+v", ann)
	}
	if !strings.Contains(ann.Title, "张三") || !strings.Contains(ann.Summary, "每年") {
		t.Fatalf("纪念日文案异常: title=%q summary=%q", ann.Title, ann.Summary)
	}
	if byID["ann:a2:"] != nil {
		t.Fatal("无 next_date 的纪念日不应入轴")
	}
}

// ---------- shenshi ----------

func shenshiFixture(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = append(*seen, r.URL.Path+"?"+r.URL.RawQuery+" auth="+r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tasks":
			if r.URL.Query().Get("status") != "done" || r.URL.Query().Get("sortBy") != "completed" {
				t.Errorf("tasks 应按完成时间拉已完成：%q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"count": 2,
				"tasks": []map[string]any{
					{"id": "11", "title": "写观复 M4", "status": "done", "priority": 2,
						"completedAt": "2026-10-15T22:41:33+08:00", "dueDate": "2026-10-15",
						"important": true, "urgent": true,
						"tags":     []map[string]any{{"id": "1", "name": "观复"}},
						"subtasks": []map[string]any{{"id": "1", "status": "done"}, {"id": "2", "status": "todo"}}},
					{"id": "12", "title": "未完成不该出现", "status": "done", "completedAt": nil},
				},
			})
		case "/api/reviews":
			// 第一条是空壳（上游缺记录时的形态），必须跳过。
			_ = json.NewEncoder(w).Encode(map[string]any{
				"reviews": []map[string]any{
					{"id": "0", "date": m4Today()},
					{"id": "5", "date": m4Today(), "mood": "平静",
						"wins": "完成了三源适配器", "blockers": "沙箱代理不稳", "tomorrow": "跑真实同步"},
				},
			})
		case "/api/habits":
			if r.URL.Query().Get("includeArchived") != "1" {
				t.Errorf("习惯看板应含归档：%q", r.URL.RawQuery)
			}
			today := m4Today()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"from": r.URL.Query().Get("from"), "to": r.URL.Query().Get("to"), "today": today,
				"habits": []map[string]any{{"id": "1", "name": "晨跑"}, {"id": "2", "name": "阅读"}},
				"logs": []map[string]any{
					{"id": "1", "habitId": "1", "day": today, "count": 1, "note": "5km", "createdAt": today + "T07:10:00+08:00"},
					{"id": "2", "habitId": "2", "day": today, "count": 2, "createdAt": today + "T21:00:00+08:00"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestShenshiSyncTaskReviewHabit(t *testing.T) {
	var seen []string
	srv := shenshiFixture(t, &seen)
	src := NewShenshiSource(config.SourceConfig{
		BaseURL: srv.URL, APIKey: "tok", LookbackDays: 30, Interval: time.Hour,
	})
	upsert, got := collect()
	if _, _, err := src.Sync(context.Background(), "", upsert); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	byID := map[string]*model.Event{}
	for _, ev := range *got {
		byID[ev.ExternalID] = ev
	}

	// 任务：completedAt 是 RFC3339(+08:00)，要按本地时区归日、按秒精度入库。
	tk := byID["task:11"]
	if tk == nil {
		t.Fatalf("缺少 task:11，实际 %v", byID)
	}
	if tk.EventType != model.EventTaskDone || tk.TimePrecision != model.PrecisionSecond {
		t.Fatalf("任务事件类型/精度异常: %+v", tk)
	}
	if tk.LocalDate != "2026-10-15" {
		t.Fatalf("完成日应为 2026-10-15（+08:00），实际 %s", tk.LocalDate)
	}
	wantAt := time.Date(2026, 10, 15, 22, 41, 33, 0, time.FixedZone("CST", 8*3600)).Unix()
	if tk.OccurredAt != wantAt {
		t.Fatalf("完成时刻应为 %d，实际 %d", wantAt, tk.OccurredAt)
	}
	if !strings.Contains(tk.Summary, "#观复") || !strings.Contains(tk.Summary, "重要且紧急") || !strings.Contains(tk.Summary, "子任务 1/2") {
		t.Fatalf("任务摘要异常: %q", tk.Summary)
	}
	if byID["task:12"] != nil {
		t.Fatal("completedAt 为空的任务不应入轴")
	}

	// 复盘：空壳跳过，有内容的入库（全天）。
	today := m4Today()
	rv := byID["review:"+today]
	if rv == nil {
		t.Fatalf("缺少当日复盘，实际 %v", byID)
	}
	if rv.EventType != model.EventReview || rv.TimePrecision != model.PrecisionDay {
		t.Fatalf("复盘事件异常: %+v", rv)
	}
	if rv.Detail["wins"] == nil || !strings.Contains(rv.Summary, "完成") {
		t.Fatalf("复盘内容异常: detail=%v summary=%q", rv.Detail, rv.Summary)
	}

	// 习惯：一天聚合一条，习惯名经 id→名称 映射。
	hb := byID["habit:"+today]
	if hb == nil {
		t.Fatalf("缺少习惯打卡聚合，实际 %v", byID)
	}
	if hb.EventType != model.EventHabitCheck || hb.Detail["habit_count"] != 2 {
		t.Fatalf("习惯聚合异常: %+v", hb.Detail)
	}
	habits, ok := hb.Detail["habits"].([]map[string]any)
	if !ok || len(habits) != 2 {
		t.Fatalf("习惯明细异常: %#v", hb.Detail["habits"])
	}
	if !strings.Contains(hb.Summary, "晨跑") || !strings.Contains(hb.Summary, "2 项") {
		t.Fatalf("习惯摘要异常: %q", hb.Summary)
	}
}

// ---------- liuxia ----------

func liuxiaFixture(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = append(*seen, r.URL.Path+"?"+r.URL.RawQuery)
		}
		if r.URL.Path != "/api/export" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("应请求 json 格式：%q", r.URL.RawQuery)
		}
		day := m4Today()
		// 裸数组；quality/aod 是指针可空。
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "1", "city": "上海", "date": day, "time": "05:42", "event_type": "morning", "model": "EC", "quality": 0.415, "aod": 0.18},
			{"id": "2", "city": "上海", "date": day, "time": "17:55", "event_type": "evening", "model": "GFS", "quality": 0.853, "aod": 0.22},
			{"id": "3", "city": "上海", "date": day, "time": "17:52", "event_type": "evening", "model": "EC", "quality": nil, "aod": nil},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLiuxiaSyncAggregatesByDay(t *testing.T) {
	var seen []string
	srv := liuxiaFixture(t, &seen)
	src := NewLiuxiaSource(config.SourceConfig{BaseURL: srv.URL, LookbackDays: 30, Interval: 6 * time.Hour})
	upsert, got := collect()
	today := m4Today()
	cursor, n, err := src.Sync(context.Background(), "", upsert)
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if n != 1 || len(*got) != 1 {
		t.Fatalf("一天一条，应产出 1 条事件，实际 %d", n)
	}
	// cursor 语义：固定在「今天」，未来预报段每轮重拉。
	if cursor != today {
		t.Fatalf("cursor 应为今天 %s，实际 %s", today, cursor)
	}
	if !strings.Contains(seen[0], "end=") {
		t.Fatalf("应带日期区间参数: %q", seen[0])
	}

	ev := (*got)[0]
	if ev.ExternalID != "sky:上海:"+today || ev.EventType != model.EventSkyForecast {
		t.Fatalf("天象事件字段异常: %+v", ev)
	}
	if ev.Title != "朝霞/晚霞预报" {
		t.Fatalf("标题应为「朝霞/晚霞预报」，实际 %q", ev.Title)
	}
	// 代表质量取各时段最高：朝霞 EC 0.42，晚霞 GFS 0.85；AOD 取时段代表值。
	if !strings.Contains(ev.Summary, "朝霞 0.42（EC）") || !strings.Contains(ev.Summary, "晚霞 0.85（GFS）") {
		t.Fatalf("摘要异常: %q", ev.Summary)
	}
	recs, ok := ev.Detail["records"].([]map[string]any)
	if !ok || len(recs) != 3 {
		t.Fatalf("原始记录应全量保留 3 条: %#v", ev.Detail["records"])
	}
	// 排序：morning 先，同段按模型字母序。
	if recs[0]["event_type"] != "morning" || recs[1]["model"] != "EC" {
		t.Fatalf("记录排序异常: %#v", recs)
	}
	if ev.Detail["aod_min"] != 0.18 {
		t.Fatalf("aod_min 应为 0.18，实际 %v", ev.Detail["aod_min"])
	}
}

// TestLiuxiaEmptyResponse 上游无记录时输出字面量 null，必须容错为 0 条。
func TestLiuxiaEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("null"))
	}))
	t.Cleanup(srv.Close)
	src := NewLiuxiaSource(config.SourceConfig{BaseURL: srv.URL, LookbackDays: 30})
	upsert, got := collect()
	if _, n, err := src.Sync(context.Background(), "", upsert); err != nil || n != 0 || len(*got) != 0 {
		t.Fatalf("null 响应应容错为 0 条：n=%d err=%v", n, err)
	}
}

// ---------- 辅助函数 ----------

func TestDayOnly(t *testing.T) {
	ok := []string{"2026-10-15", "2026-10-15 08:30:00", "2026-10-15T08:30:00+08:00"}
	for _, s := range ok {
		if got := dayOnly(s); got != "2026-10-15" {
			t.Errorf("dayOnly(%q) = %q，期望 2026-10-15", s, got)
		}
	}
	for _, s := range []string{"", "bad-date", "2026-13-45", "short"} {
		if got := dayOnly(s); got != "" {
			t.Errorf("dayOnly(%q) 应返回空，实际 %q", s, got)
		}
	}
}

func TestDaysBetweenAndFormatQuality(t *testing.T) {
	if d := daysBetween("2026-10-01", "2026-10-01"); d != 1 {
		t.Fatalf("同一天应为 1，实际 %d", d)
	}
	if d := daysBetween("2026-10-01", "2026-10-31"); d != 31 {
		t.Fatalf("十月应为 31 天，实际 %d", d)
	}
	if d := daysBetween("bad", "2026-10-31"); d != 30 {
		t.Fatalf("非法输入应回落 30，实际 %d", d)
	}
	// 2 位小数 + half-up（0.415 不该被银行家舍入成 0.41）。
	if got := formatQuality(0.415); got != "0.42" {
		t.Fatalf("formatQuality(0.415) = %q，期望 0.42", got)
	}
	if got := formatQuality(0.853); got != "0.85" {
		t.Fatalf("formatQuality(0.853) = %q，期望 0.85", got)
	}
}

func TestSkyTitleAndGiftSign(t *testing.T) {
	cases := []struct {
		items []liuxiaRecord
		want  string
	}{
		{[]liuxiaRecord{{EventType: "morning"}}, "朝霞预报"},
		{[]liuxiaRecord{{EventType: "evening"}}, "晚霞预报"},
		{[]liuxiaRecord{{EventType: "morning"}, {EventType: "evening"}}, "朝霞/晚霞预报"},
		{nil, "霞光预报"},
	}
	for _, c := range cases {
		if got := skyTitle(c.items); got != c.want {
			t.Errorf("skyTitle(%v) = %q，期望 %q", c.items, got, c.want)
		}
	}
	if giftSign(true) != "+" || giftSign(false) != "−" {
		t.Fatalf("礼金符号异常: %q %q", giftSign(true), giftSign(false))
	}
	if titleCase("", "兜底") != "兜底" || titleCase("标题", "兜底") != "标题" {
		t.Fatal("titleCase 兜底逻辑异常")
	}
	if fmtYuan(88800) != "888.00" || fmtYuan(1) != "0.01" {
		t.Fatalf("fmtYuan 异常: %q %q", fmtYuan(88800), fmtYuan(1))
	}
}

// TestM4Unconfigured 未配置 BaseURL 的三个源都应给出明确错误。
func TestM4Unconfigured(t *testing.T) {
	for name, src := range map[string]interface {
		Sync(context.Context, string, func(*model.Event) error) (string, int, error)
	}{
		"qiansi":  NewQiansiSource(config.SourceConfig{}),
		"shenshi": NewShenshiSource(config.SourceConfig{}),
		"liuxia":  NewLiuxiaSource(config.SourceConfig{}),
	} {
		if _, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
			t.Errorf("%s 未配置应报错", name)
		}
	}
}
