package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/secret"
	"github.com/Felix2yu/guanfu/internal/settings"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/Felix2yu/guanfu/internal/sync"
)

// newWriteServer 建一个带补录 token 的 API 服务（huozhi 已登记为已知源）。
func newWriteServer(t *testing.T, ingestToken string) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/w.db")
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.FromPassphrase("test-secret")
	if err != nil {
		t.Fatalf("初始化加密器失败: %v", err)
	}
	sources := map[string]config.SourceConfig{
		"huozhi": {Name: "huozhi", BaseURL: "http://127.0.0.1:1", Enabled: false},
	}
	mgr, err := settings.NewManager(st, &config.Config{Sources: sources}, box)
	if err != nil {
		t.Fatalf("初始化配置层失败: %v", err)
	}
	engine := sync.NewEngine(st, sync.NewRegistry(), func(string) bool { return false })
	return NewServer(st, engine, mgr, ingestToken), st
}

func postJSON(t *testing.T, srv *Server, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestIngestOpenByDefault(t *testing.T) {
	// 未配置 token：与 /api/v1/import 同级的内网管理面，无需凭据即可用
	//（页面「补一笔」表单依赖这一点，浏览器侧没有 token 可带）。
	srv, _ := newWriteServer(t, "")
	rec := postJSON(t, srv, "/api/v1/ingest/event", "", map[string]any{
		"external_id": "n0", "date": "2026-10-26", "title": "无 token 也可补录",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("未配置 token 应开放（200），实际 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIngestEventRoundTrip(t *testing.T) {
	srv, st := newWriteServer(t, "tok-1")

	// 无凭据 → 401。
	rec := postJSON(t, srv, "/api/v1/ingest/event", "", map[string]any{
		"external_id": "n1", "date": "2026-10-26",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺凭据应 401，实际 %d", rec.Code)
	}
	// 坏日期 → 400。
	rec = postJSON(t, srv, "/api/v1/ingest/event", "tok-1", map[string]any{
		"external_id": "n1", "date": "2026-13-45",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法日期应 400，实际 %d", rec.Code)
	}
	// 缺幂等键 → 400。
	rec = postJSON(t, srv, "/api/v1/ingest/event", "tok-1", map[string]any{
		"date": "2026-10-26",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 external_id 应 400，实际 %d", rec.Code)
	}

	// 全天事件：type 默认 note、标题兜底、金额补默认币种。
	rec = postJSON(t, srv, "/api/v1/ingest/event", "tok-1", map[string]any{
		"external_id": "note-1", "date": "2026-10-26", "amount_cents": 1500,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("补录应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Changed bool `json:"changed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Data.Changed {
		t.Fatal("首次写入 changed 应为 true")
	}
	list, err := st.ListEvents(store.EventQuery{Date: "2026-10-26"})
	if err != nil || len(list) != 1 {
		t.Fatalf("补录未落库: %v, %d 条", err, len(list))
	}
	got := list[0]
	if got.Source != "ingest" || got.EventType != "note" ||
		got.TimePrecision != "day" || got.Title != "补录" || got.Currency != "CNY" {
		t.Fatalf("补录事件字段异常: %+v", got)
	}

	// 带 HH:MM → second 精度。
	rec = postJSON(t, srv, "/api/v1/ingest/event", "tok-1", map[string]any{
		"external_id": "note-2", "date": "2026-10-26", "time": "14:30", "title": "下午的事",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("带时刻补录应 200，实际 %d", rec.Code)
	}
	list, _ = st.ListEvents(store.EventQuery{Date: "2026-10-26", EventType: "note"})
	for _, e := range list {
		if e.ExternalID == "note-2" && e.TimePrecision != "second" {
			t.Fatalf("带时刻应为 second 精度: %+v", e)
		}
	}

	// 同幂等键重放 = 更新，changed=false（真变更检测）。
	rec = postJSON(t, srv, "/api/v1/ingest/event", "tok-1", map[string]any{
		"external_id": "note-1", "date": "2026-10-26", "amount_cents": 1500,
	})
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.Changed {
		t.Fatal("同内容重放 changed 应为 false")
	}
}

func TestImportUpserts(t *testing.T) {
	srv, st := newWriteServer(t, "")

	payload := map[string]any{
		"meta": map[string]any{"count": 4},
		"events": []map[string]any{
			{"source": "huozhi", "external_id": "b1", "event_type": "transaction",
				"occurred_at": 1_790_000_000, "local_date": "2026-09-28", "title": "旧账",
				"amount_cents": 9900, "currency": "CNY", "detail": map[string]any{"type": "expense"}},
			{"source": "ingest", "external_id": "n1", "event_type": "note",
				"local_date": "2026-10-26", "title": "手写补录"}, // 无 occurred_at → 归日补中午
			{"source": "no-such-source", "external_id": "x", "local_date": "2026-10-26"}, // 未知源 → 跳过
			{"source": "huozhi", "local_date": "2026-10-26"},                              // 缺幂等键 → 跳过
		},
	}
	rec := postJSON(t, srv, "/api/v1/import", "", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("导入应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Total   int `json:"total"`
			Changed int `json:"changed"`
			Skipped []map[string]string `json:"skipped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Total != 4 || resp.Data.Changed != 2 || len(resp.Data.Skipped) != 2 {
		t.Fatalf("导入统计异常: %+v", resp.Data)
	}

	// 无 occurred_at 的记录被归日补中午，日期可用。
	list, _ := st.ListEvents(store.EventQuery{Date: "2026-10-26", Source: "ingest"})
	if len(list) != 1 || list[0].OccurredAt == 0 {
		t.Fatalf("导入补录记录异常: %+v", list)
	}

	// 重放导入：内容未变 → changed=0（幂等，不虚报）。
	rec = postJSON(t, srv, "/api/v1/import", "", payload)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.Changed != 0 {
		t.Fatalf("同内容重放导入 changed 应为 0，实际 %d", resp.Data.Changed)
	}

	// 非 export 形态（缺 events）→ 400。
	rec = postJSON(t, srv, "/api/v1/import", "", map[string]any{"foo": 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 events 应 400，实际 %d", rec.Code)
	}
}
