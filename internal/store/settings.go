package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 端点类型。源与通知渠道共用一张表、同一套「行存在即接管」语义，
// 区别只在 kind 与 extras_json 的用途。
const (
	KindSource = "source"
	KindNotify = "notify"
)

// EndpointConfig 页面保存的一行端点配置。
//
// Secret 是密文（secret.Box.Seal 的产物），不是明文——store 这一层不碰加密，
// 只负责存取，加解密与掩码在 settings 层做。
type EndpointConfig struct {
	Name         string
	Kind         string
	BaseURL      string
	Auth         string
	Secret       string
	ExtrasJSON   string
	Enabled      bool
	IntervalSecs int
	UpdatedAt    int64
}

// ErrNoEndpoint 表示该端点没有页面配置（调用方应回退到环境变量）。
var ErrNoEndpoint = errors.New("端点没有页面配置")

// ListEndpointConfigs 列出某类端点的页面配置；kind 为空表示全部。
func (s *Store) ListEndpointConfigs(kind string) ([]EndpointConfig, error) {
	q := `SELECT name, kind, base_url, auth, secret, extras_json, enabled, interval_secs, updated_at
		FROM endpoint_config`
	var args []any
	if kind != "" {
		q += ` WHERE kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY name ASC`

	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询端点配置失败: %w", err)
	}
	defer rows.Close()

	var out []EndpointConfig
	for rows.Next() {
		var c EndpointConfig
		if err := rows.Scan(&c.Name, &c.Kind, &c.BaseURL, &c.Auth, &c.Secret,
			&c.ExtrasJSON, &c.Enabled, &c.IntervalSecs, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("读取端点配置失败: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetEndpointConfig 取单个端点配置；没有时返回 ErrNoEndpoint（而非 sql.ErrNoRows，
// 让调用方不必 import database/sql 判断）。
func (s *Store) GetEndpointConfig(name string) (*EndpointConfig, error) {
	var c EndpointConfig
	err := s.DB.QueryRow(`SELECT name, kind, base_url, auth, secret, extras_json,
			enabled, interval_secs, updated_at
		FROM endpoint_config WHERE name = ?`, name).
		Scan(&c.Name, &c.Kind, &c.BaseURL, &c.Auth, &c.Secret,
			&c.ExtrasJSON, &c.Enabled, &c.IntervalSecs, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoEndpoint
	}
	if err != nil {
		return nil, fmt.Errorf("读取端点配置失败: %w", err)
	}
	return &c, nil
}

// SaveEndpointConfig 写入（或整体覆盖）一条配置。UpdatedAt 由本方法填。
func (s *Store) SaveEndpointConfig(c EndpointConfig) error {
	if c.Kind == "" {
		c.Kind = KindSource
	}
	c.UpdatedAt = time.Now().Unix()
	_, err := s.DB.Exec(`INSERT INTO endpoint_config
			(name, kind, base_url, auth, secret, extras_json, enabled, interval_secs, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			kind = excluded.kind,
			base_url = excluded.base_url,
			auth = excluded.auth,
			secret = excluded.secret,
			extras_json = excluded.extras_json,
			enabled = excluded.enabled,
			interval_secs = excluded.interval_secs,
			updated_at = excluded.updated_at`,
		c.Name, c.Kind, c.BaseURL, c.Auth, c.Secret, c.ExtrasJSON, c.Enabled, c.IntervalSecs, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("保存端点配置失败: %w", err)
	}
	return nil
}

// DeleteEndpointConfig 删除页面配置（即「恢复环境变量」）。返回是否确有行被删。
func (s *Store) DeleteEndpointConfig(name string) (bool, error) {
	res, err := s.DB.Exec(`DELETE FROM endpoint_config WHERE name = ?`, name)
	if err != nil {
		return false, fmt.Errorf("删除端点配置失败: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// GetSetting 取应用级开关；不存在时返回空串（调用方给默认值）。
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM app_setting WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取设置失败: %w", err)
	}
	return v, nil
}

// SetSetting 写应用级开关（空值等于删除，避免"存了个空串"与"没存"混淆）。
func (s *Store) SetSetting(key, value string) error {
	if value == "" {
		_, err := s.DB.Exec(`DELETE FROM app_setting WHERE key = ?`, key)
		if err != nil {
			return fmt.Errorf("清除设置失败: %w", err)
		}
		return nil
	}
	_, err := s.DB.Exec(`INSERT INTO app_setting (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("保存设置失败: %w", err)
	}
	return nil
}
