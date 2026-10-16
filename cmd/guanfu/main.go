// 观复（guanfu）——个人数据聚合应用：在同一时间轴按天聚合多源个人数据。
//
// 架构与数据源接入方案见 docs/观复规划.md。
// 配置全部来自环境变量，凭据（HUOZHI_API_KEY 等）只放 env，不落仓库。
//
// 用法：
//
//	guanfu            # HTTP 服务（时间轴 + API + /mcp）
//	guanfu mcp        # 只读 MCP server，stdio 传输（供本地 AI 客户端直连）
//	guanfu -version   # 打印版本
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
	guanfumcp "github.com/Felix2yu/guanfu/internal/mcp"
	"github.com/Felix2yu/guanfu/internal/model"
	"github.com/Felix2yu/guanfu/internal/sources"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/Felix2yu/guanfu/internal/sync"
	"github.com/Felix2yu/guanfu/internal/web"
)

// MCPPath 只读 MCP server 的挂载路径，与全家桶其他仓库（货殖/幕间/吾身/知常）一致。
const MCPPath = "/mcp"

func main() {
	showVersion := flag.Bool("version", false, "打印版本并退出")
	flag.Parse()
	if *showVersion {
		fmt.Println("guanfu " + version)
		return
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[guanfu] ")

	cfg := config.Load()
	model.SetTimezone(cfg.Timezone)

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer st.Close()

	// `guanfu mcp`：只跑 stdio 传输，不起 HTTP、不启同步引擎。
	// 日志必须留在 stderr（Go log 默认如此），任何 stdout 输出都会污染 JSON-RPC 帧。
	if flag.Arg(0) == "mcp" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		log.Printf("MCP stdio 服务已启动（库 %s），等待客户端连接", cfg.DBPath)
		if err := guanfumcp.ServeStdio(ctx, guanfumcp.NewServer(st, version)); err != nil && !errors.Is(err, ctx.Err()) {
			log.Fatalf("MCP stdio 异常退出: %v", err)
		}
		log.Printf("MCP stdio 已退出")
		return
	}

	log.Printf("观复 guanfu %s 启动", version)

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
	registry.Register(sources.NewQingyeSource(cfg.Sources["qingye"]))

	// 启用判定：配置了 BaseURL 即参与调度；各适配器自行校验额外必需项
	// （如 key），缺失时以 last_error 形式暴露在 /api/v1/sources，不静默跳过。
	// 这个判定被引擎调度和启动日志共用——两处口径必须一致，否则日志里"已启动"
	// 但实际没跑（或反之）会比报错更难查。
	isActive := func(name string) bool {
		sc, ok := cfg.Sources[name]
		return ok && sc.Enabled && sc.BaseURL != ""
	}
	engine := sync.NewEngine(st, registry, isActive)

	apiServer := api.NewServer(st, engine, cfg.Sources)

	// 前端托管：磁盘目录优先（不重编译可换前端），否则内嵌产物。
	var appHandler http.Handler = apiServer.Handler()
	if info, err := os.Stat(cfg.StaticDir); err == nil && info.IsDir() {
		appHandler = web.MountFrontend(appHandler, http.Dir(cfg.StaticDir))
		log.Printf("前端托管: 磁盘目录 %s", cfg.StaticDir)
	} else if web.HasBuild() {
		appHandler = web.MountFrontend(appHandler, http.FS(web.FS()))
		log.Printf("前端托管: 内嵌产物")
	} else {
		log.Printf("未检测到前端产物（%s 不存在且未内嵌），仅提供 API", cfg.StaticDir)
	}

	// MCP 挂在根 mux 上而不是塞进 MountFrontend：后者会把所有非 /api/ 路径
	// 回退成 SPA 的 index.html，/mcp 会被前端吞掉。
	root := http.NewServeMux()
	root.Handle("/", appHandler)
	if cfg.MCPEnabled {
		root.Handle(MCPPath, guanfumcp.HTTPHandler(guanfumcp.NewServer(st, version), cfg.MCPToken))
		if cfg.MCPToken == "" {
			log.Printf("MCP 只读服务: %s（未设 GUANFU_MCP_TOKEN，按内网只读放行）", MCPPath)
		} else {
			log.Printf("MCP 只读服务: %s（要求 Authorization: Bearer）", MCPPath)
		}
	} else {
		log.Printf("MCP 服务已禁用（GUANFU_MCP_ENABLED=0）")
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 优雅停机：先停 HTTP 再取消同步 ctx。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.SyncEnabled {
		engine.Start(ctx)
		// 名单直接来自注册表，不再手抄第二遍（抄两遍必漏抄一遍）。
		active := make([]string, 0, 12)
		for _, a := range registry.All() {
			if _, ok := cfg.Sources[a.Name()]; !ok {
				// 注册了适配器但 config 没登记这个源名：isActive 恒为 false，源静默不跑。
				log.Printf("警告：%s 已注册但没有对应的 env 配置项（internal/config 的源清单漏登记），不会参与调度", a.Name())
				continue
			}
			if isActive(a.Name()) {
				active = append(active, fmt.Sprintf("%s(%s)", a.Name(), a.Interval()))
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
