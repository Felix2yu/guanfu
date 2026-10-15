// Package web 提供前端产物托管：磁盘目录优先，否则 go:embed 内嵌产物。
// 与 huozhi/mujian 的 mountFrontend 范式一致：SPA 静态资源 + API 回退。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"strings"
)

// 构建脚本会把 SvelteKit 静态产物复制到 internal/web/dist 后再编译，
// 从而单二进制自带前端。未构建时该目录只有占位文件，HasBuild 返回 false。
//
// 注意必须用 all: 前缀：SvelteKit 的全部 JS/CSS 在 _app/ 目录下，
// 而 go:embed 默认排除 _ 和 . 开头的条目（经典陷阱）。
//
//go:embed all:dist
var embedded embed.FS

// HasBuild 判断是否内嵌了真实前端产物（dist 下存在 index.html）。
func HasBuild() bool {
	b, err := embedded.ReadFile("dist/index.html")
	return err == nil && len(b) > 0
}

// FS 返回内嵌产物的文件系统（调用方需先确认 HasBuild）。
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return embedded
	}
	return sub
}

// MountFrontend 把前端静态站挂在 handler 之前：
//   - /api/* 一律走 API；
//   - 已存在的静态文件直接服务；
//   - 其余路径回退 index.html（SPA history 路由）。
func MountFrontend(apiHandler http.Handler, staticFS http.FileSystem) http.Handler {
	fileServer := http.FileServer(staticFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		// 尝试打开静态文件；不存在则回退 SPA 入口。
		if f, err := staticFS.Open(r.URL.Path); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

// HasBuildDir 判断磁盘目录是否含真实产物（供 main 选择挂载源）。
func HasBuildDir(dir string) bool {
	st, err := os.Stat(dir + "/index.html")
	return err == nil && !st.IsDir()
}
