// Package secret 给「页面写入的凭据」做落库前封装：AES-256-GCM 加密 + 掩码。
//
// 为什么不是明文存库：SQLite 文件会被 `guanfu backup` 整库快照带走，
// 明文 token 等于把全家桶的钥匙抄在备份里。密文落库后，快照没有密钥文件
// 就打不开；密钥文件与库文件分开权限（0600），泄漏面小一号。
//
// 密钥来源（按优先级）：
//  1. GUANFU_SECRET_KEY 环境变量 —— 推荐容器部署显式给一个；
//  2. 库文件同目录的 `<db>.key` —— 不存在则自动生成 32 字节随机量（0600）。
//
// 两种方式互斥：换密钥方式会导致旧密文解不开（Open 报错，而不是静默清空），
// 这是刻意的——静默清空凭据比报错难查得多。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Prefix 密文标记。带版本号，未来换算法时可以按前缀分流迁移。
const Prefix = "enc:v1:"

// EnvKey 显式指定主密钥的环境变量名。
const EnvKey = "GUANFU_SECRET_KEY"

// Box 加解密器。零值不可用，必须经 Load / FromPassphrase 构造。
type Box struct {
	aead   cipher.AEAD
	source string // 密钥来源：env / file / test
}

// Source 返回密钥来源（供设置页如实展示「这把钥匙存在哪」）。
func (b *Box) Source() string {
	if b == nil {
		return ""
	}
	return b.source
}

// Load 按优先级取主密钥构造 Box。keyPath 为库文件配套的密钥文件路径
// （通常 <db>.key），仅在没有 GUANFU_SECRET_KEY 时使用。
func Load(keyPath string) (*Box, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvKey)); raw != "" {
		key, err := deriveKey(raw)
		if err != nil {
			return nil, fmt.Errorf("%s 不可用: %w", EnvKey, err)
		}
		b, err := newBox(key)
		if err != nil {
			return nil, err
		}
		b.source = "env"
		return b, nil
	}
	key, err := loadOrCreateKeyFile(keyPath)
	if err != nil {
		return nil, err
	}
	b, err := newBox(key)
	if err != nil {
		return nil, err
	}
	b.source = "file"
	return b, nil
}

// FromPassphrase 直接用一个口令构造 Box（测试与「不落盘」场景用）。
func FromPassphrase(pass string) (*Box, error) {
	key, err := deriveKey(pass)
	if err != nil {
		return nil, err
	}
	b, err := newBox(key)
	if err != nil {
		return nil, err
	}
	b.source = "test"
	return b, nil
}

func newBox(key []byte) (*Box, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	return &Box{aead: aead}, nil
}

// deriveKey 把任意口令收敛成 32 字节密钥：
// 先试「32 字节的 base64」（允许用户直接给二进制密钥），否则 SHA-256 派生。
func deriveKey(pass string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(pass); err == nil && len(b) == 32 {
		return b, nil
	}
	sum := sha256.Sum256([]byte(pass))
	return sum[:], nil
}

func loadOrCreateKeyFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("密钥文件路径为空（既没设 " + EnvKey + "，也没给库路径）")
	}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(b) != 32 {
			return nil, fmt.Errorf("密钥文件 %s 长度异常（%d 字节，应为 32）", path, len(b))
		}
		return b, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("读密钥文件失败: %w", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成密钥失败: %w", err)
	}
	// O_EXCL：并发启动的两个进程不会互相覆盖（后者拿到已存在错误，重读即可）。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateKeyFile(path)
		}
		return nil, fmt.Errorf("创建密钥文件失败: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(key); err != nil {
		return nil, fmt.Errorf("写密钥文件失败: %w", err)
	}
	return key, nil
}

// Seal 加密。空串不加密（保持「没设过」的语义，也省一次加解密）。
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if b == nil || b.aead == nil {
		return "", errors.New("加密器未初始化")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成 nonce 失败: %w", err)
	}
	ct := b.aead.Seal(nil, nonce, []byte(plain), nil)
	return Prefix + base64.RawURLEncoding.EncodeToString(append(nonce, ct...)), nil
}

// Open 解密。空串原样返回空串。
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !strings.HasPrefix(sealed, Prefix) {
		// 兼容手工写库的场景：不做「看起来像明文就当明文」的猜测，
		// 否则一次误写就会让后面所有读取路径都以为密钥是对的。
		return "", errors.New("凭据不是 " + Prefix + " 格式（可能被手工改动过）")
	}
	if b == nil || b.aead == nil {
		return "", errors.New("加密器未初始化")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, Prefix))
	if err != nil {
		return "", fmt.Errorf("凭据编码损坏: %w", err)
	}
	ns := b.aead.NonceSize()
	if len(raw) <= ns {
		return "", errors.New("凭据长度不足")
	}
	out, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		// GCM 校验失败两种可能：密文被改，或换了主密钥。
		return "", errors.New("凭据解密失败：主密钥与写入时不一致（检查 " + EnvKey + " 或密钥文件）")
	}
	return string(out), nil
}

// Mask 把明文收敛成可下发的展示形态：只留末 4 位，短于 8 位时完全不露。
func Mask(plain string) string {
	if plain == "" {
		return ""
	}
	r := []rune(plain)
	if len(r) < 8 {
		return "········"
	}
	return "········" + string(r[len(r)-4:])
}
