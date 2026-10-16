package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/model"
	"github.com/Felix2yu/guanfu/internal/store"
)

type exportPayload struct {
	Meta struct {
		GeneratedAt string `json:"generated_at"`
		Timezone    string `json:"timezone"`
		From        string `json:"from"`
		To          string `json:"to"`
		Count       int    `json:"count"`
		AmountUnit  string `json:"amount_unit"`
		Note        string `json:"note"`
	} `json:"meta"`
	Events []*model.Event `json:"events"`
}

func seedExport(t *testing.T, st *store.Store, source, extID, date string, cents int64, media []model.MediaRef) {
	t.Helper()
	day, _ := time.ParseInLocation("2006-01-02", date, model.LocalLocation())
	ev := &model.Event{
		Source: source, ExternalID: extID, EventType: model.EventTransaction,
		TimePrecision: model.PrecisionSecond, OccurredAt: day.Add(9 * time.Hour).UTC().Unix(),
		LocalDate: date, Title: extID, AmountCents: cents, Currency: "CNY", Media: media,
		Detail: map[string]any{"type": "expense"},
	}
	if _, err := st.UpsertEvent(ev); err != nil {
		t.Fatalf("种子事件失败: %v", err)
	}
}

func doExport(t *testing.T, h http.Handler, query string) (*httptest.ResponseRecorder, exportPayload) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/export"+query, nil))
	var out exportPayload
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("导出体不是合法 JSON: %v\n%s", err, rec.Body.String())
		}
	}
	return rec, out
}

func TestExportFullAndRange(t *testing.T) {
	srv, st := newTestServer(t, nil)
	seedExport(t, st, "huozhi", "h1", "2026-01-05", 1200, nil)
	seedExport(t, st, "huozhi", "h2", "2026-03-09", 300, nil)
	seedExport(t, st, "mujian", "m1", "2026-03-10", 8800, nil)
	h := srv.Handler()

	// 省略 from/to → 全库，并把实际边界回填进 meta（便于复核"导全了吗"）。
	rec, full := doExport(t, h, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("默认导出应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if full.Meta.Count != 3 || len(full.Events) != 3 {
		t.Fatalf("全量应为 3 条: count=%d len=%d", full.Meta.Count, len(full.Events))
	}
	if full.Meta.From != "2026-01-05" || full.Meta.To != "2026-03-10" {
		t.Fatalf("meta 边界回填有误: %s..%s", full.Meta.From, full.Meta.To)
	}
	if full.Meta.Timezone == "" || full.Meta.GeneratedAt == "" || full.Meta.AmountUnit != "cents" {
		t.Fatalf("meta 缺少复核所需字段: %+v", full.Meta)
	}
	// 顺序：local_date 升序。
	if full.Events[0].ExternalID != "h1" || full.Events[2].ExternalID != "m1" {
		t.Fatalf("导出顺序有误: %s..%s", full.Events[0].ExternalID, full.Events[2].ExternalID)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "guanfu-export-2026-01-05_2026-03-10.json") {
		t.Fatalf("文件名未带上范围: %q", cd)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("导出不应被缓存: %q", cc)
	}

	// 指定范围 → 只取区间内。
	_, narrow := doExport(t, h, "?from=2026-03-01&to=2026-03-31")
	if narrow.Meta.Count != 2 || len(narrow.Events) != 2 {
		t.Fatalf("3 月应 2 条: count=%d len=%d", narrow.Meta.Count, len(narrow.Events))
	}

	// 起止写反自动交换，与 /events/counts 同口径。
	_, swapped := doExport(t, h, "?from=2026-03-31&to=2026-01-01")
	if swapped.Meta.Count != 3 {
		t.Fatalf("范围颠倒应自动交换: %+v", swapped.Meta)
	}
}

func TestExportRejectsBadParamsBeforeFirstByte(t *testing.T) {
	srv, st := newTestServer(t, nil)
	seedExport(t, st, "huozhi", "h1", "2026-01-05", 1200, nil)
	h := srv.Handler()

	rec, _ := doExport(t, h, "?from=2026-1-5")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "YYYY-MM-DD") {
		t.Fatalf("非 YYYY-MM-DD 应 400 并给格式示例: %d %s", rec.Code, rec.Body.String())
	}

	// 形态合法但不存在的日期（正则放过、日历不存在）：这类若放过，产出的是空文件，
	// 调用方会误读成"这段时间没有数据"。
	for _, bogus := range []string{"2026-13-45", "2026-02-30", "2026-10-32"} {
		rec, _ = doExport(t, h, "?from="+bogus)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "有效日期") {
			t.Fatalf("%s 应 400 并说明是有效日期问题: %d %s", bogus, rec.Code, rec.Body.String())
		}
	}

	// 未支持的 format 必须拒收，而不是静默按 json 发出去。
	rec, _ = doExport(t, h, "?format=csv")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "json") {
		t.Fatalf("format=csv 应 400 并说明只支持 json: %d %s", rec.Code, rec.Body.String())
	}
}

func TestExportStripsProxyPathKeepsSourceRef(t *testing.T) {
	srv, st := newTestServer(t, nil)
	seedExport(t, st, "qingye", "dia:3", "2026-10-16", 0,
		[]model.MediaRef{{ID: "1792120109510945390_DDen7iOg.jpg", Kind: "photo", Width: 1280, Height: 960}})

	_, out := doExport(t, srv.Handler(), "")
	ev := out.Events[0]
	if len(ev.Media) != 1 {
		t.Fatalf("媒体应保留: %+v", ev.Media)
	}
	if ev.Media[0].ProxyPath != "" {
		t.Fatalf("导出不应带观复自己的代理路径: %q", ev.Media[0].ProxyPath)
	}
	if ev.Media[0].ID != "1792120109510945390_DDen7iOg.jpg" || ev.Media[0].Kind != "photo" {
		t.Fatalf("源侧引用丢了: %+v", ev.Media[0])
	}
	if !strings.Contains(out.Meta.Note, "uploads") {
		t.Fatalf("meta.note 应说明媒体引用怎么再取: %q", out.Meta.Note)
	}
}

func TestExportEmptyStore(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	_, out := doExport(t, srv.Handler(), "")
	if out.Meta.Count != 0 {
		t.Fatalf("空库 count 应为 0: %+v", out.Meta)
	}
	// 空结果必须是 []，不能是 null——下游脚本直接 .length 会炸。
	if out.Events == nil {
		t.Fatal("events 不能是 null")
	}
}

// TestExportLargeRangeNotCapped 时间轴的 1000 条上限不得变成导出的天花板。
func TestExportLargeRangeNotCapped(t *testing.T) {
	srv, st := newTestServer(t, nil)
	for i := 0; i < 1050; i++ {
		day := time.Date(2026, 1, 1, 0, 0, 0, 0, model.LocalLocation()).AddDate(0, 0, i/40)
		date := day.Format("2006-01-02")
		seedExport(t, st, "huozhi", fmt.Sprintf("x%d", i), date, 1, nil)
	}
	_, out := doExport(t, srv.Handler(), "")
	if len(out.Events) != 1050 || out.Meta.Count != 1050 {
		t.Fatalf("1050 条应全部导出: len=%d count=%d", len(out.Events), out.Meta.Count)
	}
}
