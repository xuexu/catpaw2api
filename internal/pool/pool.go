// Package pool 管理 CatPaw 账号池：token 校验、余额、冷却、禁用与轮转。
package pool

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"catpaw2api/internal/auth"
	"catpaw2api/internal/upstream"
)

// CoolKind 冷却原因。
type CoolKind int

const (
	CoolSoft CoolKind = iota
	CoolErr
	CoolPlan
)

// Account 一个上游账号。
type Account struct {
	Name         string `json:"name"` // 默认 = UID
	UID          string `json:"uid"`
	UserName     string `json:"user_name"`
	DefaultModel string `json:"default_model"` // auto / 模型名

	Auth   *auth.Auth       `json:"-"`
	Client *upstream.Client `json:"-"`

	mu             sync.Mutex
	balance        int64
	lastBalanceAt  time.Time
	lastValidated  time.Time
	errCount       int
	coolUntil      time.Time
	coolKind       CoolKind
	disabled       bool
	disabledReason string
	lastErr        string
}

// Config 池配置。
type Config struct {
	ErrThreshold int
	ErrCooldown  time.Duration
	SoftCooldown time.Duration
	// UpstreamTimeout 上游 JSON 总超时 / 流式首字节超时，默认 120s。
	UpstreamTimeout time.Duration
}

// Pool 账号池。
type Pool struct {
	cfg      Config
	state    string
	mu       sync.Mutex
	accounts []*Account
}

// New 构建池：每个 auth 对应一个账号。
func New(auths []*auth.Auth, cfg Config, stateFile string) (*Pool, error) {
	if cfg.ErrThreshold <= 0 {
		cfg.ErrThreshold = 3
	}
	if cfg.ErrCooldown <= 0 {
		cfg.ErrCooldown = 10 * time.Minute
	}
	if cfg.SoftCooldown <= 0 {
		cfg.SoftCooldown = 60 * time.Second
	}
	if cfg.UpstreamTimeout <= 0 {
		cfg.UpstreamTimeout = 120 * time.Second
	}
	p := &Pool{cfg: cfg, state: stateFile}
	for _, a := range auths {
		acct := &Account{
			Name:     a.UID,
			UID:      a.UID,
			UserName: a.UserName,
			Auth:     a,
			Client:   upstream.New(cfg.UpstreamTimeout),
		}
		p.accounts = append(p.accounts, acct)
	}
	p.loadState()
	return p, nil
}

// Accounts 返回全部账号（含状态）。
func (p *Pool) Accounts() []*Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accounts
}

// Get 按名字取账号（显式 conversation 路由用），找不到返回 nil。
func (p *Pool) Get(name string) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// List 状态快照（脱敏；字段对齐 WorkBuddy 面板：nickname/credits/disabled/cooling）。
func (p *Pool) List() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, 0, len(p.accounts))
	now := time.Now()
	for _, a := range p.accounts {
		a.mu.Lock()
		cooling := !a.disabled && now.Before(a.coolUntil)
		until := ""
		if cooling {
			until = a.coolUntil.Format(time.RFC3339)
		}
		remaining := a.Auth.Remaining()
		reason := a.disabledReason
		if reason == "" {
			reason = a.lastErr
		}
		status := "healthy"
		if a.disabled {
			status = "disabled"
		} else if cooling {
			status = "cooling"
		}
		out = append(out, map[string]any{
			"name":            a.Name,
			"uid":             a.UID,
			"nickname":        a.UserName,
			"user_name":       a.UserName,
			"default_model":   a.DefaultModel,
			"credits":         a.balance,
			"balance":         a.balance,
			"disabled":        a.disabled,
			"cooling":         cooling,
			"status":          status,
			"until":           until,
			"err_count":       a.errCount,
			"reason":          reason,
			"last_error":      a.lastErr,
			"token_age":       a.Auth.Age().Round(time.Minute).String(),
			"token_remaining": remaining.Round(time.Minute).String(),
			"token_expiring":  remaining > 0 && remaining <= 24*time.Hour,
		})
		a.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["uid"].(string) < out[j]["uid"].(string) })
	return out
}

// AddAccount 动态加入账号（OAuth 登录成功后热加载）；同 UID 则覆盖 token。
func (p *Pool) AddAccount(a *auth.Auth) *Account {
	if a == nil || a.UID == "" {
		return nil
	}
	acct := &Account{
		Name:     a.UID,
		UID:      a.UID,
		UserName: a.UserName,
		Auth:     a,
		Client:   upstream.New(p.cfg.UpstreamTimeout),
	}
	p.mu.Lock()
	for _, existing := range p.accounts {
		if existing.UID == a.UID {
			existing.mu.Lock()
			existing.Auth = a
			existing.UserName = a.UserName
			existing.disabled = false
			existing.disabledReason = ""
			existing.lastErr = ""
			existing.mu.Unlock()
			p.mu.Unlock()
			p.saveState()
			return existing
		}
	}
	p.accounts = append(p.accounts, acct)
	p.mu.Unlock()
	p.saveState()
	log.Printf("pool account added uid=%s name=%s", a.UID, a.UserName)
	return acct
}

// SyncToDir 用磁盘 auths 全量对齐内存池（重载）。
func (p *Pool) SyncToDir(auths []*auth.Auth) {
	p.mu.Lock()
	defer p.mu.Unlock()
	byUID := map[string]*Account{}
	for _, a := range p.accounts {
		byUID[a.UID] = a
	}
	next := make([]*Account, 0, len(auths))
	for _, a := range auths {
		if existing, ok := byUID[a.UID]; ok {
			existing.mu.Lock()
			existing.Auth = a
			existing.UserName = a.UserName
			existing.mu.Unlock()
			next = append(next, existing)
			delete(byUID, a.UID)
		} else {
			next = append(next, &Account{
				Name:     a.UID,
				UID:      a.UID,
				UserName: a.UserName,
				Auth:     a,
				Client:   upstream.New(p.cfg.UpstreamTimeout),
			})
		}
	}
	p.accounts = next
}

// PickExcluding 挑一个健康账号：余额降序（与 traework2api/workbuddy2api 一致），
// 余额相同按 UID 字典序保证稳定。
func (p *Pool) PickExcluding(tried map[string]bool) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	type cand struct {
		a       *Account
		balance int64
	}
	var cands []cand
	for _, a := range p.accounts {
		if tried != nil && tried[a.Name] {
			continue
		}
		a.mu.Lock()
		healthy := !a.disabled && now.After(a.coolUntil)
		b := a.balance
		a.mu.Unlock()
		if healthy {
			cands = append(cands, cand{a, b})
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].balance != cands[j].balance {
			return cands[i].balance > cands[j].balance
		}
		return cands[i].a.UID < cands[j].a.UID
	})
	return cands[0].a
}

// Validate 校验 token（带 5 分钟缓存）；401/403 禁用账号。
func (p *Pool) Validate(a *Account) (bool, error) {
	a.mu.Lock()
	if time.Since(a.lastValidated) < 5*time.Minute {
		ok := !a.disabled
		a.mu.Unlock()
		return ok, nil
	}
	a.mu.Unlock()
	
	// Ping to validate token
	ok, err := a.Client.Ping(context.Background(), a.Auth.Token())
	if err != nil {
		a.mu.Lock()
		a.disabled = true
		a.disabledReason = "token invalid (network error)"
		a.lastErr = err.Error()
		a.mu.Unlock()
		return false, err
	}
	if !ok {
		a.mu.Lock()
		a.disabled = true
		a.disabledReason = "token invalid (401/403)"
		a.lastErr = "token invalid"
		a.mu.Unlock()
		return false, nil
	}
	
	a.mu.Lock()
	a.lastValidated = time.Now()
	a.mu.Unlock()
	return true, nil
}

// Cooldown 冷却账号。
func (p *Pool) Cooldown(name string, kind CoolKind, dur time.Duration, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name != name {
			continue
		}
		a.mu.Lock()
		a.coolUntil = time.Now().Add(dur)
		a.coolKind = kind
		a.lastErr = reason
		a.mu.Unlock()
		log.Printf("pool cooldown account=%s kind=%d dur=%s reason=%s", name, kind, dur, reason)
	}
	p.saveState()
}

// NoteError 累计错误。
func (p *Pool) NoteError(name string, threshold int, cooldown time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name != name {
			continue
		}
		a.mu.Lock()
		a.errCount++
		if a.errCount >= threshold {
			a.coolUntil = time.Now().Add(cooldown)
			a.lastErr = "consecutive errors"
		}
		a.mu.Unlock()
	}
	p.saveState()
}

// NoteSuccess 清零错误计数。
func (p *Pool) NoteSuccess(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name == name {
			a.mu.Lock()
			a.errCount = 0
			a.mu.Unlock()
		}
	}
}

// Unfreeze 解冻账号：清除冷却与错误计数（不动 disabled 状态）。
// 用于额度恢复后自动解冻（对齐 traework2api 签到解冻语义）。
func (p *Pool) Unfreeze(name string) {
	p.mu.Lock()
	changed := false
	for _, a := range p.accounts {
		if a.Name != name {
			continue
		}
		a.mu.Lock()
		if time.Now().Before(a.coolUntil) || a.errCount > 0 {
			a.coolUntil = time.Time{}
			a.errCount = 0
			a.lastErr = ""
			changed = true
			log.Printf("pool unfreeze account=%s", name)
		}
		a.mu.Unlock()
	}
	p.mu.Unlock()
	if changed {
		p.saveState()
	}
}

// Healthy 查询账号当前可用（未禁用且不在冷却中）。
func (p *Pool) Healthy(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name == name {
			a.mu.Lock()
			healthy := !a.disabled && time.Now().After(a.coolUntil)
			a.mu.Unlock()
			return healthy
		}
	}
	return false
}

// IsDisabled 查询账号是否已禁用（幂等巡检用，避免重复禁用刷日志）。
func (p *Pool) IsDisabled(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name == name {
			a.mu.Lock()
			d := a.disabled
			a.mu.Unlock()
			return d
		}
	}
	return false
}

// Disable 禁用账号。
func (p *Pool) Disable(name, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name == name {
			a.mu.Lock()
			a.disabled = true
			a.disabledReason = reason
			a.lastErr = reason
			a.mu.Unlock()
			log.Printf("pool disable account=%s reason=%s", name, reason)
		}
	}
	p.saveState()
}

// Enable 解冻账号（可选设置余额）。
func (p *Pool) Enable(name string, balance int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name == name {
			a.mu.Lock()
			a.disabled = false
			a.disabledReason = ""
			a.lastErr = ""
			a.balance = balance
			a.mu.Unlock()
			log.Printf("pool enable account=%s", name)
		}
	}
	p.saveState()
}

// ClearCooldown 清冷却。
func (p *Pool) ClearCooldown(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name == name {
			a.mu.Lock()
			a.coolUntil = time.Time{}
			a.coolKind = CoolSoft
			a.mu.Unlock()
			log.Printf("pool clear cooldown account=%s", name)
		}
	}
	p.saveState()
}

// Stats 汇总账号状态。
func (p *Pool) Stats() (total, healthy, disabled, cooling int, credits int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		total++
		credits += a.balance
		if a.disabled {
			disabled++
			continue
		}
		if a.coolUntil.After(time.Now()) {
			cooling++
			continue
		}
		healthy++
	}
	return
}

// SetBalance 更新余额缓存。
func (p *Pool) SetBalance(name string, balance int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.Name == name {
			a.mu.Lock()
			a.balance = balance
			a.lastBalanceAt = time.Now()
			a.mu.Unlock()
		}
	}
}

// loadState / saveState 持久化冷却与余额。
func (p *Pool) loadState() {
	if p.state == "" {
		return
	}
	raw, err := os.ReadFile(p.state)
	if err != nil {
		return
	}
	var data []struct {
		Name      string    `json:"name"`
		ErrCount  int       `json:"err_count"`
		CoolUntil time.Time `json:"cool_until"`
		Disabled  bool      `json:"disabled"`
		Reason    string    `json:"reason"`
		LastErr   string    `json:"last_error"`
		Balance   int64     `json:"balance"`
		BalanceAt time.Time `json:"balance_at"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}
	for _, a := range p.accounts {
		for _, s := range data {
			if s.Name == a.Name {
				a.mu.Lock()
				a.errCount = s.ErrCount
				a.coolUntil = s.CoolUntil
				a.disabled = s.Disabled
				a.disabledReason = s.Reason
				a.lastErr = s.LastErr
				a.balance = s.Balance
				a.lastBalanceAt = s.BalanceAt
				a.mu.Unlock()
			}
		}
	}
}

func (p *Pool) saveState() {
	if p.state == "" {
		return
	}
	type entry struct {
		Name      string    `json:"name"`
		ErrCount  int       `json:"err_count"`
		CoolUntil time.Time `json:"cool_until"`
		Disabled  bool      `json:"disabled"`
		Reason    string    `json:"reason"`
		LastErr   string    `json:"last_error"`
		Balance   int64     `json:"balance"`
		BalanceAt time.Time `json:"balance_at"`
	}
	out := make([]entry, 0, len(p.accounts))
	for _, a := range p.accounts {
		a.mu.Lock()
		out = append(out, entry{
			Name:      a.Name,
			ErrCount:  a.errCount,
			CoolUntil: a.coolUntil,
			Disabled:  a.disabled,
			Reason:    a.disabledReason,
			LastErr:   a.lastErr,
			Balance:   a.balance,
			BalanceAt: a.lastBalanceAt,
		})
		a.mu.Unlock()
	}
	raw, _ := json.MarshalIndent(out, "", "  ")
	tmp := p.state + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p.state)
}
