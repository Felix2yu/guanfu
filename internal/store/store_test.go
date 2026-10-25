package store

import (
	"path/filepath"
	"testing"

	"github.com/Felix2yu/guanfu/internal/model"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestUpsertEventIdempotent(t *testing.T) {
	st := openTemp(t)
	e := &model.Event{
		Source: "huozhi", ExternalID: "101", EventType: model.EventTransaction,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1000, LocalDate: "2026-10-14",
		Title: "午饭", AmountCents: 2350, Currency: "CNY",
		Detail: map[string]any{"type": "expense"},
	}
	for i := 0; i < 2; i++ {
		if _, err := st.UpsertEvent(e); err != nil {
			t.Fatalf("第 %d 次 Upsert 失败: %v", i+1, err)
		}
	}
	list, err := st.ListEvents(EventQuery{Date: "2026-10-14"})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("幂等失败：期望 1 条，实际 %d", len(list))
	}
	// 修订后覆盖。
	e.Title = "午饭（改）"
	if _, err := st.UpsertEvent(e); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	list, _ = st.ListEvents(EventQuery{Date: "2026-10-14"})
	if list[0].Title != "午饭（改）" {
		t.Fatalf("更新未生效: %q", list[0].Title)
	}
	if list[0].Detail["type"] != "expense" {
		t.Fatalf("detail JSON 往返异常: %v", list[0].Detail)
	}
}

func TestUpsertEventChangeDetection(t *testing.T) {
	st := openTemp(t)
	e := &model.Event{
		Source: "huozhi", ExternalID: "900", EventType: model.EventTransaction,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1000, LocalDate: "2026-10-14",
		Title: "午饭", AmountCents: 2350, Currency: "CNY",
		Detail: map[string]any{"type": "expense"},
	}
	changed, err := st.UpsertEvent(e)
	if err != nil || !changed {
		t.Fatalf("首次写入应 changed=true: %v, %v", changed, err)
	}
	before, _ := st.ListEvents(EventQuery{Date: "2026-10-14"})
	// 内容完全相同的重放：不算变更，updated_at 也不该被碰。
	changed, err = st.UpsertEvent(e)
	if err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if changed {
		t.Fatal("重复写入同一内容不应计为变更，否则「新增 N 条」会虚报")
	}
	after, _ := st.ListEvents(EventQuery{Date: "2026-10-14"})
	if after[0].UpdatedAt != before[0].UpdatedAt {
		t.Fatal("无变化时 updated_at 不应被更新")
	}
	// 修订标题 → 变更。
	e.Title = "午饭（改）"
	changed, err = st.UpsertEvent(e)
	if err != nil || !changed {
		t.Fatalf("修订后应 changed=true: %v, %v", changed, err)
	}
}

func TestListEventsFilters(t *testing.T) {
	st := openTemp(t)
	_ = mustUpsert(t, st, &model.Event{Source: "huozhi", ExternalID: "1", EventType: model.EventTransaction, OccurredAt: 100, LocalDate: "2026-10-13", Title: "a"})
	_ = mustUpsert(t, st, &model.Event{Source: "huozhi", ExternalID: "2", EventType: model.EventTransaction, OccurredAt: 200, LocalDate: "2026-10-14", Title: "b"})
	_ = mustUpsert(t, st, &model.Event{Source: "mujian", ExternalID: "3", EventType: model.EventPerformance, OccurredAt: 300, LocalDate: "2026-10-14", Title: "c"})

	// 按天
	list, _ := st.ListEvents(EventQuery{Date: "2026-10-14"})
	if len(list) != 2 {
		t.Fatalf("按天查询异常: %d", len(list))
	}
	// 按天 + 时刻正序
	if list[0].Title != "b" || list[1].Title != "c" {
		t.Fatalf("排序异常: %v", []string{list[0].Title, list[1].Title})
	}
	// 按源
	list, _ = st.ListEvents(EventQuery{Source: "mujian"})
	if len(list) != 1 {
		t.Fatalf("按源查询异常: %d", len(list))
	}
	// 按类型
	list, _ = st.ListEvents(EventQuery{EventType: model.EventTransaction})
	if len(list) != 2 {
		t.Fatalf("按类型查询异常: %d", len(list))
	}
	// 区间
	list, _ = st.ListEvents(EventQuery{From: "2026-10-14", To: "2026-10-14"})
	if len(list) != 2 {
		t.Fatalf("区间查询异常: %d", len(list))
	}
	// 计数
	counts, _ := st.CountByDate("2026-10-14")
	if counts["huozhi"] != 1 || counts["mujian"] != 1 {
		t.Fatalf("计数异常: %v", counts)
	}
	// 日期范围
	min, max, _ := st.DateRange()
	if min != "2026-10-13" || max != "2026-10-14" {
		t.Fatalf("日期范围异常: %s ~ %s", min, max)
	}
}

func TestSourceStateRoundTrip(t *testing.T) {
	st := openTemp(t)
	// 未初始化时读游标返回空而非报错。
	cur, err := st.GetSourceCursor("huozhi")
	if err != nil || cur != "" {
		t.Fatalf("初始游标异常: %q, %v", cur, err)
	}
	if err := st.SaveSourceState("huozhi", true, 1800, "2026-10-14", 123, "", 5); err != nil {
		t.Fatalf("保存状态失败: %v", err)
	}
	cur, _ = st.GetSourceCursor("huozhi")
	if cur != "2026-10-14" {
		t.Fatalf("游标异常: %q", cur)
	}
	// 错误态保存必须保留游标（引擎 recordError 依赖此行为）。
	if err := st.SaveSourceState("huozhi", true, 1800, cur, 0, "boom", 0); err != nil {
		t.Fatalf("保存错误态失败: %v", err)
	}
	states, _ := st.ListSourceStates()
	if len(states) != 1 || states[0].LastError != "boom" || states[0].Cursor != "2026-10-14" {
		t.Fatalf("错误态异常: %+v", states)
	}
}

func mustUpsert(t *testing.T, st *Store, e *model.Event) error {
	t.Helper()
	_, err := st.UpsertEvent(e)
	return err
}
