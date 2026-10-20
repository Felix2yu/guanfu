package secret

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := FromPassphrase("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	// 明文都取「不可能随机撞进 base64」的长度：短明文（如单字符）会让
	// Contains 断言变成掷硬币，只会在 CI 里随机翻车。
	for _, plain := range []string{"x-api-key-value", "用户名:密码", strings.Repeat("a", 4096)} {
		sealed, err := box.Seal(plain)
		if err != nil {
			t.Fatalf("Seal(%q): %v", plain, err)
		}
		if !strings.HasPrefix(sealed, Prefix) {
			t.Fatalf("密文缺少前缀: %q", sealed)
		}
		if strings.Contains(sealed, plain) {
			t.Fatalf("密文里出现了明文: %q", sealed)
		}
		got, err := box.Open(sealed)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got != plain {
			t.Fatalf("往返不一致: got %q want %q", got, plain)
		}
	}
}

func TestSealOpenRoundTripSingleByte(t *testing.T) {
	box, err := FromPassphrase("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("k")
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != "k" {
		t.Fatalf("往返不一致: got %q want %q", got, "k")
	}
}

func TestSealEmptyIsEmpty(t *testing.T) {
	box, _ := FromPassphrase("k")
	sealed, err := box.Seal("")
	if err != nil || sealed != "" {
		t.Fatalf("空串应保持空串: %q %v", sealed, err)
	}
	if got, err := box.Open(""); err != nil || got != "" {
		t.Fatalf("空密文应返回空串: %q %v", got, err)
	}
}

func TestOpenRejectsTampered(t *testing.T) {
	box, _ := FromPassphrase("k")
	sealed, _ := box.Seal("secret-value")
	// 直接翻转密文末字节再编码回去。不能只在 base64 文本上改末位字符：
	// 末位 6 bit 里常有 2 bit 是填充位，改了照样解出同样的字节。
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, Prefix))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0x01
	bad := Prefix + base64.RawURLEncoding.EncodeToString(raw)
	if _, err := box.Open(bad); err == nil {
		t.Fatal("篡改后的密文必须报错")
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	a, _ := FromPassphrase("key-a")
	b, _ := FromPassphrase("key-b")
	sealed, _ := a.Seal("token")
	if _, err := b.Open(sealed); err == nil {
		t.Fatal("换密钥后必须报错，而不是给出错误明文")
	} else if !strings.Contains(err.Error(), "主密钥") {
		t.Fatalf("错误信息应指向主密钥: %v", err)
	}
}

func TestOpenRejectsPlaintext(t *testing.T) {
	box, _ := FromPassphrase("k")
	if _, err := box.Open("plain-token"); err == nil {
		t.Fatal("非 enc:v1: 形态必须报错")
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"abc":               "········",
		"1234567":           "········",
		"12345678":          "········5678",
		"sk-abcdefghijklmn": "········klmn",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKeyFileCreatedWith0600AndStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guanfu.db.key")

	t.Setenv(EnvKey, "") // 确保走文件分支
	b1, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("密钥文件未生成: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("密钥文件权限 %o，应为 600", perm)
	}
	sealed, _ := b1.Seal("token")

	// 再次 Load 必须复用同一把密钥（否则重启后旧密文全废）。
	b2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b2.Open(sealed); err != nil || got != "token" {
		t.Fatalf("重启后解不开: %q %v", got, err)
	}
}

func TestEnvKeyWinsOverFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k.key")

	t.Setenv(EnvKey, "env-passphrase")
	sealed, err := sealVia(t, path, "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("有环境变量时不该生成密钥文件")
	}
	// 换一个环境变量值：解不开，证明真的走了 env。
	t.Setenv(EnvKey, "other-passphrase")
	box, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("换口令后应解不开")
	}
}

func TestBase64KeyAccepted(t *testing.T) {
	// 32 字节的 base64 直接当密钥（不再哈希），保证二进制密钥可用。
	raw := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 个 0
	box, err := FromPassphrase(raw)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Open(sealed); err != nil {
		t.Fatal(err)
	}
}

func sealVia(t *testing.T, keyPath, plain string) (string, error) {
	t.Helper()
	box, err := Load(keyPath)
	if err != nil {
		return "", err
	}
	return box.Seal(plain)
}
