package config

import (
	"os"
	"strings"
)

// 通知渠道名。与 endpoint_config 里的 name 一致，前端按名字找。
const (
	NotifyApprise = "apprise"
)

// NotifyChannel 单个通知渠道的环境变量配置（可被页面配置整块覆盖）。
type NotifyChannel struct {
	Name    string
	BaseURL string
	// Auth 认证方式，取值同数据源。Apprise 默认 none（多数自建实例不带鉴权）。
	Auth    string
	Token   string
	Enabled bool

	// Key Apprise 的配置键（POST /notify/{key}）。为空时退化到
	// POST /notify + urls 列表，两种都是 Apprise API 的正式用法。
	Key string
	// Targets Apprise 的目标 URL 列表（空格/逗号/换行分隔），
	// 仅在 Key 为空时随请求体下发。
	Targets string
}

// NotifyConfig 通知渠道集合。
type NotifyConfig struct {
	Channels map[string]NotifyChannel
}

// Channel 取渠道配置（不存在时返回零值，Enabled=false）。
func (n NotifyConfig) Channel(name string) NotifyChannel {
	if n.Channels == nil {
		return NotifyChannel{Name: name}
	}
	c, ok := n.Channels[name]
	if !ok {
		return NotifyChannel{Name: name}
	}
	if c.Name == "" {
		c.Name = name
	}
	return c
}

// EnabledChannels 返回已启用的渠道名（供启动日志与调度判断）。
func (n NotifyConfig) EnabledChannels() []string {
	out := make([]string, 0, len(n.Channels))
	for _, name := range []string{NotifyApprise} {
		if c := n.Channel(name); c.Enabled && c.BaseURL != "" {
			out = append(out, name)
		}
	}
	return out
}

// loadNotify 从 env 读通知渠道。未配 BaseURL 的渠道视为未配置且不启用，
// 与数据源同一套「配了地址即自动启用，可用 *_ENABLED=0 显式关」规则。
func loadNotify() NotifyConfig {
	n := NotifyConfig{Channels: map[string]NotifyChannel{}}

	appriseBase := strings.TrimRight(os.Getenv("APPRISE_BASE_URL"), "/")
	n.Channels[NotifyApprise] = NotifyChannel{
		Name:    NotifyApprise,
		BaseURL: appriseBase,
		Auth:    strings.ToLower(strings.TrimSpace(os.Getenv("APPRISE_AUTH"))),
		Token:   os.Getenv("APPRISE_API_KEY"),
		Enabled: notifyEnabled("APPRISE", appriseBase != ""),
		Key:     os.Getenv("APPRISE_KEY"),
		Targets: os.Getenv("APPRISE_TARGETS"),
	}
	return n
}

// notifyEnabled 三态开关，规则与数据源一致。
func notifyEnabled(upper string, configured bool) bool {
	switch os.Getenv(upper + "_ENABLED") {
	case "1":
		return true
	case "0":
		return false
	default:
		return configured
	}
}
