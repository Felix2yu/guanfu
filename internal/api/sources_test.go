package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
)

func TestSourcesPayloadShape(t *testing.T) {
	srv, st := newTestServer(t, map[string]config.SourceConfig{
		"huozhi":   {BaseURL: "https://huozhi.example.com", APIKey: "secret-key-abc", Enabled: true, Interval: 30 * time.Minute},
		"zhichang": {BaseURL: "http://127.0.0.1:18788", Enabled: true},
		"qiansi":   {BaseURL: "http://127.0.0.1:8080", Enabled: true},
		"immich":   {Enabled: true}, // 没填端点：应 configured=false
	})
	// 库里有一条配置里没有的源（改过 env 后遗留），也必须列出来。
	if err := st.SaveSourceState("dawarich", false, 1800, "2026-10-01", 1792120000, "缺 key", 0); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/sources", nil))
	var resp struct {
		Code int          `json:"code"`
		Data []sourceView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	by := map[string]sourceView{}
	for _, v := range resp.Data {
		by[v.Name] = v
	}
	if len(resp.Data) != 5 {
		t.Fatalf("应列出 4 个配置源 + 1 个只在库里的源，实际 %d: %v", len(resp.Data), resp.Data)
	}
	// 名称升序（设置页顺序稳定）
	for i := 1; i < len(resp.Data); i++ {
		if resp.Data[i-1].Name > resp.Data[i].Name {
			t.Fatalf("未按名称升序: %v", resp.Data)
		}
	}

	h := by["huozhi"]
	if h.Auth != config.AuthAPIKey || h.CredentialEnv != "HUOZHI_API_KEY" || h.BaseURLEnv != "HUOZHI_BASE_URL" {
		t.Fatalf("huozhi 认证元信息有误: %+v", h)
	}
	if !h.CredentialSet || !h.Configured || !h.Enabled || h.BaseURL != "https://huozhi.example.com" {
		t.Fatalf("huozhi 配置状态有误: %+v", h)
	}
	if h.IntervalSecs != 1800 {
		t.Fatalf("没同步过的源应按配置给间隔，实际 %d", h.IntervalSecs)
	}
	if z := by["zhichang"]; z.Auth != config.AuthNone || !z.Configured || z.CredentialSet {
		t.Fatalf("零鉴权源应 configured 且不需要凭据: %+v", z)
	}
	if q := by["qiansi"]; !q.Configured {
		t.Fatalf("token 可选的源不该被标成未配好: %+v", q)
	}
	if im := by["immich"]; im.Configured {
		t.Fatalf("没填端点的源必须 configured=false: %+v", im)
	}
	d := by["dawarich"]
	if d.Enabled || d.LastError != "缺 key" || d.Auth != config.AuthBearer {
		t.Fatalf("仅存在于库里的源应带同步状态: %+v", d)
	}
	// 凭据值绝不出现在响应里。
	if strings.Contains(rec.Body.String(), "secret-key-abc") {
		t.Fatal("响应泄漏了 API key 明文")
	}
}
