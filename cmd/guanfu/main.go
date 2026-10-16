// 观复（guanfu）——个人数据聚合应用：在同一时间轴按天聚合多源个人数据。
//
// 架构与数据源接入方案见 docs/观复规划.md。
// 配置全部来自环境变量，凭据（HUOZHI_API_KEY 等）只放 env，不落仓库。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	showVersion := flag.Bool("version", false, "打印版本并退出")
	flag.Parse()
	if *showVersion {
		fmt.Println("guanfu " + version)
		return
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[guanfu] ")
	log.Printf("观复 guanfu %s 启动", version)

	cfg := config.Load()
	model.SetTimezone(cfg.Timezone)

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer st.Close()

	registry := sync.NewRegistry()
	registry.Register(sources.NewHuozhiSource(cfg.Sources["huozhi"]))
	registry.Register(sources.NewMujianSource(cfg.Sources["mujian"]))
	registry.Register(sources.NewDiarumSource(cfg.Sources["diarum"]))
	registry.Register(sources.NewZhichangSource(cfg.Sources["zhichang"]))
	registry.Register(sources.NewDawarichSource(cfg.Sources["dawarich"]))
	registry.Register(sources.NewImmichSource(cfg.Sources["immich"]))
	registry.Register(sources.NewBiliHistorySource(cfg.Sources["bili-history"]))
	registry.Register(sources.NewQiansiSource(cfg.Sources["qiansi"]))
	registry.Register(sources.NewShenshiSource(cfg.Sources["shenshi"]))
	registry.Register(sources.NewLiuxiaSource(cfg.Sources["liuxia"]))

	// 启用判定：配置了 BaseURL 即参与调度；各适配器自行校验额外必需项
	// （如 key），缺失时以 last_error 形式暴露在 /api/v1/sources，不静默跳过。
	engine := sync.NewEngine(st, registry, func(name string) bool {
		sc, ok := cfg.Sources[name]
		return ok && sc.Enabled && sc.BaseURL != ""
	})

	apiServer := api.NewServer(st, engine, cfg.Sources)

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
		active := make([]string, 0, 10)
		for _, n := range []string{
			"huozhi", "mujian", "diarum", "zhichang", "dawarich", "immich", "bili-history",
			"qiansi", "shenshi", "liuxia",
		} {
			if sc := cfg.Sources[n]; sc.Enabled && sc.BaseURL != "" {
				if a, ok := registry.Get(n); ok {
					active = append(active, fmt.Sprintf("%s(%s)", n, a.Interval()))
				}
			}
		}
		log.Printf("同步引擎已启动：%s", strings.Join(active, " "))
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
