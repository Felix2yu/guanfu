package config

import (
	"net/http"
	"strings"
	"testing"
)

func reqWithAuth(t *testing.T, cfg SourceConfig) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://example.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyAuth(req); err != nil {
		t.Fatalf("ApplyAuth: %v", err)
	}
	return req
}

func TestApplyAuthDefaultsPerSource(t *testing.T) {
	cases := []struct {
		name   string
		header string
		value  string
	}{
		{"huozhi", "X-API-Key", "k"},
		{"immich", "x-api-key", "k"},
		{"mujian", "X-Auth-Token", "k"},
		{"qiansi", "Authorization", "Bearer k"},
		{"shenshi", "Authorization", "Bearer k"},
		{"dawarich", "Authorization", "Bearer k"},
		{"diarum", "Authorization", "Bearer k"},
	}
	for _, tc := range cases {
		req := reqWithAuth(t, SourceConfig{Name: tc.name, APIKey: "k"})
		if got := req.Header.Get(tc.header); got != tc.value {
			t.Errorf("%s 默认认证头 %s = %q，期望 %q", tc.name, tc.header, got, tc.value)
		}
	}
}

func TestApplyAuthNoKeySendsNothing(t *testing.T) {
	for _, name := range []string{"huozhi", "qiansi"} {
		req := reqWithAuth(t, SourceConfig{Name: name})
		if got := req.Header.Get("Authorization"); got != "" {
			t.Errorf("%s 未配凭据不该发 Authorization：%q", name, got)
		}
		if got := req.Header.Get("X-API-Key"); got != "" {
			t.Errorf("%s 未配凭据不该发 X-API-Key：%q", name, got)
		}
	}
}

func TestApplyAuthOverrideMode(t *testing.T) {
	// huozhi 默认走 X-API-Key，改成 bearer 后必须换成 Authorization。
	req := reqWithAuth(t, SourceConfig{Name: "huozhi", APIKey: "tok", Auth: AuthBearer})
	if req.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("bearer 覆盖失败: %v", req.Header)
	}
	if req.Header.Get("X-API-Key") != "" {
		t.Fatal("改为 bearer 后不该再发 X-API-Key")
	}

	// qiansi 默认 Bearer，改成 api_key 后落到默认头 X-API-Key。
	req = reqWithAuth(t, SourceConfig{Name: "qiansi", APIKey: "tok", Auth: AuthAPIKey})
	if req.Header.Get("X-API-Key") != "tok" || req.Header.Get("Authorization") != "" {
		t.Fatalf("api_key 覆盖失败: %v", req.Header)
	}

	// none 必须真的不发。
	req = reqWithAuth(t, SourceConfig{Name: "huozhi", APIKey: "tok", Auth: AuthNone})
	if len(req.Header) != 0 {
		t.Fatalf("none 不该发任何认证头: %v", req.Header)
	}
}

func TestApplyAuthBasic(t *testing.T) {
	req := reqWithAuth(t, SourceConfig{Name: "qiansi", APIKey: "user:pass", Auth: AuthBasic})
	// base64("user:pass") = dXNlcjpwYXNz
	if got := req.Header.Get("Authorization"); got != "Basic dXNlcjpwYXNz" {
		t.Fatalf("Basic 编码错误: %q", got)
	}

	// 已经是 base64 的直传，不二次编码。
	req = reqWithAuth(t, SourceConfig{Name: "qiansi", APIKey: "dXNlcjpwYXNz", Auth: AuthBasic})
	if got := req.Header.Get("Authorization"); got != "Basic dXNlcjpwYXNz" {
		t.Fatalf("base64 直传被改写: %q", got)
	}

	// 既不含冒号也不是合法 base64：必须报错而不是发出坏头。
	cfg := SourceConfig{Name: "qiansi", APIKey: "not base64!!", Auth: AuthBasic}
	req2, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	if err := cfg.ApplyAuth(req2); err == nil {
		t.Fatal("非法 Basic 凭据应报错")
	}
}

func TestAuthModeFallsBackOnInvalid(t *testing.T) {
	cfg := SourceConfig{Name: "huozhi", Auth: "oauth2-weird"}
	if got := cfg.AuthMode(); got != AuthAPIKey {
		t.Fatalf("非法认证方式应回落到源默认，得到 %q", got)
	}
}

func TestNotifyEnvFallback(t *testing.T) {
	t.Setenv("APPRISE_BASE_URL", "http://apprise/")
	t.Setenv("APPRISE_API_KEY", "a-key")
	t.Setenv("APPRISE_KEY", "family")
	t.Setenv("APPRISE_ENABLED", "")

	n := loadNotify()
	a := n.Channel(NotifyApprise)
	if a.BaseURL != "http://apprise" {
		t.Fatalf("末尾斜杠应被裁掉: %q", a.BaseURL)
	}
	if !a.Enabled {
		t.Fatal("配了地址应默认启用")
	}
	if a.Token != "a-key" {
		t.Fatalf("token 未读到: %q", a.Token)
	}
	if got := n.EnabledChannels(); len(got) != 1 || got[0] != NotifyApprise {
		t.Fatalf("启用渠道列表不符: %v", got)
	}
	if a.Key != "family" {
		t.Fatalf("apprise key 未读到: %q", a.Key)
	}

	// 显式关闭优先于「配了地址即启用」。
	t.Setenv("APPRISE_ENABLED", "0")
	if loadNotify().Channel(NotifyApprise).Enabled {
		t.Fatal("APPRISE_ENABLED=0 应强制关闭")
	}
}

func TestAPIKeyHeaderUnknownSourceFallsBack(t *testing.T) {
	if got := APIKeyHeader("weird-source"); !strings.EqualFold(got, "X-API-Key") {
		t.Fatalf("未知源应兜底 X-API-Key，得到 %q", got)
	}
}
