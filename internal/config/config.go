// Package config 负责从环境变量加载观复运行配置。
// 所有凭据只从 env 读取，不写文件、不进仓库（见 README 安全约定）。
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 观复运行时配置。
type Config struct {
	// HTTP 监听地址，默认 :8788。
	ListenAddr string
	// SQLite 数据文件路径，默认 ./data/guanfu.db。
	DBPath string
	// 本地时区名，用于 local_date 归日，默认 Asia/Shanghai。
	Timezone string
	// 静态前端目录（磁盘优先于内嵌产物），默认 ./web/dist。
	StaticDir string
	// 同步引擎总开关，默认开。
	SyncEnabled bool
	// MCPEnabled 只读 MCP server 开关（挂在 /mcp），默认开。
	MCPEnabled bool
	// MCPToken 非空时 /mcp 要求 Authorization: Bearer <token>；stdio 不需要。
	MCPToken string

	// 各数据源配置。缺省时对应源标记为未配置（enabled=false 且无凭据），
	// 同步会记录 last_error 而不是崩溃。
	Sources map[string]SourceConfig
}

// SourceConfig 单个数据源的连接配置。
type SourceConfig struct {
	BaseURL string
	APIKey  string
	// Enabled 显式开关。规则：*_ENABLED=1 强制开；=0 强制关；
	// 未设置时「配了 BaseURL 即自动启用」（避免配了地址却忘记开开关的静默失败）。
	Enabled bool
	// Interval 覆盖该源默认同步间隔；0 表示用适配器默认值。
	Interval time.Duration
	// 增量游标缺失时的默认回溯天数（各源在 Load 里给默认值）。
	LookbackDays int
	// 仅 zhichang 使用：逗号分隔的指标名列表，空表示用适配器默认核心指标。
	Metrics string
}

// Load 从环境变量构建配置。
func Load() *Config {
	cfg := &Config{
		ListenAddr:  getenv("GUANFU_LISTEN", ":8788"),
		DBPath:      getenv("GUANFU_DB", "./data/guanfu.db"),
		Timezone:    getenv("GUANFU_TZ", "Asia/Shanghai"),
		StaticDir:   getenv("GUANFU_STATIC_DIR", "./web/dist"),
		SyncEnabled: getenv("GUANFU_SYNC_ENABLED", "1") != "0",
		MCPEnabled:  getenv("GUANFU_MCP_ENABLED", "1") != "0",
		MCPToken:    os.Getenv("GUANFU_MCP_TOKEN"),
		Sources:     map[string]SourceConfig{},
	}
	// 各源默认回溯天数（首次无 cursor 时的回溯窗口）。
	lookback := map[string]int{
		"huozhi":       90,  // 交易流水：季度级回溯够用
		"mujian":       365, // 演出记录：手工录入，历史价值高
		"diarum":       365, // 日记：一天一篇，历史价值高（与 mujian 同级）
		"zhichang":     30,  // 健康：数据密集，一月起步
		"dawarich":     30,  // 位置：数据密集
		"bili-history": 90,  // 观看历史
		"immich":       365, // 照片：按天聚合，回溯一年
		"qiansi":       180, // 人情往来：手工录入，半年回溯
		"shenshi":      90,  // 任务/习惯/复盘：季度回溯
		"liuxia":       30,  // 天象预报：时效性数据，一月足够
	}
	cfg.Sources["huozhi"] = SourceConfig{
		BaseURL:      strings.TrimRight(os.Getenv("HUOZHI_BASE_URL"), "/"),
		APIKey:       os.Getenv("HUOZHI_API_KEY"),
		Enabled:      sourceEnabled("HUOZHI", os.Getenv("HUOZHI_BASE_URL") != ""),
		Interval:     getenvDuration("HUOZHI_INTERVAL", 30*time.Minute),
		LookbackDays: getenvInt("HUOZHI_LOOKBACK_DAYS", lookback["huozhi"]),
	}
	// 其余源按同样模式注册；未配置 BaseURL 的源不参与调度，也不会在状态里报错。
	// M4 增值源：qiansi（人情往来）/ shenshi（任务复盘）/ liuxia（霞光预报）。
	for _, name := range []string{
		"mujian", "diarum", "zhichang", "dawarich", "bili-history", "immich",
		"qiansi", "shenshi", "liuxia",
	} {
		upper := strings.ToUpper(name)
		base := strings.TrimRight(os.Getenv(upper+"_BASE_URL"), "/")
		key := os.Getenv(upper + "_API_KEY")
		cfg.Sources[name] = SourceConfig{
			BaseURL:      base,
			APIKey:       key,
			Enabled:      sourceEnabled(upper, base != ""),
			Interval:     getenvDuration(upper+"_INTERVAL", 0),
			LookbackDays: getenvInt(upper+"_LOOKBACK_DAYS", lookback[name]),
			Metrics:      os.Getenv(upper + "_METRICS"),
		}
	}
	return cfg
}

// sourceEnabled 三态开关：显式 1 开、显式 0 关、未设置按 configured 推断。
func sourceEnabled(upper string, configured bool) bool {
	switch os.Getenv(upper + "_ENABLED") {
	case "1":
		return true
	case "0":
		return false
	default:
		return configured
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
