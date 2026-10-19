// Package notify 负责把观复的事件推送到外部通知渠道。
//
// 目前两个渠道：
//
//	unraid  —— Unraid 7 的 GraphQL `createNotification`，进系统通知中心，
//	           再由 Unraid 自己的通知代理（邮件/Discord/…）分发；
//	apprise —— Apprise API（含 Apprise-Go）的 `/notify`，一次打给 N 个目标。
//
// 设计取舍：渠道只管「把一条消息发出去并如实回报结果」，不吞异常。
// 设置页的「测试发送」需要看到上游真实状态码与响应体，否则配错了只能靠猜。
package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
)

// Message 一条待发送的通知。
type Message struct {
	Title string
	Body  string
	// Priority 统一三档：info / warning / alert。各渠道自己映射到本地枚举。
	Priority string
}

// 优先级常量。
const (
	PriorityInfo    = "info"
	PriorityWarning = "warning"
	PriorityAlert   = "alert"
)

// Result 一次发送的如实回报（测试发送与自动通知共用）。
type Result struct {
	Channel    string `json:"channel"`
	OK         bool   `json:"ok"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	// Response 上游响应体（截断）。成功时可能是 {"success":true}，
	// 失败时往往是真正有用的那句错误。
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Channel 单个通知渠道。
type Channel interface {
	Name() string
	Send(ctx context.Context, m Message) (Result, error)
}

// maxBody 上游响应体的读取上限：够看清错误，不会被巨型 HTML 拖死。
const maxBody = 8 << 10

// truncate 压成单行并截断，便于塞进 JSON 与日志。
func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// Dispatcher 按「当前生效配置」把消息发到所有启用的渠道。
//
// cfg 用函数而不是快照：设置页保存后无需重建 Dispatcher，下一次发送自然用新配置。
type Dispatcher struct {
	cfg    func() config.NotifyConfig
	client *http.Client

	mu     sync.Mutex
	last   map[string]time.Time
	window time.Duration
}

// NewDispatcher 创建分发器。默认去重窗口 6 小时。
func NewDispatcher(cfg func() config.NotifyConfig) *Dispatcher {
	return &Dispatcher{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
		last:   map[string]time.Time{},
		window: 6 * time.Hour,
	}
}

// SetWindow 调整去重窗口（测试用）。
func (d *Dispatcher) SetWindow(w time.Duration) { d.window = w }

// Channel 按名字构造渠道（不做启用判断，供测试发送用）。
func (d *Dispatcher) Channel(name string) (Channel, error) {
	c := d.cfg().Channel(name)
	return build(c, d.client)
}

// Test 向指定渠道发一条固定的测试消息，返回上游真实结果。
func (d *Dispatcher) Test(ctx context.Context, name string) (Result, error) {
	ch, err := d.Channel(name)
	if err != nil {
		return Result{Channel: name, Error: err.Error()}, err
	}
	return ch.Send(ctx, Message{
		Title:    "观复 · 测试通知",
		Body:     "如果你看到这条消息，说明观复的通知渠道配置可用。",
		Priority: PriorityInfo,
	})
}

// Send 向所有启用的渠道发送。单个渠道失败不影响其他渠道，错误聚合返回。
//
// dedupeKey 非空时按窗口去重（同一源反复失败不该刷屏）。
func (d *Dispatcher) Send(ctx context.Context, m Message, dedupeKey string) ([]Result, error) {
	if dedupeKey != "" && !d.allow(dedupeKey) {
		return nil, nil
	}

	channels := d.cfg().EnabledChannels()
	if len(channels) == 0 {
		return nil, errors.New("没有启用的通知渠道")
	}

	var (
		results []Result
		errs    []error
	)
	for _, name := range channels {
		ch, err := d.Channel(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			results = append(results, Result{Channel: name, Error: err.Error()})
			continue
		}
		res, err := ch.Send(ctx, m)
		results = append(results, res)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return results, errors.Join(errs...)
}

// allow 判断该去重键是否已过窗口；窗口内直接拦掉。
func (d *Dispatcher) allow(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if t, ok := d.last[key]; ok && now.Sub(t) < d.window {
		return false
	}
	d.last[key] = now
	return true
}

// build 按渠道配置构造具体实现。
func build(c config.NotifyChannel, client *http.Client) (Channel, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("%s 未配置地址", c.Name)
	}
	switch c.Name {
	case config.NotifyUnraid:
		return newUnraid(c, client), nil
	case config.NotifyApprise:
		return newApprise(c, client), nil
	default:
		return nil, fmt.Errorf("未知通知渠道 %q", c.Name)
	}
}

// send 执行一次 HTTP 往返并把状态码/耗时/响应体填进 Result。
func send(client *http.Client, req *http.Request) (Result, []byte, error) {
	start := time.Now()
	resp, err := client.Do(req)
	res := Result{DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		res.Error = err.Error()
		return res, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	res.Status = resp.StatusCode
	res.Response = truncate(body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, res.Response)
		res.Error = err.Error()
		return res, body, err
	}
	res.OK = true
	return res, body, nil
}
