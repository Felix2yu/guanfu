// 观复（guanfu）——个人数据聚合应用：在同一时间轴按天聚合多源个人数据。
//
// 架构与数据源接入方案见 docs/观复规划.md。
// 配置来自环境变量，也可在设置页里按端点覆盖（页面优先，见 internal/settings）；
// 页面写入的凭据以 AES-GCM 密文落库，密钥取 GUANFU_SECRET_KEY 或同目录 .key 文件。
//
// 用法：
//
//	guanfu            # HTTP 服务（时间轴 + API + /mcp）
//	guanfu mcp        # 只读 MCP server，stdio 传输（供本地 AI 客户端直连）
//	guanfu backup     # 向 stdout 导出 SQLite 一致性快照（供外部备份工具 docker exec 调用）
//	guanfu -version   # 打印版本
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/Felix2yu/guanfu/internal/secret"
	"github.com/Felix2yu/guanfu/internal/settings"
	"github.com/Felix2yu/guanfu/internal/sources"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/Felix2yu/guanfu/internal/sync"
	"github.com/Felix2yu/guanfu/internal/web"
)

// MCPPath 只读 MCP server 的挂载路径，与全家桶其他仓库（货殖/幕间/吾身/知常）一致。
const MCPPath = "/mcp"

func main() {
	// 外部备份工具（docker-db-auto-backup）经 docker exec 调用 `guanfu backup`，
	// 把一致性快照写到 stdout。必须拦在最前面——早于 flag.Parse、存储初始化、
	// 迁移、同步引擎与 HTTP：导出进程只做导出这一件事。
	// stdout 只承载快照字节，日志一律留在 stderr（Go log 默认即 stderr）。
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		if err := runBackup(config.Load().DBPath, os.Stdout); err != nil {
			log.Fatalf("备份导出失败: %v", err)
		}
		return
	}

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

	// 凭据加密器：页面写入的 token 以密文落库（见 internal/secret）。
	// 密钥优先取 GUANFU_SECRET_KEY，否则用库文件同目录的 <db>.key（自动生成 0600）。
	box, err := secret.Load(cfg.DBPath + ".key")
	if err != nil {
		log.Fatalf("初始化凭据加密失败: %v", err)
	}

	// 配置层：env 基线 + 页面覆盖（页面优先，按端点整块生效）。
	mgr, err := settings.NewManager(st, cfg, box)
	if err != nil {
		log.Fatalf("载入配置失败: %v", err)
	}

	// 适配器是配置快照：配置一变就整批重建（不是热改字段）。
	registryFor := func(srcs map[string]config.SourceConfig) *sync.Registry {
		reg := sync.NewRegistry()
		reg.Register(sources.NewHuozhiSource(srcs["huozhi"]))
		reg.Register(sources.NewMujianSource(srcs["mujian"]))
		reg.Register(sources.NewDiarumSource(srcs["diarum"]))
		reg.Register(sources.NewZhichangSource(srcs["zhichang"]))
		reg.Register(sources.NewDawarichSource(srcs["dawarich"]))
		reg.Register(sources.NewImmichSource(srcs["immich"]))
		reg.Register(sources.NewBiliHistorySource(srcs["bili-history"]))
		reg.Register(sources.NewQiansiSource(srcs["qiansi"]))
		reg.Register(sources.NewShenshiSource(srcs["shenshi"]))
		reg.Register(sources.NewLiuxiaSource(srcs["liuxia"]))
		reg.Register(sources.NewQingyeSource(srcs["qingye"]))
		return reg
	}
	registry := registryFor(mgr.Sources())

	// 启用判定：配置了 BaseURL 即参与调度；各适配器自行校验额外必需项
	// （如 key），缺失时以 last_error 形式暴露在 /api/v1/sources，不静默跳过。
	// 读的是「当前生效配置」，所以页面保存后无需重建这个函数。
	isActive := func(name string) bool {
		sc, ok := mgr.Sources()[name]
		return ok && sc.Enabled && sc.BaseURL != ""
	}
	engine := sync.NewEngine(st, registry, isActive)

	// 页面保存配置后：重建适配器 + 重启这批调度 goroutine，进程不重启。
	// 重建本身不追加同步，被改动的源由 API 层单独 Trigger（见 settings.handleSaveEndpoint）。
	mgr.OnApply(func(c *config.Config) {
		engine.Reconfigure(registryFor(c.Sources))
		log.Printf("[settings] 配置已生效：重建 %d 个源适配器并按新配置调度", len(c.Sources))
	})

	// 同步由正常转失败时推送（开关在设置页，默认关闭；窗口内去重）。
	engine.SetOnFailure(func(name string, cause error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		mgr.NotifySyncFailure(ctx, name, cause)
	})

	apiServer := api.NewServer(st, engine, mgr)

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
		eff := mgr.Sources()
		active := make([]string, 0, 12)
		for _, a := range registry.All() {
			if _, ok := eff[a.Name()]; !ok {
				// 注册了适配器但 config 没登记这个源名：isActive 恒为 false，源静默不跑。
				log.Printf("警告：%s 已注册但没有对应的 env 配置项（internal/config 的源清单漏登记），不会参与调度", a.Name())
				continue
			}
			if isActive(a.Name()) {
				active = append(active, fmt.Sprintf("%s(%s)", a.Name(), a.Interval()))
			}
		}
		if len(active) == 0 {
			log.Printf("同步引擎已启动，但没有任何启用的源（在设置页或 env 里配地址）")
		} else {
			log.Printf("同步引擎已启动：%s", strings.Join(active, " "))
		}
	} else {
		log.Printf("同步引擎已禁用（GUANFU_SYNC_ENABLED=0）")
	}

	// 配置来源与通知渠道各报一句：出问题时先看这两行能省很多排查。
	log.Printf("凭据加密密钥来源: %s（页面保存的 token 以密文落库）", box.Source())
	if chans := mgr.Effective().Notify.EnabledChannels(); len(chans) > 0 {
		log.Printf("通知渠道已启用: %s（同步失败推送 %s）", strings.Join(chans, " "), onOff(mgr.Policy().NotifyOnSyncFailure))
	} else {
		log.Printf("通知渠道未启用（可在设置页配置 Apprise）")
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

// onOff 把布尔开关转成日志里好扫的中文。
func onOff(v bool) string {
	if v {
		return "已开启"
	}
	return "未开启"
}

// runBackup 实现 `guanfu backup`：以不迁移、不建档的方式打开数据库，// 把 VACUUM INTO 的事务级一致快照写到 w（由调用方给 os.Stdout）。
// 库路径复用 config 的解析（GUANFU_DB），备份工具无需额外配置。
func runBackup(dbPath string, w io.Writer) error {
	st, err := store.OpenForExport(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.WriteSQLiteSnapshot(w)
}
