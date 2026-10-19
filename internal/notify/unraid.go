package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Felix2yu/guanfu/internal/config"
)

// unraidMutation Unraid 7 GraphQL：往系统通知中心塞一条。
// 判据是官方 API schema 的 `createNotification(input: NotificationInput!)`，
// 字段为 title / subject / description / importance（NotificationImportance）。
const unraidMutation = `mutation CreateNotification($input: NotificationInput!) {
  createNotification(input: $input) { id }
}`

// Unraid Unraid 通知渠道。
type Unraid struct {
	cfg    config.NotifyChannel
	client *http.Client
}

func newUnraid(c config.NotifyChannel, client *http.Client) *Unraid {
	return &Unraid{cfg: c, client: client}
}

func (u *Unraid) Name() string { return config.NotifyUnraid }

// endpoint 允许用户在页面里填 http://tower 或 http://tower/graphql 两种写法，
// 少一个「填了地址还是 404」的坑。
func (u *Unraid) endpoint() string {
	base := strings.TrimRight(u.cfg.BaseURL, "/")
	if strings.HasSuffix(base, "/graphql") {
		return base
	}
	return base + "/graphql"
}

func (u *Unraid) Send(ctx context.Context, m Message) (Result, error) {
	payload := map[string]any{
		"query": unraidMutation,
		"variables": map[string]any{
			"input": map[string]any{
				// Unraid 的通知条目同时有 title 与 subject（列表显示 subject），
				// 两处保持一致，免得通知中心里出现两条不同标题。
				"title":       m.Title,
				"subject":     m.Title,
				"description": m.Body,
				"importance":  unraidImportance(m.Priority),
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Result{Channel: u.Name(), Error: err.Error()}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.endpoint(), bytes.NewReader(raw))
	if err != nil {
		return Result{Channel: u.Name(), Error: err.Error()}, err
	}
	req.Header.Set("Content-Type", "application/json")
	mode := u.cfg.Auth
	if mode == "" {
		mode = config.AuthAPIKey // Unraid 的 API Key 走 X-API-Key
	}
	if err := config.ApplyAuth(req, mode, u.cfg.Token, "X-API-Key"); err != nil {
		return Result{Channel: u.Name(), Error: err.Error()}, err
	}

	res, body, err := send(u.client, req)
	res.Channel = u.Name()
	if err != nil {
		return res, err
	}

	// GraphQL 的坑：HTTP 200 也可能是一段 {"errors":[…]}。
	// 只认状态码会把「权限不足」当成发送成功。
	var gql struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &gql) == nil && len(gql.Errors) > 0 {
		msg := gql.Errors[0].Message
		if msg == "" {
			msg = res.Response
		}
		res.OK = false
		res.Error = "GraphQL 返回错误: " + msg
		return res, fmt.Errorf("%s", res.Error)
	}
	return res, nil
}

// unraidImportance 三档优先级 → Unraid 的 NotificationImportance 枚举。
func unraidImportance(p string) string {
	switch p {
	case PriorityAlert:
		return "ALERT"
	case PriorityWarning:
		return "WARNING"
	default:
		return "NORMAL"
	}
}
