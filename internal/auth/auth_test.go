package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileNameSanitize(t *testing.T) {
	a := New("u1", "n", "tok")
	if got := a.FileName(); got != "catpaw-u1.json" {
		t.Fatalf("FileName=%s", got)
	}
	a2 := New("a"+string(os.PathSeparator)+"b", "n", "tok")
	if got := a2.FileName(); strings.Contains(got, string(os.PathSeparator)+"b") {
		t.Fatalf("FileName not sanitized: %s", got)
	}
	empty := New("", "n", "tok")
	if got := empty.FileName(); got != "catpaw-unknown.json" {
		t.Fatalf("FileName=%s", got)
	}
}

func TestSaveNewAndLoadDir(t *testing.T) {
	dir := t.TempDir()
	a1 := New("u1", "alice", "token-1")
	a2 := New("u2", "bob", "token-2")
	if err := SaveNew(dir, a1); err != nil {
		t.Fatal(err)
	}
	if err := SaveNew(dir, a2); err != nil {
		t.Fatal(err)
	}
	// 非 catpaw- 前缀 / 空 token 的文件应被跳过。
	_ = os.WriteFile(filepath.Join(dir, "other.json"), []byte(`{"uid":"x"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "catpaw-empty.json"), []byte(`{"uid":"e","access_token":""}`), 0o600)

	auths, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(auths) != 2 {
		t.Fatalf("loaded=%d", len(auths))
	}
	// LoadDir 按 UID 排序。
	if auths[0].UID != "u1" || auths[1].UID != "u2" {
		t.Fatalf("order=%s,%s", auths[0].UID, auths[1].UID)
	}
	if auths[0].Token() != "token-1" {
		t.Fatalf("token=%s", auths[0].Token())
	}

	// 文件权限与内容。
	raw, err := os.ReadFile(filepath.Join(dir, "catpaw-u1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"access_token"`) {
		t.Fatalf("content=%s", raw)
	}
}

func TestLoadDirMissing(t *testing.T) {
	auths, err := LoadDir(filepath.Join(t.TempDir(), "nope"))
	if err != nil || auths != nil {
		t.Fatalf("auths=%v err=%v", auths, err)
	}
}

func TestTokenLifetimeHelpers(t *testing.T) {
	fresh := New("u", "n", "tok")
	if fresh.Expired() {
		t.Fatal("fresh token should not be expired")
	}
	if fresh.ExpiringSoon(24 * time.Hour) {
		t.Fatal("fresh token should not be expiring soon")
	}
	if rem := fresh.Remaining(); rem <= 70*time.Hour || rem > TokenLifetime {
		t.Fatalf("remaining=%s", rem)
	}
	if exp := fresh.ExpiresAt(); exp.IsZero() || exp.Before(time.Now().Add(70*time.Hour)) {
		t.Fatalf("expiresAt=%v", exp)
	}

	old := &Auth{UID: "u", AccessToken: "tok", UpdatedAt: time.Now().Add(-73 * time.Hour).Unix()}
	if !old.Expired() {
		t.Fatal("73h token should be expired")
	}
	if old.ExpiringSoon(24 * time.Hour) {
		t.Fatal("expired is not 'expiring soon'")
	}

	soon := &Auth{UID: "u", AccessToken: "tok", UpdatedAt: time.Now().Add(-60 * time.Hour).Unix()}
	if soon.Expired() {
		t.Fatal("60h token not expired")
	}
	if !soon.ExpiringSoon(24 * time.Hour) {
		t.Fatal("60h token should be expiring within 24h")
	}

	unknown := &Auth{UID: "u", AccessToken: "tok"}
	if unknown.Age() != 0 {
		t.Fatalf("age=%s", unknown.Age())
	}
	if unknown.Remaining() != TokenLifetime {
		t.Fatalf("remaining=%s", unknown.Remaining())
	}
	if !unknown.ExpiresAt().IsZero() {
		t.Fatal("ExpiresAt should be zero for missing UpdatedAt")
	}
}

func TestSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	a := New("u1", "n", "tok-a")
	if err := SaveNew(dir, a); err != nil {
		t.Fatal(err)
	}
	a.AccessToken = "tok-b"
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	auths, err := LoadDir(dir)
	if err != nil || len(auths) != 1 {
		t.Fatalf("auths=%d err=%v", len(auths), err)
	}
	if auths[0].Token() != "tok-b" {
		t.Fatalf("token=%s", auths[0].Token())
	}
	// 无路径的 Auth 保存应报错而不是 panic。
	bare := New("x", "n", "t")
	if err := bare.Save(); err == nil {
		t.Fatal("expected error for empty path")
	}
}
