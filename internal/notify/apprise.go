package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Felix2yu/guanfu/internal/config"
)

// Apprise Apprise API / Apprise-Go 通知渠道。
//
// 两种调用形态（都是 Apprise API 的正式用法）：
//
//	POST {base}/notify/{key}   —— 用服务端已保存的配置（推荐，多目标在这里配）。
//	POST {base}/notify         —— 请求体里带 urls 列表，临时指定目标。
//
// 请求体：{ "title", "body", "type" }，type ∈ info/success/warning/failure。
type Apprise struct {
	cfg    config.NotifyChannel
	client *http.Client
}

func newApprise(c config.NotifyChannel, client *http.Client) *Apprise {
	return &Apprise{cfg: c, client: client}
}

func (a *Apprise) Name() string { return config.NotifyApprise }

func (a *Apprise) endpoint() string {
	base := strings.TrimRight(a.cfg.BaseURL, "/")
	if key := strings.TrimSpace(a.cfg.Key); key != "" {
		// key 会进 URL 路径，必须转义：允许用户填 "family/phone" 这种带层级的键。
		return base + "/notify/" + url.PathEscape(key)
	}
	return base + "/notify"
}

func (a *Apprise) Send(ctx context.Context, m Message) (Result, error) {
	payload := map[string]any{
		"title": m.Title,
		"body":  m.Body,
		"type":  appriseType(m.Priority),
	}
	if strings.TrimSpace(a.cfg.Key) == "" {
		targets := splitTargets(a.cfg.Targets)
		if len(targets) == 0 {
			err := fmt.Errorf("apprise 需要配置 Key（服务端已存配置）或至少一个目标 URL")
			return Result{Channel: a.Name(), Error: err.Error()}, err
		}
		payload["urls"] = targets
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return Result{Channel: a.Name(), Error: err.Error()}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), bytes.NewReader(raw))
	if err != nil {
		return Result{Channel: a.Name(), Error: err.Error()}, err
	}
	req.Header.Set("Content-Type", "application/json")
	mode := a.cfg.Auth
	if mode == "" {
		mode = config.AuthNone // 自建 Apprise 多数不带鉴权
	}
	if err := config.ApplyAuth(req, mode, a.cfg.Token, "X-API-Key"); err != nil {
		return Result{Channel: a.Name(), Error: err.Error()}, err
	}

	res, body, err := send(a.client, req)
	res.Channel = a.Name()
	if err != nil {
		return res, err
	}

	// Apprise 成功时回 {"success":true}；个别代理回 200 但 success=false，
	// 这种情况按失败处理，否则「测试发送」会给出虚假的成功。
	var out struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &out) == nil && out.Success != nil && !*out.Success {
		msg := out.Error
		if msg == "" {
			msg = res.Response
		}
		res.OK = false
		res.Error = "Apprise 返回失败: " + msg
		return res, fmt.Errorf("%s", res.Error)
	}
	return res, nil
}

// appriseType 三档优先级 → Apprise 的 type 枚举。
func appriseType(p string) string {
	switch p {
	case PriorityAlert:
		return "failure"
	case PriorityWarning:
		return "warning"
	default:
		return "info"
	}
}

// splitTargets 目标 URL 列表：逗号 / 换行 / 空白都能分隔。
// Apprise URL 形如 tgram://bot/token，内部本身有 ':' 与 '/'，所以只按这三类切。
func splitTargets(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if v := strings.TrimSpace(f); v != "" {
			out = append(out, v)
		}
	}
	return out
}
