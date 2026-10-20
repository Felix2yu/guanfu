// Package settings 是「环境变量 + 页面配置」的合并层。
//
// 优先级（也是设置页里对用户的那句说明）：
//
//	页面配置  >  环境变量  >  内置默认
//
// 语义按**端点整块**生效，不做到字段级混搭：
//   - endpoint_config 里有该端点的行 → 地址/认证/凭据/启用/间隔全部以行为准，
//     行里留空就是真的清空（不会偷偷回退到 env）；
//   - 没有行 → 完全读环境变量，改 env 重启即生效；
//   - 「恢复环境变量」= 删除该行。
//
// 这样只有两种状态，用户不需要理解"哪一项来自哪边"；视图里的 origin 字段
// 也会如实告诉前端这一行现在由谁提供。
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/notify"
	"github.com/Felix2yu/guanfu/internal/secret"
	"github.com/Felix2yu/guanfu/internal/store"
)

// ErrUnknownEndpoint 端点不在白名单内（源清单与通知渠道之外的名字一律拒绝）。
var ErrUnknownEndpoint = errors.New("未知端点")

// 端点配置来源。
const (
	OriginPage = "page" // 由页面配置接管
	OriginEnv  = "env"  // 读环境变量
)

// Policy 应用级开关（不属于任何端点）。
type Policy struct {
	// NotifyOnSyncFailure 同步由正常转为失败时推送一条。默认关闭：
	// 通知是出站副作用，不能因为配了渠道就自动开始发。
	NotifyOnSyncFailure bool `json:"notify_on_sync_failure"`
}

// settingNotifyOnFailure app_setting 的键。
const settingNotifyOnFailure = "notify.on_sync_failure"

// Input 保存端点的请求体（PUT 语义：整体替换）。
type Input struct {
	BaseURL string `json:"base_url"`
	Auth    string `json:"auth"`
	// Secret nil = 保留原凭据；"" = 清空；其他 = 设为该值。
	// 三态是必要的：页面不该为了"不改密码"而被迫把明文回传一遍。
	Secret       *string           `json:"secret"`
	Enabled      bool              `json:"enabled"`
	IntervalSecs int               `json:"interval_secs"`
	Extras       map[string]string `json:"extras,omitempty"`
}

// EndpointView 下发给设置页的端点视图。永不包含明文凭据。
type EndpointView struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Origin 该端点当前由谁提供配置。
	Origin      string `json:"origin"`
	BaseURL     string `json:"base_url"`
	EnvBaseURL  string `json:"env_base_url"`
	Auth        string `json:"auth"`
	DefaultAuth string `json:"default_auth"`
	// SecretSet 是否已有可用凭据；SecretHint 是掩码（只露末 4 位）。
	SecretSet     bool              `json:"secret_set"`
	SecretHint    string            `json:"secret_hint"`
	SecretOrigin  string            `json:"secret_origin"`
	CredentialEnv string            `json:"credential_env"`
	Enabled       bool              `json:"enabled"`
	IntervalSecs  int               `json:"interval_secs"`
	Extras        map[string]string `json:"extras,omitempty"`
	// Error 配置本身有问题时的原因（如密文解不开），前端要显眼地提示。
	Error string `json:"error,omitempty"`
}

// Manager 配置合并与保存。
type Manager struct {
	st   *store.Store
	box  *secret.Box
	base *config.Config // env 基线，只读

	mu       sync.RWMutex
	eff      *config.Config
	rows     map[string]store.EndpointConfig
	problems map[string]string

	dispatch *notify.Dispatcher
	onApply  func(*config.Config)
}

// NewManager 载入页面配置并与 env 基线合并。
func NewManager(st *store.Store, base *config.Config, box *secret.Box) (*Manager, error) {
	m := &Manager{st: st, box: box, base: base}
	// Dispatcher 读的是「当前生效」的通知配置，保存后自动用新值。
	m.dispatch = notify.NewDispatcher(func() config.NotifyConfig { return m.Effective().Notify })
	if err := m.rebuild(); err != nil {
		return nil, err
	}
	return m, nil
}

// OnApply 注册「生效配置变化」回调（main 用它重建适配器与调度）。
func (m *Manager) OnApply(fn func(*config.Config)) { m.onApply = fn }

// Effective 返回当前生效配置的快照。返回的指针不会被就地修改，可安全并发读。
func (m *Manager) Effective() *config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.eff
}

// Sources 便捷方法：当前生效的源配置。
func (m *Manager) Sources() map[string]config.SourceConfig { return m.Effective().Sources }

// Dispatcher 暴露给 main 挂同步失败回调。
func (m *Manager) Dispatcher() *notify.Dispatcher { return m.dispatch }

// rebuild 从库里重读覆盖并重建生效配置（调用方需持写锁或处于构造期）。
func (m *Manager) rebuild() error {
	rows, err := m.st.ListEndpointConfigs("")
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	byName := make(map[string]store.EndpointConfig, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}
	problems := map[string]string{}

	eff := m.base.Clone()
	for name, sc := range eff.Sources {
		// 基线里若漏填 Name（手工构造配置、测试），补上：认证方式默认值与
		// Key 请求头都靠它解析。
		sc.Name = name
		row, ok := byName[name]
		if !ok || row.Kind != store.KindSource {
			continue
		}
		// 整块覆盖：行存在即接管，空值就是清空。
		sc.BaseURL = strings.TrimRight(row.BaseURL, "/")
		if config.IsValidAuth(row.Auth) {
			sc.Auth = row.Auth
		} else {
			sc.Auth = ""
		}
		sc.APIKey = ""
		if row.Secret != "" {
			plain, err := m.box.Open(row.Secret)
			if err != nil {
				// 解不开就当作没有凭据，并把原因显式暴露给页面——
				// 静默回退到 env 的旧密钥会让"页面已经改了"变成谎言。
				problems[name] = err.Error()
			} else {
				sc.APIKey = plain
			}
		}
		sc.Enabled = row.Enabled
		sc.Interval = time.Duration(row.IntervalSecs) * time.Second
		eff.Sources[name] = sc
	}

	// 渠道名是固定的：基线里没有也要补上，否则页面保存的渠道配置
	// 会被"生效配置里没这个键"吞掉（列表能看到、保存却没效果）。
	for _, name := range m.notifyNames() {
		if _, ok := eff.Notify.Channels[name]; !ok {
			eff.Notify.Channels[name] = config.NotifyChannel{Name: name}
		}
	}
	for name, ch := range eff.Notify.Channels {
		row, ok := byName[name]
		if !ok || row.Kind != store.KindNotify {
			continue
		}
		ch.BaseURL = strings.TrimRight(row.BaseURL, "/")
		if config.IsValidAuth(row.Auth) {
			ch.Auth = row.Auth
		} else {
			ch.Auth = ""
		}
		ch.Token = ""
		if row.Secret != "" {
			plain, err := m.box.Open(row.Secret)
			if err != nil {
				problems[name] = err.Error()
			} else {
				ch.Token = plain
			}
		}
		ch.Enabled = row.Enabled
		ex := decodeExtras(row.ExtrasJSON)
		ch.Key = ex["key"]
		ch.Targets = ex["targets"]
		eff.Notify.Channels[name] = ch
	}

	m.eff = eff
	m.rows = byName
	m.problems = problems
	return nil
}

// applyLocked 在写完库之后重建并通知外部。调用方不得持锁（rebuild 内部要拿写锁）。
func (m *Manager) apply() error {
	if err := m.rebuild(); err != nil {
		return err
	}
	if m.onApply != nil {
		m.onApply(m.Effective())
	}
	return nil
}

// sourceNames 返回源名（升序），保证列表顺序稳定。
func (m *Manager) sourceNames() []string {
	names := make([]string, 0, len(m.base.Sources))
	for n := range m.base.Sources {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (m *Manager) notifyNames() []string {
	return []string{config.NotifyApprise}
}

// kindOf 判断名字属于哪类端点；未知返回 false（挡住随意写入）。
func (m *Manager) kindOf(name string) (string, bool) {
	if _, ok := m.base.Sources[name]; ok {
		return store.KindSource, true
	}
	switch name {
	case config.NotifyApprise:
		return store.KindNotify, true
	}
	return "", false
}

// List 返回全部端点的可编辑视图（源 + 通知渠道）。
func (m *Manager) List() []EndpointView {
	m.mu.RLock()
	rows, problems := m.rows, m.problems
	m.mu.RUnlock()
	eff := m.Effective()

	out := make([]EndpointView, 0, len(eff.Sources)+2)
	for _, name := range m.sourceNames() {
		sc := eff.Sources[name]
		env := m.base.Sources[name]
		row, hasRow := rows[name]
		v := EndpointView{
			Name: name, Kind: store.KindSource,
			Origin:  OriginEnv,
			BaseURL: sc.BaseURL, EnvBaseURL: env.BaseURL,
			Auth: sc.AuthMode(), DefaultAuth: config.AuthKind(name),
			CredentialEnv: config.CredentialEnv(name),
			SecretSet:     sc.APIKey != "", SecretOrigin: OriginEnv,
			Enabled: sc.Enabled, IntervalSecs: int(sc.Interval.Seconds()),
			Error: problems[name],
		}
		if v.Auth == "" {
			v.Auth = config.AuthKind(name)
		}
		if sc.APIKey != "" {
			v.SecretHint = secret.Mask(sc.APIKey)
		}
		if hasRow && row.Kind == store.KindSource {
			v.Origin = OriginPage
			v.SecretOrigin = OriginPage
			if row.Secret == "" {
				v.SecretOrigin = ""
			}
		}
		out = append(out, v)
	}

	for _, name := range m.notifyNames() {
		ch := eff.Notify.Channel(name)
		row, hasRow := rows[name]
		v := EndpointView{
			Name: name, Kind: store.KindNotify,
			Origin:  OriginEnv,
			BaseURL: ch.BaseURL, EnvBaseURL: m.base.Notify.Channel(name).BaseURL,
			Auth: ch.Auth, DefaultAuth: defaultNotifyAuth(),
			CredentialEnv: strings.ToUpper(name) + "_API_KEY",
			SecretSet:     ch.Token != "", SecretOrigin: OriginEnv,
			Enabled: ch.Enabled,
			Extras:  map[string]string{"key": ch.Key, "targets": ch.Targets},
			Error:   problems[name],
		}
		if v.Auth == "" {
			v.Auth = defaultNotifyAuth()
		}
		if ch.Token != "" {
			v.SecretHint = secret.Mask(ch.Token)
		}
		if hasRow && row.Kind == store.KindNotify {
			v.Origin = OriginPage
			v.SecretOrigin = OriginPage
			if row.Secret == "" {
				v.SecretOrigin = ""
			}
		}
		out = append(out, v)
	}
	return out
}

func defaultNotifyAuth() string {
	return config.AuthNone
}

// View 取单个端点的视图。
func (m *Manager) View(name string) (EndpointView, bool) {
	for _, v := range m.List() {
		if v.Name == name {
			return v, true
		}
	}
	return EndpointView{}, false
}

// Save 保存端点配置（整体替换 + 可选换凭据），随后重建并触发生效回调。
func (m *Manager) Save(name string, in Input) (EndpointView, error) {
	kind, ok := m.kindOf(name)
	if !ok {
		return EndpointView{}, fmt.Errorf("%w %q", ErrUnknownEndpoint, name)
	}
	if err := validateBaseURL(in.BaseURL); err != nil {
		return EndpointView{}, err
	}
	if in.Auth != "" && !config.IsValidAuth(in.Auth) {
		return EndpointView{}, fmt.Errorf("认证方式 %q 不受支持", in.Auth)
	}
	if in.IntervalSecs < 0 || in.IntervalSecs > 30*86400 {
		return EndpointView{}, fmt.Errorf("同步间隔需在 0～30 天之间（秒）")
	}
	if kind == store.KindNotify {
		// 即使 extras 被整体清空也要校验：Apprise 没有 Key 又没有目标 URL 时，
		// 启用它只会在发送那一刻失败，不如保存时就挡住。
		if err := validateExtras(name, in.Extras, in.BaseURL); err != nil {
			return EndpointView{}, err
		}
	}

	old, err := m.st.GetEndpointConfig(name)
	if err != nil && !errors.Is(err, store.ErrNoEndpoint) {
		return EndpointView{}, err
	}

	row := store.EndpointConfig{Name: name, Kind: kind}
	if old != nil {
		row = *old
	}
	row.Kind = kind
	row.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	row.Auth = in.Auth
	row.Enabled = in.Enabled
	row.IntervalSecs = in.IntervalSecs
	if kind == store.KindNotify {
		row.ExtrasJSON = encodeExtras(in.Extras)
	}

	switch {
	case in.Secret == nil:
		// 保留原凭据（row.Secret 原样带过来）。
	case *in.Secret == "":
		row.Secret = ""
	default:
		sealed, err := m.box.Seal(*in.Secret)
		if err != nil {
			return EndpointView{}, err
		}
		row.Secret = sealed
	}

	if err := m.st.SaveEndpointConfig(row); err != nil {
		return EndpointView{}, err
	}
	if err := m.apply(); err != nil {
		return EndpointView{}, err
	}
	v, _ := m.View(name)
	return v, nil
}

// Reset 删除页面配置，回到环境变量。返回是否确有行被删。
func (m *Manager) Reset(name string) (bool, error) {
	if _, ok := m.kindOf(name); !ok {
		return false, fmt.Errorf("%w %q", ErrUnknownEndpoint, name)
	}
	ok, err := m.st.DeleteEndpointConfig(name)
	if err != nil {
		return false, err
	}
	if err := m.apply(); err != nil {
		return ok, err
	}
	return ok, nil
}

// Policy 读应用级开关。
func (m *Manager) Policy() Policy {
	v, err := m.st.GetSetting(settingNotifyOnFailure)
	if err != nil {
		log.Printf("[settings] 读取通知开关失败: %v", err)
	}
	return Policy{NotifyOnSyncFailure: v == "1"}
}

// SetPolicy 写应用级开关。
func (m *Manager) SetPolicy(p Policy) error {
	v := "0"
	if p.NotifyOnSyncFailure {
		v = "1"
	}
	return m.st.SetSetting(settingNotifyOnFailure, v)
}

// TestNotify 向指定渠道发测试消息，返回上游真实结果。
func (m *Manager) TestNotify(ctx context.Context, name string) (notify.Result, error) {
	if _, ok := m.kindOf(name); !ok {
		return notify.Result{Channel: name, Error: "未知渠道"}, fmt.Errorf("%w %q", ErrUnknownEndpoint, name)
	}
	return m.dispatch.Test(ctx, name)
}

// NotifySyncFailure 同步由正常转为失败时的推送入口。
//
// 去重键带源名：同一源在窗口内反复失败只发一条（见 notify.Dispatcher）。
func (m *Manager) NotifySyncFailure(ctx context.Context, source string, cause error) {
	if !m.Policy().NotifyOnSyncFailure {
		return
	}
	msg := notify.Message{
		Title:    fmt.Sprintf("观复 · %s 同步失败", source),
		Body:     fmt.Sprintf("数据源 %s 本次同步失败：%s", source, cause.Error()),
		Priority: notify.PriorityWarning,
	}
	results, err := m.dispatch.Send(ctx, msg, "sync-failure:"+source)
	if err != nil {
		log.Printf("[notify] 同步失败通知发送失败: %v", err)
	} else if len(results) > 0 {
		log.Printf("[notify] 已推送 %s 同步失败（%d 个渠道）", source, len(results))
	}
}

// validateBaseURL 只接受 http/https 的裸地址：带 query/fragment 的地址
// 拼上各家固定的 path 之后基本都不对，早报错好过同步时返回 404。
func validateBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil // 清空是合法操作
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("地址无法解析: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("地址必须以 http:// 或 https:// 开头")
	}
	if u.Host == "" {
		return fmt.Errorf("地址缺少主机名")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("地址不要带查询串或锚点（只要协议+主机+路径前缀）")
	}
	return nil
}

func validateExtras(name string, ex map[string]string, baseURL string) error {
	if name != config.NotifyApprise {
		return nil
	}
	key, targets := strings.TrimSpace(ex["key"]), strings.TrimSpace(ex["targets"])
	if key == "" && targets == "" && baseURL != "" {
		return fmt.Errorf("apprise 需要填 Key（服务端已存配置）或至少一个目标 URL")
	}
	return nil
}

func encodeExtras(ex map[string]string) string {
	if len(ex) == 0 {
		return ""
	}
	clean := map[string]string{}
	for k, v := range ex {
		if v = strings.TrimSpace(v); v != "" {
			clean[k] = v
		}
	}
	if len(clean) == 0 {
		return ""
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeExtras(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// SecretSource 返回凭据加密密钥的来源（env / file / test），供设置页说明。
func (m *Manager) SecretSource() string { return m.box.Source() }

// ProbeResult 端点连通性探测结果。
type ProbeResult struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	OK         bool   `json:"ok"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	// Message 人话结论（页面上直接显示这一句）。
	Message  string `json:"message"`
	Response string `json:"response,omitempty"`
}

// ProbeSource 对数据源做一次「地址 + 认证」探测。
//
// 只验证连通性与认证是否被接受，不验证数据格式：各源的业务接口路径由适配器
// 拼接，拿根路径探业务语义必然误报。所以 404 也算"地址可达"，而 401/403
// 明确判为失败——那才是配置错。
//
// draft 非空时用表单草稿覆盖（支持"没保存先测"），Secret 为 nil 表示沿用已存凭据。
func (m *Manager) ProbeSource(ctx context.Context, name string, draft *Input) (ProbeResult, error) {
	if _, ok := m.base.Sources[name]; !ok {
		return ProbeResult{}, fmt.Errorf("%w %q", ErrUnknownEndpoint, name)
	}
	cfg := m.Sources()[name]
	if draft != nil {
		if base := strings.TrimSpace(draft.BaseURL); base != "" {
			if err := validateBaseURL(base); err != nil {
				return ProbeResult{}, err
			}
			cfg.BaseURL = strings.TrimRight(base, "/")
		}
		if draft.Auth != "" {
			if !config.IsValidAuth(draft.Auth) {
				return ProbeResult{}, fmt.Errorf("认证方式 %q 不受支持", draft.Auth)
			}
			cfg.Auth = draft.Auth
		}
		if draft.Secret != nil {
			cfg.APIKey = *draft.Secret
		}
	}
	res := ProbeResult{Name: name, URL: cfg.BaseURL}
	if cfg.BaseURL == "" {
		res.Message = "还没填地址"
		return res, nil
	}

	// 8 秒足够区分「慢」与「不通」，又不至于让设置页按钮转太久。
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.BaseURL, nil)
	if err != nil {
		res.Message = "地址无法解析：" + err.Error()
		return res, nil
	}
	if err := cfg.ApplyAuth(req); err != nil {
		res.Message = err.Error()
		return res, nil
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 8 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Message = "连接失败：" + err.Error()
		return res, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
	res.Status = resp.StatusCode
	res.Response = strings.Join(strings.Fields(string(body)), " ")
	if len(res.Response) > 200 {
		res.Response = res.Response[:200] + "…"
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 400:
		res.OK = true
		res.Message = "地址可达，认证通过"
	case resp.StatusCode == http.StatusNotFound:
		// 根路径 404 很常见（服务把 API 挂在子路径下），不能判成配置错。
		res.OK = true
		res.Message = "地址可达（根路径 404 属正常，业务路径由适配器拼接）"
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		res.Message = fmt.Sprintf("地址可达，但认证被拒绝（HTTP %d）——检查凭据与认证方式", resp.StatusCode)
	default:
		res.Message = fmt.Sprintf("地址可达，但服务返回 HTTP %d", resp.StatusCode)
	}
	return res, nil
}
