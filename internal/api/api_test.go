package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
	"github.com/Felix2yu/guanfu/internal/secret"
	"github.com/Felix2yu/guanfu/internal/settings"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/Felix2yu/guanfu/internal/sync"
)

func newTestServer(t *testing.T, sources map[string]config.SourceConfig) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.FromPassphrase("test-secret")
	if err != nil {
		t.Fatalf("初始化加密器失败: %v", err)
	}
	mgr, err := settings.NewManager(st, &config.Config{Sources: sources}, box)
	if err != nil {
		t.Fatalf("初始化配置层失败: %v", err)
	}
	engine := sync.NewEngine(st, sync.NewRegistry(), func(string) bool { return false })
	return NewServer(st, engine, mgr), st
}

func seedEvent(t *testing.T, st *store.Store, source, extID, date string, media []model.MediaRef) {
	t.Helper()
	day, _ := time.ParseInLocation("2006-01-02", date, model.LocalLocation())
	ev := &model.Event{
		Source: source, ExternalID: extID, EventType: model.EventVideoWatch,
		TimePrecision: model.PrecisionSecond,
		OccurredAt:    day.Add(10 * time.Hour).UTC().Unix(),
		LocalDate:     date, Title: "t", Media: media,
	}
	if _, err := st.UpsertEvent(ev); err != nil {
		t.Fatalf("种子事件失败: %v", err)
	}
}

// ---------- counts 范围模式（热力图数据源） ----------

func TestCountsRangeMode(t *testing.T) {
	srv, st := newTestServer(t, nil)
	seedEvent(t, st, "bili-history", "a", "2026-10-01", nil)
	seedEvent(t, st, "bili-history", "b", "2026-10-01", nil)
	seedEvent(t, st, "huozhi", "c", "2026-10-03", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/events/counts?from=2026-10-01&to=2026-10-31", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	var resp struct {
		Code int            `json:"code"`
		Data map[string]int `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if resp.Data["2026-10-01"] != 2 || resp.Data["2026-10-03"] != 1 {
		t.Fatalf("范围计数异常: %v", resp.Data)
	}
	if _, ok := resp.Data["2026-10-02"]; ok {
		t.Fatal("无数据日期不应出现")
	}
}

func TestCountsRangeModeValidation(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	for _, q := range []string{"from=2026-10-01", "to=2026-10-31", ""} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/events/counts?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("q=%q 期望 400，实际 %d", q, rec.Code)
		}
	}
}

// ---------- 事件输出：ProxyPath 填充 ----------

func TestEventsFillProxyPath(t *testing.T) {
	sources := map[string]config.SourceConfig{
		"mujian":       {BaseURL: "http://mujian.test"},
		"bili-history": {BaseURL: "http://bili.test"},
	}
	srv, st := newTestServer(t, sources)
	seedEvent(t, st, "mujian", "m1", "2026-10-09", []model.MediaRef{
		{Kind: "photo", ID: "covers/abc.avif"},
		{Kind: "photo", ID: "../../etc/passwd"}, // 穿越必须被拒
	})
	seedEvent(t, st, "bili-history", "b1", "2026-10-09", []model.MediaRef{
		{Kind: "cover", ID: "https://i0.hdslb.com/cover.jpg"},
		{Kind: "cover", ID: "https://evil.com/x.jpg"}, // 非 B 站域必须被拒
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/events?date=2026-10-09", nil))
	var resp struct {
		Data []*model.Event `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	bySrc := map[string]*model.Event{}
	for _, e := range resp.Data {
		bySrc[e.Source] = e
	}
	m := bySrc["mujian"]
	if m == nil || len(m.Media) != 2 {
		t.Fatalf("mujian 事件异常: %+v", m)
	}
	if m.Media[0].ProxyPath != "/api/v1/media/mujian/covers/abc.avif" {
		t.Fatalf("mujian 代理路径异常: %q", m.Media[0].ProxyPath)
	}
	if m.Media[1].ProxyPath != "" {
		t.Fatalf("路径穿越未被拒: %q", m.Media[1].ProxyPath)
	}
	b := bySrc["bili-history"]
	if b == nil || len(b.Media) != 2 {
		t.Fatalf("bili 事件异常: %+v", b)
	}
	if b.Media[0].ProxyPath != "/api/v1/media/bili-history?u=https%3A%2F%2Fi0.hdslb.com%2Fcover.jpg" {
		t.Fatalf("bili 代理路径异常: %q", b.Media[0].ProxyPath)
	}
	if b.Media[1].ProxyPath != "" {
		t.Fatalf("非 B 站域未被拒: %q", b.Media[1].ProxyPath)
	}
}

// ---------- 媒体代理端到端 ----------

func TestMediaProxyMujianStreamsUploads(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/uploads/covers/abc.avif" {
			t.Errorf("回源路径异常: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/avif")
		_, _ = w.Write([]byte("fake-avif-bytes"))
	}))
	defer upstream.Close()

	sources := map[string]config.SourceConfig{"mujian": {BaseURL: upstream.URL}}
	srv, _ := newTestServer(t, sources)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/mujian/covers/abc.avif", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/avif" {
		t.Fatalf("Content-Type 异常: %q", ct)
	}
	if body := rec.Body.String(); body != "fake-avif-bytes" {
		t.Fatalf("媒体内容异常: %q", body)
	}
}

func TestMediaProxyRejectsTraversal(t *testing.T) {
	sources := map[string]config.SourceConfig{"mujian": {BaseURL: "http://mujian.test"}}
	srv, _ := newTestServer(t, sources)
	for _, p := range []string{
		"/api/v1/media/mujian/..%2F..%2Fetc%2Fpasswd",
		"/api/v1/media/mujian/covers/..%2Fsecret",
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code == http.StatusOK {
			t.Fatalf("路径穿越未被拒: %s -> %d", p, rec.Code)
		}
	}
}

func TestMediaProxyBiliRejectsNonHdslb(t *testing.T) {
	sources := map[string]config.SourceConfig{"bili-history": {BaseURL: "http://bili.test"}}
	srv, _ := newTestServer(t, sources)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/media/bili-history?u=https%3A%2F%2Fevil.com%2Fx.jpg", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非 B 站域应 400，实际 %d", rec.Code)
	}
}

func TestMediaProxyUnconfiguredSource404(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/mujian/covers/a.avif", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未配置源应 404，实际 %d", rec.Code)
	}
}

// ---------- ProxyPathFor 单元 ----------

func TestProxyPathFor(t *testing.T) {
	cases := []struct {
		source, id, want string
	}{
		{"mujian", "covers/a.avif", "/api/v1/media/mujian/covers/a.avif"},
		{"mujian", "covers/../a", ""},
		{"mujian", "/etc/passwd", ""},
		{"bili-history", "https://i0.hdslb.com/a.jpg", "/api/v1/media/bili-history?u=https%3A%2F%2Fi0.hdslb.com%2Fa.jpg"},
		{"bili-history", "http://i0.hdslb.com/a.jpg", ""},        // 必须 https
		{"bili-history", "https://sub.hdslb.com.evil.com/x", ""}, // 后缀伪装
		{"zhichang", "whatever", ""},                             // 无代理分支
		// immich：UUID 才放行（防路径穿越/注入）。
		{"immich", "3b37059b-8e63-4d93-a254-7e02dd6a4d29", "/api/v1/media/immich/3b37059b-8e63-4d93-a254-7e02dd6a4d29"},
		{"immich", "../../etc/passwd", ""},
		{"immich", "not-a-uuid", ""},
		{"immich", "3b37059b8e634d93a2547e02dd6a4d29", ""}, // 缺连字符
	}
	for _, c := range cases {
		if got := ProxyPathFor(c.source, c.id); got != c.want {
			t.Errorf("ProxyPathFor(%q,%q) = %q, want %q", c.source, c.id, got, c.want)
		}
	}
}

// ---------- immich 媒体代理 ----------

func TestMediaProxyImmichThumbnailAndOriginal(t *testing.T) {
	var gotPaths []string
	var gotKeys []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.RequestURI())
		gotKeys = append(gotKeys, r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-jpeg"))
	}))
	defer upstream.Close()

	sources := map[string]config.SourceConfig{"immich": {BaseURL: upstream.URL, APIKey: "im-key"}}
	srv, _ := newTestServer(t, sources)
	id := "3b37059b-8e63-4d93-a254-7e02dd6a4d29"

	// 缺省 → thumbnail?size=thumbnail。
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/immich/"+id, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "fake-jpeg" {
		t.Fatalf("缺省缩略图异常: %d %q", rec.Code, rec.Body.String())
	}
	// preview / original 尺寸路由。
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/immich/"+id+"?size=preview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview 异常: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/immich/"+id+"?size=original", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("original 异常: %d", rec.Code)
	}
	wantPaths := []string{
		"/api/assets/" + id + "/thumbnail?size=thumbnail",
		"/api/assets/" + id + "/thumbnail?size=preview",
		"/api/assets/" + id + "/original",
	}
	for i, want := range wantPaths {
		if gotPaths[i] != want {
			t.Fatalf("回源路径异常: got %q want %q", gotPaths[i], want)
		}
		if gotKeys[i] != "im-key" {
			t.Fatalf("x-api-key 未透传: %q", gotKeys[i])
		}
	}
}

func TestMediaProxyImmichValidation(t *testing.T) {
	sources := map[string]config.SourceConfig{"immich": {BaseURL: "http://immich.test", APIKey: "k"}}
	srv, _ := newTestServer(t, sources)
	// 非法 assetID。
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/immich/..%2F..%2Fetc%2Fpasswd", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 ID 应 400，实际 %d", rec.Code)
	}
	// 非法 size。
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/immich/3b37059b-8e63-4d93-a254-7e02dd6a4d29?size=evil", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 size 应 400，实际 %d", rec.Code)
	}
	// 未配置（缺 key 同样不可用）。
	srv2, _ := newTestServer(t, nil)
	rec = httptest.NewRecorder()
	srv2.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/immich/3b37059b-8e63-4d93-a254-7e02dd6a4d29", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未配置应 404，实际 %d", rec.Code)
	}
}

// 事件输出时 immich 媒体引用也应填 ProxyPath。
func TestEventsFillProxyPathImmich(t *testing.T) {
	sources := map[string]config.SourceConfig{"immich": {BaseURL: "http://immich.test", APIKey: "k"}}
	srv, st := newTestServer(t, sources)
	id := "3b37059b-8e63-4d93-a254-7e02dd6a4d29"
	seedEvent(t, st, "immich", "2026-10-15", "2026-10-15", []model.MediaRef{{Kind: "photo", ID: id}})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/events?date=2026-10-15", nil))
	var resp struct {
		Data []*model.Event `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Media) != 1 {
		t.Fatalf("事件异常: %+v", resp.Data)
	}
	if got := resp.Data[0].Media[0].ProxyPath; got != "/api/v1/media/immich/"+id {
		t.Fatalf("immich 代理路径异常: %q", got)
	}
}

// ---------- overview（总览页聚合） ----------

func TestOverviewEndpoint(t *testing.T) {
	srv, st := newTestServer(t, nil)
	seed := func(id, source, date string, cents int64, detailType string) {
		t.Helper()
		ev := &model.Event{
			Source: source, ExternalID: id, EventType: model.EventTransaction,
			TimePrecision: model.PrecisionSecond, OccurredAt: 1_760_000_000,
			LocalDate: date, Title: id, AmountCents: cents, Currency: "CNY",
		}
		if detailType != "" {
			ev.Detail = map[string]any{"type": detailType}
		}
		if _, err := st.UpsertEvent(ev); err != nil {
			t.Fatalf("种子失败: %v", err)
		}
	}
	seed("a", "huozhi", "2026-10-02", 3000, "expense")
	seed("b", "huozhi", "2026-10-02", 900000, "income")
	seed("c", "shenshi", "2026-10-03", 0, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/overview?from=2026-10-01&to=2026-10-31", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int            `json:"code"`
		Data store.Overview `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if resp.Code != 0 {
		t.Fatalf("code 期望 0，实际 %d", resp.Code)
	}
	got := resp.Data
	if got.TotalCount != 3 || len(got.Days) != 2 || len(got.Sources) != 2 {
		t.Fatalf("聚合结果不符: %+v", got)
	}
	if cny := got.Totals["CNY"]; cny.IncomeCents != 900000 || cny.ExpenseCents != 3000 {
		t.Fatalf("合计有误: %+v", cny)
	}
	// 来源分布按条数倒序：huozhi 两条在前。
	if got.Sources[0].Key != "huozhi" || got.Sources[0].Count != 2 {
		t.Fatalf("来源分布有误: %+v", got.Sources)
	}
}

func TestOverviewRejectsBadParams(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	for _, q := range []string{"", "?from=2026-10-01", "?from=x&to=y"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/overview"+q, nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q 期望 400，实际 %d", q, rec.Code)
		}
	}
}
