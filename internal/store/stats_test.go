package store

import (
	"testing"

	"github.com/Felix2yu/guanfu/internal/model"
)

func seedStat(t *testing.T, st *Store, id, source, eventType, date string, cents int64, detailType string) {
	t.Helper()
	e := &model.Event{
		Source: source, ExternalID: id, EventType: eventType,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1_700_000_000,
		LocalDate: date, Title: id, AmountCents: cents, Currency: "CNY",
	}
	if detailType != "" {
		e.Detail = map[string]any{"type": detailType}
	}
	if _, err := st.UpsertEvent(e); err != nil {
		t.Fatalf("写入事件 %s 失败: %v", id, err)
	}
}

func TestStatsGroupsAndTotals(t *testing.T) {
	st := openTemp(t)
	seedStat(t, st, "h1", "huozhi", "transaction", "2026-10-05", 1200, "expense")
	seedStat(t, st, "h2", "huozhi", "transaction", "2026-10-05", 500000, "income")
	seedStat(t, st, "h3", "huozhi", "transaction", "2026-10-06", 800, "transfer") // 非 income → 支出
	seedStat(t, st, "q1", "qiansi", "gift_money", "2026-10-06", 20000, "income")
	seedStat(t, st, "m1", "mujian", "performance", "2026-10-07", 0, "") // 无金额事件
	seedStat(t, st, "d1", "diarum", "diary", "2026-11-01", 0, "")       // 区间外

	got, err := st.Stats(StatFilter{From: "2026-10-01", To: "2026-10-31", GroupBy: "source"})
	if err != nil {
		t.Fatalf("Stats 失败: %v", err)
	}
	if got.TotalCount != 5 {
		t.Fatalf("total_count 期望 5，实际 %d", got.TotalCount)
	}
	bySource := map[string]StatRow{}
	for _, r := range got.Rows {
		bySource[r.Key] = r
	}
	h := bySource["huozhi"]
	if h.Count != 3 {
		t.Fatalf("huozhi 条数期望 3，实际 %d", h.Count)
	}
	if cny := h.Totals["CNY"]; cny.IncomeCents != 500000 || cny.ExpenseCents != 2000 {
		t.Fatalf("huozhi 收支持有误: %+v（期望 收 500000 / 支 2000）", cny)
	}
	if _, ok := bySource["diarum"]; ok {
		t.Fatal("11 月的事件不应出现在 10 月区间")
	}
	q := bySource["qiansi"].Totals["CNY"]
	if q.IncomeCents != 20000 || q.ExpenseCents != 0 {
		t.Fatalf("qiansi 礼金口径有误: %+v", q)
	}
	// 无金额事件仍要计入条数，但收支持保持 0。
	mj := bySource["mujian"]
	if mj.Count != 1 {
		t.Fatalf("mujian 条数期望 1，实际 %d", mj.Count)
	}
	if mj.Totals["CNY"] != (MoneyTotals{}) {
		t.Fatalf("无金额事件不应产生收支: %+v", mj.Totals)
	}
	// 全局合计 = 各组之和。
	if tot := got.Totals["CNY"]; tot.IncomeCents != 520000 || tot.ExpenseCents != 2000 {
		t.Fatalf("全局合计有误: %+v", tot)
	}
}

func TestStatsGroupByMonthAndFilters(t *testing.T) {
	st := openTemp(t)
	seedStat(t, st, "a", "huozhi", "transaction", "2026-09-30", 100, "expense")
	seedStat(t, st, "b", "huozhi", "transaction", "2026-10-01", 200, "expense")
	seedStat(t, st, "c", "mujian", "performance", "2026-10-02", 0, "")

	got, err := st.Stats(StatFilter{From: "2026-09-01", To: "2026-10-31", GroupBy: "month", Source: "huozhi"})
	if err != nil {
		t.Fatalf("Stats 失败: %v", err)
	}
	if len(got.Rows) != 2 || got.Rows[0].Key != "2026-09" || got.Rows[1].Key != "2026-10" {
		t.Fatalf("month 分组或升序有误: %+v", got.Rows)
	}
	if got.TotalCount != 2 {
		t.Fatalf("source 过滤未生效: total=%d", got.TotalCount)
	}

	// event_type 收窄。
	got, err = st.Stats(StatFilter{From: "2026-09-01", To: "2026-10-31", GroupBy: "day", EventType: "performance"})
	if err != nil {
		t.Fatalf("Stats 失败: %v", err)
	}
	if got.TotalCount != 1 || got.Rows[0].Key != "2026-10-02" {
		t.Fatalf("event_type 过滤有误: %+v", got.Rows)
	}
}

func TestStatsRejectsUnknownGroupBy(t *testing.T) {
	st := openTemp(t)
	seedStat(t, st, "a", "huozhi", "transaction", "2026-10-01", 100, "expense")

	if _, err := st.Stats(StatFilter{From: "2026-10-01", To: "2026-10-02", GroupBy: "currency; DROP TABLE events"}); err == nil {
		t.Fatal("非法聚合维度应当被白名单拒绝")
	}
	// 空维度回落到默认口径而不是报错。
	got, err := st.Stats(StatFilter{From: "2026-10-01", To: "2026-10-02"})
	if err != nil {
		t.Fatalf("默认维度失败: %v", err)
	}
	if got.GroupBy != "event_type" || got.Rows[0].Key != "transaction" {
		t.Fatalf("默认维度应为 event_type: %+v", got)
	}
	// 起止漏填必须报错，不能静默全表扫。
	if _, err := st.Stats(StatFilter{From: "2026-10-01", GroupBy: "day"}); err == nil {
		t.Fatal("缺 to 应报错")
	}
}

func TestStatsSurvivesDirtyDetailJSON(t *testing.T) {
	st := openTemp(t)
	seedStat(t, st, "ok", "huozhi", "transaction", "2026-10-01", 100, "income")
	// 直接写库造脏数据：detail_json 为空串与非法 JSON 各一条。
	if _, err := st.DB.Exec(`INSERT INTO events (source, external_id, event_type, time_precision,
		occurred_at, local_date, title, amount_cents, currency, detail_json, created_at, updated_at)
		VALUES ('huozhi','bad1','transaction','second',1,'2026-10-01','x',500,'CNY','',1,1),
		       ('huozhi','bad2','transaction','second',1,'2026-10-01','y',700,'CNY','{不是JSON',1,1)`); err != nil {
		t.Fatalf("造脏数据失败: %v", err)
	}

	got, err := st.Stats(StatFilter{From: "2026-10-01", To: "2026-10-01", GroupBy: "source"})
	if err != nil {
		t.Fatalf("脏 detail_json 导致查询失败（json_valid 兜底未生效）: %v", err)
	}
	row := got.Rows[0]
	totals := row.Totals["CNY"]
	if totals.IncomeCents != 100 || totals.ExpenseCents != 1200 {
		t.Fatalf("脏数据应计入支出而非报错: %+v", totals)
	}
	if row.Count != 3 {
		t.Fatalf("条数期望 3，实际 %d", row.Count)
	}
}

func TestListEventsQueryIsLiteral(t *testing.T) {
	st := openTemp(t)
	seedStat(t, st, "a", "huozhi", "transaction", "2026-10-01", 100, "expense")
	seedStat(t, st, "b", "huozhi", "transaction", "2026-10-01", 200, "expense")
	if _, err := st.DB.Exec(`UPDATE events SET title='会员10%折扣' WHERE external_id='b'`); err != nil {
		t.Fatal(err)
	}

	// 关键词含 LIKE 元字符时必须按字面量匹配，否则 "10%" 会命中所有以 10 开头的标题。
	list, err := st.ListEvents(EventQuery{Date: "2026-10-01", Query: "10%"})
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(list) != 1 || list[0].ExternalID != "b" {
		t.Fatalf("10%% 应只命中标题里真的含 10%% 的一条: %s", extIDs(list))
	}
	list, err = st.ListEvents(EventQuery{Date: "2026-10-01", Query: "100"})
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("title/summary 里没有 100，不应命中: %s", extIDs(list))
	}
}

func extIDs(list []*model.Event) string {
	out := ""
	for _, e := range list {
		out += e.ExternalID + " "
	}
	return out
}
