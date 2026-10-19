package config

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// AuthModes 设置页下拉的展示顺序（也是校验白名单）。
var AuthModes = []string{AuthNone, AuthAPIKey, AuthBearer, AuthBasic, AuthJWT, AuthToken}

// IsValidAuth 判断认证方式是否是已知取值。
func IsValidAuth(mode string) bool {
	for _, m := range AuthModes {
		if m == mode {
			return true
		}
	}
	return false
}

// apiKeyHeaders 各源「Key 放哪个请求头」。与各适配器的历史实现逐字对齐，
// 改这里必须同时确认适配器没有硬编码第二份。
var apiKeyHeaders = map[string]string{
	"huozhi": "X-API-Key",
	"immich": "x-api-key",
	"mujian": "X-Auth-Token",
}

// APIKeyHeader 返回该源放 Key 的请求头名；未知源按 X-API-Key 兜底。
func APIKeyHeader(name string) string {
	if h, ok := apiKeyHeaders[name]; ok {
		return h
	}
	return "X-API-Key"
}

// AuthMode 返回生效的认证方式：显式配置优先，非法/空值回落到该源默认。
func (c SourceConfig) AuthMode() string {
	if IsValidAuth(c.Auth) {
		return c.Auth
	}
	return AuthKind(c.Name)
}

// ApplyAuth 按生效的认证方式给请求加头。
//
// 凭据为空时什么都不加（返回 nil）：各适配器自己会在同步入口给出
// 「X_API_KEY 未配置」这类更具体的错误，比在这里统一报错更好定位。
func (c SourceConfig) ApplyAuth(req *http.Request) error {
	if c.APIKey == "" {
		return nil
	}
	mode := c.AuthMode()
	// jwt 由适配器自己换票（换完仍是 Bearer），这里按 Bearer 发。
	if mode == AuthJWT {
		mode = AuthBearer
	}
	return ApplyAuth(req, mode, c.APIKey, APIKeyHeader(c.Name))
}

// ApplyAuth 给请求按指定认证方式加头。数据源与通知渠道共用同一套规则，
// 避免「页面上写着 API Key、实际发的是 Bearer」这种两套实现漂移。
//
// headerName 是 api_key / token 两种方式的请求头名。
func ApplyAuth(req *http.Request, mode, token, headerName string) error {
	if token == "" || mode == AuthNone {
		return nil
	}
	switch mode {
	case AuthBasic:
		v, err := basicCredential(token)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Basic "+v)
	case AuthBearer, AuthJWT:
		req.Header.Set("Authorization", "Bearer "+token)
	default: // api_key / token / 非法值（按最保守的自定义头处理）
		if headerName == "" {
			headerName = "X-API-Key"
		}
		req.Header.Set(headerName, token)
	}
	return nil
}

// basicCredential 接受 "用户名:密码" 或已经 base64 过的串。
func basicCredential(key string) (string, error) {
	if strings.Contains(key, ":") {
		return base64.StdEncoding.EncodeToString([]byte(key)), nil
	}
	if _, err := base64.StdEncoding.DecodeString(key); err != nil {
		return "", fmt.Errorf("Basic 认证凭据需为「用户名:密码」或 base64 编码串")
	}
	return key, nil
}
