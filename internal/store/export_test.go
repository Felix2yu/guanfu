package store

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Felix2yu/guanfu/internal/model"
)

// TestExportEventsNoCap 导出必须一条不落：ListEvents 的 1000 上限是时间轴的护栏，
// 不能变成导出的天花板。
func TestExportEventsNoCap(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "export.db"))
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	defer st.Close()

	const total = 1200
	for i := 0; i < total; i++ {
		day := fmt.Sprintf("2026-%02d-%02d", 1+i/30, 1+i%30)
		ev := &model.Event{
			Source: "huozhi", ExternalID: fmt.Sprintf("t%d", i), EventType: model.EventTransaction,
			TimePrecision: model.PrecisionSecond, OccurredAt: int64(1_790_000_000 + i),
			LocalDate: day, Title: "x", AmountCents: 100, Currency: "CNY",
		}
		if _, err := st.UpsertEvent(ev); err != nil {
			t.Fatalf("写入 %d 失败: %v", i, err)
		}
	}

	n := 0
	if err := st.ExportEvents("", "", func(*model.Event) error { n++; return nil }); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if n != total {
		t.Fatalf("导出应覆盖全库 %d 条，实际 %d 条", total, n)
	}

	// 区间过滤同样精确（local_date 口径）。
	n = 0
	if err := st.ExportEvents("2026-01-01", "2026-01-31", func(*model.Event) error { n++; return nil }); err != nil {
		t.Fatalf("区间导出失败: %v", err)
	}
	if n != 30 {
		t.Fatalf("1 月应 30 条，实际 %d", n)
	}
}

// TestExportEventsOrderAndCallbackError 顺序稳定 + 回调错误能中断并原样抛出。
func TestExportEventsOrderAndCallbackError(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "order.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	seed := func(ext, day string, occurred int64) {
		_, err := st.UpsertEvent(&model.Event{
			Source: "huozhi", ExternalID: ext, EventType: model.EventTransaction,
			TimePrecision: model.PrecisionSecond, OccurredAt: occurred, LocalDate: day, Title: ext,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("c", "2026-03-01", 300)
	seed("a", "2026-01-02", 100)
	seed("b", "2026-01-01", 200) // 同日更晚时刻

	var order []string
	if err := st.ExportEvents("", "", func(e *model.Event) error {
		order = append(order, e.ExternalID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 按 local_date → occurred_at → id 升序：同日内 b(01-01) 在 a(01-02) 前。
	if len(order) != 3 || order[0] != "b" || order[1] != "a" || order[2] != "c" {
		t.Fatalf("导出顺序有误: %v", order)
	}

	sentinel := fmt.Errorf("写入端断了")
	n := 0
	err = st.ExportEvents("", "", func(*model.Event) error {
		n++
		if n == 2 {
			return sentinel
		}
		return nil
	})
	if err != sentinel {
		t.Fatalf("回调错误应原样抛出，实际 %v", err)
	}
	if n != 2 {
		t.Fatalf("出错后应立即停止遍历，实际处理 %d 条", n)
	}
}
