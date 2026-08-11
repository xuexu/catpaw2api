// Package auth 管理 auths/ 目录下的 CatPaw 凭证文件 catpaw-{uid}.json。
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TokenLifetime 个人版 passport token 有效时长（72h，逆向自 ACCESS_TOKEN_LIFETIME_MS）。
const TokenLifetime = 72 * time.Hour

// Auth 单个账号凭证。
type Auth struct {
	UID         string `json:"uid"`
	UserName    string `json:"user_name"`
	AccessToken string `json:"access_token"`
	UpdatedAt   int64  `json:"updated_at"` // Unix 秒

	path string
	mu   sync.Mutex
}

// New 构造 Auth（登录落盘用）。
func New(uid, userName, token string) *Auth {
	return &Auth{
		UID:         uid,
		UserName:    userName,
		AccessToken: token,
		UpdatedAt:   time.Now().Unix(),
	}
}

// FileName 返回 auth 文件名。
func (a *Auth) FileName() string {
	id := strings.ReplaceAll(a.UID, string(filepath.Separator), "_")
	if id == "" {
		id = "unknown"
	}
	return fmt.Sprintf("catpaw-%s.json", id)
}

// Token 返回 access token（并发安全快照）。
func (a *Auth) Token() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.AccessToken
}

// Age 返回 token 已存在时长。
func (a *Auth) Age() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.UpdatedAt == 0 {
		return 0
	}
	return time.Since(time.Unix(a.UpdatedAt, 0))
}

// ExpiresAt 返回 token 预计过期时间（UpdatedAt + 72h；UpdatedAt 缺失时返回零值）。
func (a *Auth) ExpiresAt() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.UpdatedAt == 0 {
		return time.Time{}
	}
	return time.Unix(a.UpdatedAt, 0).Add(TokenLifetime)
}

// Remaining 返回 token 剩余有效期（<=0 表示已过期；UpdatedAt 缺失时返回 TokenLifetime）。
func (a *Auth) Remaining() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.UpdatedAt == 0 {
		return TokenLifetime
	}
	return time.Until(time.Unix(a.UpdatedAt, 0).Add(TokenLifetime))
}

// Expired 报告 token 是否已过期（按 72h 推算，不等上游 401）。
func (a *Auth) Expired() bool {
	return a.Remaining() <= 0
}

// ExpiringSoon 报告 token 是否将在 within 内过期。
func (a *Auth) ExpiringSoon(within time.Duration) bool {
	r := a.Remaining()
	return r > 0 && r <= within
}

// Save 原子写回 auth 文件（0600）。
func (a *Auth) Save() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return saveLocked(a.path, a)
}

// SetPath 设置文件路径（LoadDir 时内部调用）。
func (a *Auth) SetPath(p string) { a.path = p }

// LoadDir 加载 auths 目录下全部 catpaw-*.json。
func LoadDir(dir string) ([]*Auth, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Auth
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "catpaw-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var a Auth
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if a.AccessToken == "" {
			continue
		}
		a.path = p
		out = append(out, &a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// SaveNew 把新账号写入 auths 目录。
func SaveNew(dir string, a *Auth) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	a.path = filepath.Join(dir, a.FileName())
	return a.Save()
}

func saveLocked(path string, v any) error {
	if path == "" {
		return fmt.Errorf("auth: empty path")
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
