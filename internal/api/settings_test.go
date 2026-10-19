package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/settings"
)

func doJSON(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

type settingsPayload struct {
	Code int `json:"code"`
	Data struct {
		Endpoints    []settings.EndpointView `json:"endpoints"`
		Policy       settings.Policy         `json:"policy"`
		SecretSource string                  `json:"secret_source"`
		AuthModes    []string                `json:"auth_modes"`
	} `json:"data"`
}

func getSettings(t *testing.T, srv *Server) settingsPayload {
	t.Helper()
	rec := doJSON(t, srv, "GET", "/api/v1/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var p settingsPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return p
}

func TestSettingsPayloadHasNoSecrets(t *testing.T) {
	srv, _ := newTestServer(t, map[string]config.SourceConfig{
		"huozhi": {Name: "huozhi", BaseURL: "https://h.example.com", APIKey: "plain-secret-key", Enabled: true},
	})
	p := getSettings(t, srv)

	if len(p.Data.Endpoints) != 3 { // 1 个源 + 2 个通知渠道
		t.Fatalf("端点数量不符: %d", len(p.Data.Endpoints))
	}
	if len(p.Data.AuthModes) == 0 {
		t.Fatal("应下发认证方式清单")
	}
	if p.Data.SecretSource == "" {
		t.Fatal("应说明密钥来源")
	}
	raw := mustJSON(t, p)
	if strings.Contains(raw, "plain-secret-key") {
		t.Fatal("设置接口泄漏了凭据明文")
	}
	var h settings.EndpointView
	for _, e := range p.Data.Endpoints {
		if e.Name == "huozhi" {
			h = e
		}
	}
	if h.Origin != settings.OriginEnv || !h.SecretSet || h.SecretHint != "········-key" {
		t.Fatalf("huozhi 视图不符: %+v", h)
	}
	if h.DefaultAuth != config.AuthAPIKey {
		t.Fatalf("默认认证方式应来自源定义: %q", h.DefaultAuth)
	}
}

func TestSaveEndpointOverridesAndReflectsInSources(t *testing.T) {
	srv, _ := newTestServer(t, map[string]config.SourceConfig{
		"huozhi": {Name: "huozhi", BaseURL: "https://env.example.com", APIKey: "env-key", Enabled: true},
	})

	rec := doJSON(t, srv, "PUT", "/api/v1/settings/endpoints/huozhi", `{
		"base_url": "http://page.example.com:9000",
		"auth": "bearer",
		"secret": "page-token",
		"enabled": true,
		"interval_secs": 600
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存失败 HTTP %d: %s", rec.Code, rec.Body.String())
	}

	// 页面视图：来源变 page，凭据打码。
	p := getSettings(t, srv)
	for _, e := range p.Data.Endpoints {
		if e.Name == "huozhi" {
			if e.Origin != settings.OriginPage || e.BaseURL != "http://page.example.com:9000" ||
				e.Auth != config.AuthBearer || e.IntervalSecs != 600 {
				t.Fatalf("视图未反映保存结果: %+v", e)
			}
			if e.SecretHint != "········oken" {
				t.Fatalf("掩码不符: %q", e.SecretHint)
			}
		}
	}

	// /api/v1/sources 必须用生效配置（否则总览/状态卡还在显示 env 地址）。
	rec = doJSON(t, srv, "GET", "/api/v1/sources", "")
	var sources struct {
		Data []sourceView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sources); err != nil {
		t.Fatal(err)
	}
	var h sourceView
	for _, s := range sources.Data {
		if s.Name == "huozhi" {
			h = s
		}
	}
	if h.BaseURL != "http://page.example.com:9000" || h.Auth != config.AuthBearer {
		t.Fatalf("/api/v1/sources 未用生效配置: %+v", h)
	}
	if strings.Contains(rec.Body.String(), "page-token") {
		t.Fatal("sources 接口泄漏了凭据")
	}
}

func TestSaveEndpointValidation(t *testing.T) {
	srv, _ := newTestServer(t, map[string]config.SourceConfig{"huozhi": {Name: "huozhi"}})

	if rec := doJSON(t, srv, "PUT", "/api/v1/settings/endpoints/nope", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("未知端点应 404，得到 %d", rec.Code)
	}
	if rec := doJSON(t, srv, "PUT", "/api/v1/settings/endpoints/huozhi", `{"base_url":"ftp://x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法地址应 400，得到 %d", rec.Code)
	}
	if rec := doJSON(t, srv, "PUT", "/api/v1/settings/endpoints/huozhi", `{oops}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400，得到 %d", rec.Code)
	}
}

func TestResetEndpointReturnsToEnv(t *testing.T) {
	srv, _ := newTestServer(t, map[string]config.SourceConfig{
		"huozhi": {Name: "huozhi", BaseURL: "https://env.example.com", APIKey: "k", Enabled: true},
	})
	if rec := doJSON(t, srv, "PUT", "/api/v1/settings/endpoints/huozhi", `{"base_url":"http://page","enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("保存失败: %d", rec.Code)
	}
	rec := doJSON(t, srv, "DELETE", "/api/v1/settings/endpoints/huozhi", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("重置失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Removed  bool                  `json:"removed"`
			Endpoint settings.EndpointView `json:"endpoint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Data.Removed || resp.Data.Endpoint.Origin != settings.OriginEnv ||
		resp.Data.Endpoint.BaseURL != "https://env.example.com" {
		t.Fatalf("重置结果不符: %+v", resp.Data)
	}
}

func TestVerifyEndpointProbe(t *testing.T) {
	var gotKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		if gotKey != "probe-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, map[string]config.SourceConfig{"huozhi": {Name: "huozhi"}})

	// 草稿探测（未保存）：地址与凭据都由请求体给。
	rec := doJSON(t, srv, "POST", "/api/v1/settings/endpoints/huozhi/verify",
		`{"base_url":"`+upstream.URL+`","auth":"api_key","secret":"probe-key"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("探测接口应 200: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data settings.ProbeResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Data.OK || resp.Data.Status != 200 {
		t.Fatalf("探测应成功: %+v", resp.Data)
	}
	if gotKey != "probe-key" {
		t.Fatalf("探测未按认证方式发头: %q", gotKey)
	}
	if !strings.Contains(resp.Data.Message, "认证通过") {
		t.Fatalf("结论文案不符: %q", resp.Data.Message)
	}

	// 错误凭据：仍然 200，但 ok=false 且结论点明认证被拒。
	rec = doJSON(t, srv, "POST", "/api/v1/settings/endpoints/huozhi/verify",
		`{"base_url":"`+upstream.URL+`","auth":"api_key","secret":"wrong"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("探测失败也应是 200（结果在 body 里）: %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.OK || !strings.Contains(resp.Data.Message, "认证被拒绝") {
		t.Fatalf("应判为认证失败: %+v", resp.Data)
	}

	// 不通的地址：ok=false，且不该抛 5xx。
	rec = doJSON(t, srv, "POST", "/api/v1/settings/endpoints/huozhi/verify",
		`{"base_url":"http://127.0.0.1:9/none"}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != http.StatusOK || resp.Data.OK {
		t.Fatalf("不可达地址应 200 + ok=false: %d %+v", rec.Code, resp.Data)
	}
}

func TestTestNotifyEndpoint(t *testing.T) {
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"data":{"createNotification":{"id":"1"}}}`))
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, nil)
	// 先保存 unraid 渠道配置。
	rec := doJSON(t, srv, "PUT", "/api/v1/settings/endpoints/unraid", `{
		"base_url": "`+upstream.URL+`", "auth": "api_key",
		"secret": "u-key", "enabled": true
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存渠道失败: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, srv, "POST", "/api/v1/settings/notify/unraid/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("测试发送应 200: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Channel string `json:"channel"`
			OK      bool   `json:"ok"`
			Status  int    `json:"status"`
			Error   string `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Data.OK || resp.Data.Status != 200 {
		t.Fatalf("测试发送结果不符: %+v", resp.Data)
	}
	if path != "/graphql" {
		t.Fatalf("应打到 /graphql，实际 %q", path)
	}

	// 未知渠道：404 而不是 500。
	if rec := doJSON(t, srv, "POST", "/api/v1/settings/notify/nope/test", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("未知渠道应 404，得到 %d", rec.Code)
	}
}

func TestSavePolicyEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	rec := doJSON(t, srv, "PUT", "/api/v1/settings/policy", `{"notify_on_sync_failure":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存策略失败: %d %s", rec.Code, rec.Body.String())
	}
	if !getSettings(t, srv).Data.Policy.NotifyOnSyncFailure {
		t.Fatal("策略未生效")
	}
	// 关掉也要能存（不能用"零值 = 不处理"的写法）。
	rec = doJSON(t, srv, "PUT", "/api/v1/settings/policy", `{"notify_on_sync_failure":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("关闭策略失败: %d", rec.Code)
	}
	if getSettings(t, srv).Data.Policy.NotifyOnSyncFailure {
		t.Fatal("关闭未生效")
	}
}

func TestSecretNilKeepsCredentialThroughAPI(t *testing.T) {
	srv, _ := newTestServer(t, map[string]config.SourceConfig{"huozhi": {Name: "huozhi"}})
	if rec := doJSON(t, srv, "PUT", "/api/v1/settings/endpoints/huozhi",
		`{"base_url":"http://a","secret":"first-token","enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("首次保存失败: %d", rec.Code)
	}
	// 不带 secret 字段：凭据必须保留。
	if rec := doJSON(t, srv, "PUT", "/api/v1/settings/endpoints/huozhi",
		`{"base_url":"http://b","enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("二次保存失败: %d", rec.Code)
	}
	p := getSettings(t, srv)
	for _, e := range p.Data.Endpoints {
		if e.Name == "huozhi" && (!e.SecretSet || e.BaseURL != "http://b") {
			t.Fatalf("凭据被误清: %+v", e)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
