package web

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Go 内置扩展名表不认 .webmanifest，不注册就会被嗅探成 text/plain，
// 而浏览器对 manifest 的 MIME 是严格校验的：装不上，且只在 DevTools 留一行警告。
func TestWebmanifestMIME(t *testing.T) {
	if got := mime.TypeByExtension(".webmanifest"); got != "application/manifest+json" {
		t.Fatalf(".webmanifest 的 Content-Type = %q，期望 application/manifest+json", got)
	}
}

func TestHasBuild(t *testing.T) {
	if !HasBuild() {
		t.Skip("未构建前端产物（PLACEHOLDER 阶段），跳过内嵌测试")
	}
}

// 复现冒烟中发现的问题：经 http.FS(FS()) 打开内嵌资产路径失败，
// 导致 MountFrontend 把 .js 请求回退成 index.html。
func TestEmbeddedFSOpen(t *testing.T) {
	if !HasBuild() {
		t.Skip("未构建前端产物")
	}
	// 1) 底层 fs.FS 直接打开
	fsys := FS()
	f, err := fsys.Open("_app/immutable/entry")
	if err == nil {
		f.Close()
		t.Log("fs.FS 可打开 _app/immutable/entry 目录")
	} else {
		t.Logf("fs.FS 打开目录失败: %v", err)
	}

	// 2) http.FS 打开带前导斜杠的资产路径（MountFrontend 的用法）
	hfs := http.FS(fsys)
	for _, p := range []string{
		"/index.html",
		"/_app/immutable/entry",
		"/_app/immutable/chunks",
	} {
		f, err := hfs.Open(p)
		if err != nil {
			t.Errorf("http.FS.Open(%q) 失败: %v", p, err)
			continue
		}
		st, _ := f.Stat()
		t.Logf("http.FS.Open(%q) -> %s isDir=%v", p, st.Name(), st.IsDir())
		f.Close()
	}
}

// MountFrontend 对真实资产必须返回原文件而非 index.html。
func TestMountFrontendServesAssets(t *testing.T) {
	if !HasBuild() {
		t.Skip("未构建前端产物")
	}
	apiCalled := false
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		w.WriteHeader(http.StatusTeapot)
	})
	h := MountFrontend(api, http.FS(FS()))

	// 资产路径（任取一个真实 chunk）
	entries, err := os.ReadDir(filepath.Join("dist", "_app", "immutable", "chunks"))
	if err != nil || len(entries) == 0 {
		t.Skip("无 chunks 产物")
	}
	assetPath := "/_app/immutable/chunks/" + entries[0].Name()

	req, _ := http.NewRequest("GET", assetPath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("资产 %s 返回 %d", assetPath, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" || ct == "text/html; charset=utf-8" {
		t.Fatalf("资产 Content-Type 异常: %q（疑似被回退成 index.html）", ct)
	}
	if apiCalled {
		t.Fatal("资产请求误入 API handler")
	}

	// SPA 深链必须回退 index.html 且 200。
	req, _ = http.NewRequest("GET", "/some/deep/route", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("SPA 回退返回 %d", rec.Code)
	}
	if body := rec.Body.String(); len(body) == 0 || body[:9] != "<!doctype" {
		t.Fatalf("SPA 回退内容不是 index.html")
	}

	// /api 前缀必须走 API。
	req, _ = http.NewRequest("GET", "/api/v1/health", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("/api 未走 API handler: %d", rec.Code)
	}
}
