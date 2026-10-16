package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Felix2yu/guanfu/internal/config"
)

// TestQingyeProxyPathRules 代理路径生成的白名单。
// qingye 的 /uploads/ 是扁平命名（实测 `<雪花号>_<随机>.jpg`），比 mujian 的
// covers/<hash>.<ext> 少一层，所以这里连斜杠都不放行。
func TestQingyeProxyPathRules(t *testing.T) {
	cases := []struct {
		id, want string
	}{
		{"1792120109510945390_DDen7iOg.jpg", "/api/v1/media/qingye/1792120109510945390_DDen7iOg.jpg"},
		{"a_b-c.d.JPG", "/api/v1/media/qingye/a_b-c.d.JPG"},
		{"../../etc/passwd", ""},                // 穿越
		{"covers/a.jpg", ""},                    // 多一层目录（mujian 形态，不该出现在这里）
		{"/uploads/a.jpg", ""},                  // 没剥前缀
		{".hidden", ""},                         // 必须以字母数字开头
		{"a b.jpg", ""},                         // 空格
		{"a.jpg?x=1", ""},                       // 查询串注入
		{"", ""},                                // 空
		{strings.Repeat("a", 200) + ".jpg", ""}, // 超长
	}
	for _, c := range cases {
		if got := ProxyPathFor("qingye", c.id); got != c.want {
			t.Fatalf("ProxyPathFor(qingye, %q) = %q，期望 %q", c.id, got, c.want)
		}
	}
}

func TestQingyeMediaProxyRoundTrip(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uploads/1792120109510945390_DDen7iOg.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("JPEGDATA"))
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}))
	defer origin.Close()

	srv, _ := newTestServer(t, map[string]config.SourceConfig{
		"qingye": {BaseURL: origin.URL},
	})
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/qingye/1792120109510945390_DDen7iOg.jpg", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "JPEGDATA" {
		t.Fatalf("代理回源异常: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("未带上游 Content-Type: %q", ct)
	}

	// 非法文件名必须在回源之前就拒掉（400），不能把拼接结果交给上游。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/qingye/%2e%2e%2f%2e%2e%2fetc%2fpasswd", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("编码穿越竟被代理成功: %s", rec.Body.String())
	}

	// 上游 404 时透传状态码，不要伪装成 200 空图。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/qingye/missing.jpg", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("上游缺失应透传 404，实际 %d", rec.Code)
	}
}

func TestQingyeMediaProxyUnconfigured(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/media/qingye/a_b.jpg", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "qingye 未配置") {
		t.Fatalf("未配置时应明确 404: code=%d body=%s", rec.Code, rec.Body.String())
	}
}
