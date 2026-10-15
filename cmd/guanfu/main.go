// 观复（guanfu）——个人数据聚合应用：在同一时间轴按天聚合多源个人数据。
//
// 架构与数据源接入方案见 docs/观复规划.md。
// 配置全部来自环境变量，凭据（HUOZHI_API_KEY 等）只放 env，不落仓库。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Felix2yu/guanfu/internal/api"
	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
	"github.com/Felix2yu/guanfu/internal/sources"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/Felix2yu/guanfu/internal/sync"
	"github.com/Felix2yu/guanfu/internal/web"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[guanfu] ")

	cfg := config.Load()
	model.SetTimezone(cfg.Timezone)

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer st.Close()

	registry := sync.NewRegistry()
	registry.Register(sources.NewHuozhiSource(cfg.Sources["huozhi"]))

	engine := sync.NewEngine(st, registry, func(name string) bool {
		sc, ok := cfg.Sources[name]
		return ok && sc.Enabled && sc.BaseURL != "" && sc.APIKey != ""
	})

	apiServer := api.NewServer(st, engine)

	// 前端托管：磁盘目录优先（不重编译可换前端），否则内嵌产物。
	handler := apiServer.Handler()
	if info, err := os.Stat(cfg.StaticDir); err == nil && info.IsDir() {
		handler = web.MountFrontend(apiServer.Handler(), http.Dir(cfg.StaticDir))
		log.Printf("前端托管: 磁盘目录 %s", cfg.StaticDir)
	} else if web.HasBuild() {
		handler = web.MountFrontend(apiServer.Handler(), http.FS(web.FS()))
		log.Printf("前端托管: 内嵌产物")
	} else {
		log.Printf("未检测到前端产物（%s 不存在且未内嵌），仅提供 API", cfg.StaticDir)
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 优雅停机：先停 HTTP 再取消同步 ctx。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.SyncEnabled {
		engine.Start(ctx)
		log.Printf("同步引擎已启动（huozhi 间隔 %s）", sources.NewHuozhiSource(cfg.Sources["huozhi"]).Interval())
	} else {
		log.Printf("同步引擎已禁用（GUANFU_SYNC_ENABLED=0）")
	}

	go func() {
		log.Printf("HTTP 服务监听 %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("收到停机信号，正在优雅退出…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP 停机超时: %v", err)
	}
	log.Printf("已退出")
}
