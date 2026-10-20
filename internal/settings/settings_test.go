package settings

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/secret"
	"github.com/Felix2yu/guanfu/internal/store"
)

func baseConfig() *config.Config {
	return &config.Config{
		Sources: map[string]config.SourceConfig{
			"huozhi": {
				Name: "huozhi", BaseURL: "http://env-huozhi:8080", APIKey: "env-key",
				Enabled: true,
			},
			"immich": {Name: "immich", Enabled: false},
		},
		Notify: config.NotifyConfig{Channels: map[string]config.NotifyChannel{
			config.NotifyApprise: {Name: config.NotifyApprise},
		}},
	}
}

func newManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.FromPassphrase("test-pass")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st, baseConfig(), box)
	if err != nil {
		t.Fatal(err)
	}
	return m, st
}

func ptr(s string) *string { return &s }

// ---------- 优先级 ----------

func TestDefaultsComeFromEnv(t *testing.T) {
	m, _ := newManager(t)
	src := m.Sources()["huozhi"]
	if src.BaseURL != "http://env-huozhi:8080" || src.APIKey != "env-key" {
		t.Fatalf("未保存时应读 env: %+v", src)
	}
	v, ok := m.View("huozhi")
	if !ok || v.Origin != OriginEnv {
		t.Fatalf("来源应为 env: %+v", v)
	}
	if v.SecretHint == "" || v.SecretOrigin != OriginEnv {
		t.Fatalf("env 凭据也要给掩码: %+v", v)
	}
}

func TestPageOverridesEnvWholeBlock(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Save("huozhi", Input{
		BaseURL: "http://page-huozhi:9000", Auth: config.AuthBearer,
		Secret: ptr("page-token"), Enabled: false, IntervalSecs: 600,
	}); err != nil {
		t.Fatal(err)
	}

	src := m.Sources()["huozhi"]
	if src.BaseURL != "http://page-huozhi:9000" {
		t.Fatalf("地址未覆盖: %q", src.BaseURL)
	}
	if src.APIKey != "page-token" {
		t.Fatalf("凭据未覆盖: %q", src.APIKey)
	}
	if src.AuthMode() != config.AuthBearer {
		t.Fatalf("认证方式未覆盖: %q", src.AuthMode())
	}
	if src.Enabled {
		t.Fatal("启用状态未覆盖")
	}
	if int(src.Interval.Seconds()) != 600 {
		t.Fatalf("间隔未覆盖: %v", src.Interval)
	}

	v, _ := m.View("huozhi")
	if v.Origin != OriginPage {
		t.Fatalf("来源应为 page: %+v", v)
	}
	if v.SecretSet == false || v.SecretHint != "········oken" {
		t.Fatalf("掩码不符: %+v", v)
	}
	if v.EnvBaseURL != "http://env-huozhi:8080" {
		t.Fatalf("仍应展示 env 地址供对照: %q", v.EnvBaseURL)
	}
}

func TestPageEmptyMeansClearedNotFallback(t *testing.T) {
	m, _ := newManager(t)
	// 页面把地址与凭据都清空：不能悄悄回退到 env。
	if _, err := m.Save("huozhi", Input{BaseURL: "", Auth: "", Secret: ptr(""), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	src := m.Sources()["huozhi"]
	if src.BaseURL != "" || src.APIKey != "" {
		t.Fatalf("清空未生效: %+v", src)
	}
}

func TestSecretNilKeepsExisting(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Save("huozhi", Input{BaseURL: "http://a", Secret: ptr("tok-1"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// 第二次保存只改地址，Secret 传 nil：凭据必须原样保留。
	if _, err := m.Save("huozhi", Input{BaseURL: "http://b", Secret: nil, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	src := m.Sources()["huozhi"]
	if src.BaseURL != "http://b" || src.APIKey != "tok-1" {
		t.Fatalf("凭据被误清: %+v", src)
	}
}

func TestResetRestoresEnv(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Save("huozhi", Input{BaseURL: "http://page", Secret: ptr("p"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ok, err := m.Reset("huozhi")
	if err != nil || !ok {
		t.Fatalf("重置失败: %v %v", ok, err)
	}
	src := m.Sources()["huozhi"]
	if src.BaseURL != "http://env-huozhi:8080" || src.APIKey != "env-key" {
		t.Fatalf("未回到 env: %+v", src)
	}
	if v, _ := m.View("huozhi"); v.Origin != OriginEnv {
		t.Fatalf("来源应回到 env: %+v", v)
	}
	// 再删一次：没有行可删。
	if ok, _ := m.Reset("huozhi"); ok {
		t.Fatal("重复重置应返回 false")
	}
}

// ---------- 凭据落库形态 ----------

func TestSecretIsEncryptedAtRest(t *testing.T) {
	m, st := newManager(t)
	if _, err := m.Save("huozhi", Input{BaseURL: "http://x", Secret: ptr("super-secret"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetEndpointConfig("huozhi")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.Secret, "super-secret") {
		t.Fatalf("库里出现了明文: %q", row.Secret)
	}
	if !strings.HasPrefix(row.Secret, secret.Prefix) {
		t.Fatalf("密文缺少前缀: %q", row.Secret)
	}
	// API 视图同样不能含明文。
	raw, _ := json.Marshal(m.List())
	if strings.Contains(string(raw), "super-secret") {
		t.Fatalf("视图泄漏明文: %s", raw)
	}
}

func TestWrongKeySurfacesErrorAndClearsCredential(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	boxA, _ := secret.FromPassphrase("key-a")
	mA, err := NewManager(st, baseConfig(), boxA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mA.Save("huozhi", Input{BaseURL: "http://x", Secret: ptr("tok"), Enabled: true}); err != nil {
		t.Fatal(err)
	}

	boxB, _ := secret.FromPassphrase("key-b")
	mB, err := NewManager(st, baseConfig(), boxB)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := mB.View("huozhi")
	if v.Error == "" || !strings.Contains(v.Error, "主密钥") {
		t.Fatalf("应显式暴露解密失败: %+v", v)
	}
	if mB.Sources()["huozhi"].APIKey != "" {
		t.Fatal("解不开时不能拿 env 旧 key 顶上")
	}
}

// ---------- 校验 ----------

func TestSaveValidation(t *testing.T) {
	m, _ := newManager(t)
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"nope", Input{}, "未知端点"},
		{"huozhi", Input{BaseURL: "ftp://x"}, "http"},
		{"huozhi", Input{BaseURL: "http://x/?a=1"}, "查询串"},
		{"huozhi", Input{BaseURL: "http://x", Auth: "magic"}, "认证方式"},
		{"huozhi", Input{BaseURL: "http://x", IntervalSecs: -1}, "间隔"},
		{"huozhi", Input{BaseURL: "http://x", IntervalSecs: 31 * 86400}, "间隔"},
		{"apprise", Input{BaseURL: "http://apprise"}, "Key"},
	}
	for _, tc := range cases {
		if _, err := m.Save(tc.name, tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Save(%s, %+v) 应报含 %q 的错误，得到 %v", tc.name, tc.in, tc.want, err)
		}
	}
	// 同类请求换个合法值必须能过（防止校验把正常操作也挡了）。
	if _, err := m.Save("apprise", Input{
		BaseURL: "http://apprise", Enabled: true,
		Extras: map[string]string{"targets": "json://localhost"},
	}); err != nil {
		t.Fatalf("合法 apprise 配置被挡: %v", err)
	}
}

// ---------- 通知渠道 ----------

func TestNotifyChannelOverrideAndExtras(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Save("apprise", Input{
		BaseURL: "http://apprise:8000", Auth: config.AuthBearer,
		Secret: ptr("a-token"), Enabled: true,
		Extras: map[string]string{"key": "family", "targets": "tgram://b/c"},
	}); err != nil {
		t.Fatal(err)
	}
	ch := m.Effective().Notify.Channel(config.NotifyApprise)
	if ch.BaseURL != "http://apprise:8000" || ch.Key != "family" ||
		ch.Targets != "tgram://b/c" || ch.Token != "a-token" || !ch.Enabled {
		t.Fatalf("渠道覆盖不完整: %+v", ch)
	}
	if got := m.Effective().Notify.EnabledChannels(); len(got) != 1 || got[0] != config.NotifyApprise {
		t.Fatalf("启用渠道不符: %v", got)
	}
	v, _ := m.View("apprise")
	if v.Extras["key"] != "family" || v.Error != "" {
		t.Fatalf("视图不符: %+v", v)
	}
}

func TestPolicyToggle(t *testing.T) {
	m, _ := newManager(t)
	if m.Policy().NotifyOnSyncFailure {
		t.Fatal("默认应关闭")
	}
	if err := m.SetPolicy(Policy{NotifyOnSyncFailure: true}); err != nil {
		t.Fatal(err)
	}
	if !m.Policy().NotifyOnSyncFailure {
		t.Fatal("开启未生效")
	}
	if err := m.SetPolicy(Policy{NotifyOnSyncFailure: false}); err != nil {
		t.Fatal(err)
	}
	if m.Policy().NotifyOnSyncFailure {
		t.Fatal("关闭未生效")
	}
}

func TestNotifySyncFailureRespectsPolicy(t *testing.T) {
	m, _ := newManager(t)
	// 未配渠道 + 开关关闭：必须静默返回，不能 panic、不能报错。
	m.NotifySyncFailure(context.Background(), "huozhi", errString("boom"))
}

func TestOnApplyFiresOnSaveAndReset(t *testing.T) {
	m, _ := newManager(t)
	count := 0
	var seen string
	m.OnApply(func(c *config.Config) {
		count++
		seen = c.Sources["huozhi"].BaseURL
	})
	if _, err := m.Save("huozhi", Input{BaseURL: "http://new", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if count != 1 || seen != "http://new" {
		t.Fatalf("保存后应回调一次并带上新配置: count=%d seen=%q", count, seen)
	}
	if _, err := m.Reset("huozhi"); err != nil {
		t.Fatal(err)
	}
	if count != 2 || seen != "http://env-huozhi:8080" {
		t.Fatalf("重置后应回调一次: count=%d seen=%q", count, seen)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
