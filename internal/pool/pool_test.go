package pool

import (
	"path/filepath"
	"testing"
	"time"

	"catpaw2api/internal/auth"
)

func newTestPool(t *testing.T, uids ...string) *Pool {
	t.Helper()
	var auths []*auth.Auth
	for _, id := range uids {
		auths = append(auths, &auth.Auth{UID: id, UserName: "user-" + id, AccessToken: "tok-" + id})
	}
	p, err := New(auths, Config{}, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPickByBalanceDescending(t *testing.T) {
	p := newTestPool(t, "a", "b", "c")
	// 全部余额为 0 时按 UID 字典序。
	if got := p.PickExcluding(nil); got == nil || got.UID != "a" {
		t.Fatalf("pick=%+v", got)
	}
	p.SetBalance("b", 500)
	p.SetBalance("c", 300)
	p.SetBalance("a", 100)
	if got := p.PickExcluding(nil); got == nil || got.UID != "b" {
		t.Fatalf("pick=%+v", got)
	}
	// tried 排除最高余额后轮到 c。
	if got := p.PickExcluding(map[string]bool{"b": true}); got == nil || got.UID != "c" {
		t.Fatalf("pick=%+v", got)
	}
	// 全部排除。
	if got := p.PickExcluding(map[string]bool{"a": true, "b": true, "c": true}); got != nil {
		t.Fatalf("pick=%+v", got)
	}
}

func TestPickSkipsCooldownAndDisabled(t *testing.T) {
	p := newTestPool(t, "a", "b")
	p.SetBalance("a", 900)
	p.SetBalance("b", 100)
	p.Cooldown("a", CoolSoft, time.Minute, "429")
	if got := p.PickExcluding(nil); got == nil || got.UID != "b" {
		t.Fatalf("pick=%+v", got)
	}
	p.Disable("b", "401")
	if got := p.PickExcluding(nil); got != nil {
		t.Fatalf("pick=%+v", got)
	}
}

func TestUnfreeze(t *testing.T) {
	p := newTestPool(t, "a")
	p.Cooldown("a", CoolPlan, time.Hour, "balance exhausted")
	if p.Healthy("a") {
		t.Fatal("should be cooling")
	}
	p.NoteError("a", 3, 10*time.Minute)
	p.Unfreeze("a")
	if !p.Healthy("a") {
		t.Fatal("should be unfrozen")
	}
	list := p.List()
	if list[0]["err_count"].(int) != 0 {
		t.Fatalf("err_count=%v", list[0]["err_count"])
	}
	// Unfreeze 不动 disabled。
	p.Disable("a", "token invalid")
	p.Unfreeze("a")
	if !p.IsDisabled("a") {
		t.Fatal("disabled must survive unfreeze")
	}
}

func TestGetAndHealthy(t *testing.T) {
	p := newTestPool(t, "a", "b")
	if p.Get("a") == nil || p.Get("a").UID != "a" {
		t.Fatal("Get(a) failed")
	}
	if p.Get("nope") != nil {
		t.Fatal("Get(nope) should be nil")
	}
	if !p.Healthy("a") || p.Healthy("nope") {
		t.Fatal("Healthy wrong")
	}
}

func TestNoteErrorThreshold(t *testing.T) {
	p := newTestPool(t, "a")
	p.NoteError("a", 3, 10*time.Minute)
	p.NoteError("a", 3, 10*time.Minute)
	if !p.Healthy("a") {
		t.Fatal("2 errors below threshold should stay healthy")
	}
	p.NoteError("a", 3, 10*time.Minute)
	if p.Healthy("a") {
		t.Fatal("3 errors should trigger cooldown")
	}
	p.NoteSuccess("a")
	list := p.List()
	if list[0]["err_count"].(int) != 0 {
		t.Fatalf("err_count=%v", list[0]["err_count"])
	}
	// NoteSuccess 只清计数，不清冷却。
	if p.Healthy("a") {
		t.Fatal("cooldown must survive NoteSuccess")
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	auths := []*auth.Auth{{UID: "a", AccessToken: "tok-a"}, {UID: "b", AccessToken: "tok-b"}}
	p1, err := New(auths, Config{}, state)
	if err != nil {
		t.Fatal(err)
	}
	p1.SetBalance("a", 777)
	p1.Cooldown("b", CoolSoft, time.Hour, "429")
	p1.Disable("a", "test disable")
	// Cooldown/Disable 都会落盘。

	auths2 := []*auth.Auth{{UID: "a", AccessToken: "tok-a"}, {UID: "b", AccessToken: "tok-b"}}
	p2, err := New(auths2, Config{}, state)
	if err != nil {
		t.Fatal(err)
	}
	list := p2.List()
	var a, b map[string]any
	for _, e := range list {
		switch e["uid"] {
		case "a":
			a = e
		case "b":
			b = e
		}
	}
	if a == nil || b == nil {
		t.Fatalf("list=%v", list)
	}
	if a["status"] != "disabled" || a["reason"] != "test disable" {
		t.Fatalf("a=%v", a)
	}
	if a["balance"].(int64) != 777 {
		t.Fatalf("balance=%v", a["balance"])
	}
	if b["status"] != "cooling" {
		t.Fatalf("b=%v", b)
	}
}

func TestListHasTokenFields(t *testing.T) {
	p := newTestPool(t, "a")
	list := p.List()
	if len(list) != 1 {
		t.Fatalf("list=%v", list)
	}
	if _, ok := list[0]["token_remaining"].(string); !ok {
		t.Fatalf("missing token_remaining: %v", list[0])
	}
	if exp, ok := list[0]["token_expiring"].(bool); !ok || exp {
		t.Fatalf("token_expiring=%v", list[0]["token_expiring"])
	}
	if _, ok := list[0]["token_age"].(string); !ok {
		t.Fatalf("missing token_age: %v", list[0])
	}
	// 脱敏：不得包含 token。
	for k, v := range list[0] {
		if s, ok := v.(string); ok && s == "tok-a" {
			t.Fatalf("token leaked in field %s", k)
		}
	}
}

func TestUpstreamTimeoutWired(t *testing.T) {
	auths := []*auth.Auth{{UID: "a", AccessToken: "tok-a"}}
	p, err := New(auths, Config{UpstreamTimeout: 33 * time.Second}, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.cfg.UpstreamTimeout != 33*time.Second {
		t.Fatalf("timeout=%s", p.cfg.UpstreamTimeout)
	}
	if p.accounts[0].Client == nil {
		t.Fatal("client nil")
	}
	// 未配置时走默认 120s。
	p2, err := New(auths, Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if p2.cfg.UpstreamTimeout != 120*time.Second {
		t.Fatalf("default timeout=%s", p2.cfg.UpstreamTimeout)
	}
}
