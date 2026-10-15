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

	// 各数据源配置。缺省时对应源标记为未配置（enabled=false 且无凭据），
	// 同步会记录 last_error 而不是崩溃。
	Sources map[string]SourceConfig
}

// SourceConfig 单个数据源的连接配置。
type SourceConfig struct {
	BaseURL string
	APIKey  string
	// Enabled 显式开关；未设置时按「有 BaseURL + APIKey」推断。
	Enabled bool
	// Interval 覆盖该源默认同步间隔；0 表示用适配器默认值。
	Interval time.Duration
	// 仅 huozhi 使用：增量游标缺失时的默认回溯天数。
	LookbackDays int
}

// Load 从环境变量构建配置。
func Load() *Config {
	cfg := &Config{
		ListenAddr:  getenv("GUANFU_LISTEN", ":8788"),
		DBPath:      getenv("GUANFU_DB", "./data/guanfu.db"),
		Timezone:    getenv("GUANFU_TZ", "Asia/Shanghai"),
		StaticDir:   getenv("GUANFU_STATIC_DIR", "./web/dist"),
		SyncEnabled: getenv("GUANFU_SYNC_ENABLED", "1") != "0",
		Sources:     map[string]SourceConfig{},
	}
	cfg.Sources["huozhi"] = SourceConfig{
		BaseURL:      strings.TrimRight(os.Getenv("HUOZHI_BASE_URL"), "/"),
		APIKey:       os.Getenv("HUOZHI_API_KEY"),
		Enabled:      os.Getenv("HUOZHI_ENABLED") != "0",
		Interval:     getenvDuration("HUOZHI_INTERVAL", 30*time.Minute),
		LookbackDays: getenvInt("HUOZHI_LOOKBACK_DAYS", 90),
	}
	// 其余源（mujian/diarum/zhichang/dawarich/bili-history/immich）在 M2/M3
	// 落地时按同样模式追加；未配置的源不会进入调度。
	for _, name := range []string{"mujian", "diarum", "zhichang", "dawarich", "bili-history", "immich"} {
		base := strings.TrimRight(os.Getenv(strings.ToUpper(name)+"_BASE_URL"), "/")
		key := os.Getenv(strings.ToUpper(name) + "_API_KEY")
		cfg.Sources[name] = SourceConfig{
			BaseURL:  base,
			APIKey:   key,
			Enabled:  os.Getenv(strings.ToUpper(name)+"_ENABLED") == "1",
			Interval: getenvDuration(strings.ToUpper(name)+"_INTERVAL", 0),
		}
	}
	return cfg
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
