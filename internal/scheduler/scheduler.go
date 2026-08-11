// Package scheduler 提供余额看门狗 + 自动申请额度 + token 自动续期。
//
// 核对结论（逆向 2026.0729.1956 桌面端 + credit.catpaw.meituan.com）：
// CatPaw 没有每日签到接口；免费额度来自「注册奖励」活动：
// POST /api/gateway/credit/register（registerChannel=CATPAW_PC）→
// data.registrationBonus（新用户一次性发放，活动截止 2026-08-27）。
// token 无 refresh_token，有效期 72h，到期只能重新走 OAuth 登录。
// 本调度器：
//  1. 启动时对每个账号调用 register 领取注册奖励（幂等，老用户 newUser=false）
//  2. 周期性查余额，低于阈值时按配置自动申请（register / campaign / none）
//  3. token 即将过期时自动发起 OAuth session，轮询 poll-token 等待新 token
//     （需一个有美团登录态的浏览器打开 auth_url 完成静默续期）
package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"catpaw2api/internal/auth"
	"catpaw2api/internal/pool"
	"catpaw2api/internal/upstream"
)

// Config 调度器配置。
type Config struct {
	Pool            *pool.Pool
	Enabled         bool
	PollInterval    time.Duration // 余额轮询周期，默认 10m
	ApplyThreshold  int64         // 低于该值触发申请，默认 50
	ApplyMethod     string        // register | campaign | none
	ApplyCooldown   time.Duration // 两次申请最小间隔，默认 6h
	RegisterOnStart bool          // 启动时领取注册奖励
	// AutoRenew token 自动续期：剩余低于 RenewThreshold 时发起 OAuth。
	AutoRenew       bool          // 是否启用自动续期
	RenewThreshold  time.Duration // 触发续期的剩余时间阈值，默认 6h
	RenewWaitMax    time.Duration // 单次续期轮询最长等待，默认 15m
	AuthDir         string        // auths 目录（续期成功后落盘）
}

// Scheduler 定时任务。
type Scheduler struct {
	cfg       Config
	mu        sync.Mutex
	lastApply map[string]time.Time

	// renewMu 保护 renewing 映射
	renewMu     sync.Mutex
	renewing    map[string]bool          // account → 是否正在续期
	renewStatus map[string]*RenewStatus  // account → 续期状态
}

// RenewStatus 单个账号的续期状态（供 WebUI 展示）。
type RenewStatus struct {
	Account   string    `json:"account"`
	UID       string    `json:"uid"`
	AuthURL   string    `json:"auth_url,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Done      bool      `json:"done"`
	Error     string    `json:"error,omitempty"`
}

// New 构造调度器。
func New(cfg Config) *Scheduler {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 10 * time.Minute
	}
	if cfg.ApplyThreshold <= 0 {
		cfg.ApplyThreshold = 50
	}
	if cfg.ApplyMethod == "" {
		cfg.ApplyMethod = "register"
	}
	if cfg.ApplyCooldown <= 0 {
		cfg.ApplyCooldown = 6 * time.Hour
	}
	if cfg.RenewThreshold <= 0 {
		cfg.RenewThreshold = 6 * time.Hour
	}
	if cfg.RenewWaitMax <= 0 {
		cfg.RenewWaitMax = 15 * time.Minute
	}
	return &Scheduler{
		cfg:         cfg,
		lastApply:   map[string]time.Time{},
		renewing:    map[string]bool{},
		renewStatus: map[string]*RenewStatus{},
	}
}

// Run 启动定时循环。
func (s *Scheduler) Run(ctx context.Context) {
	if s.cfg.RegisterOnStart {
		s.RegisterAll(ctx)
	}
	if !s.cfg.Enabled {
		log.Printf("quota watchdog disabled")
		return
	}
	log.Printf("quota watchdog enabled: poll=%s threshold=%d method=%s cooldown=%s auto_renew=%v renew_threshold=%s",
		s.cfg.PollInterval, s.cfg.ApplyThreshold, s.cfg.ApplyMethod, s.cfg.ApplyCooldown,
		s.cfg.AutoRenew, s.cfg.RenewThreshold)
	// 启动时立即跑一次续期巡检（不等第一个 ticker），让快过期的 token 尽早发起续期。
	s.renewSweep(ctx)
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

// Tick 遍历全部账号：token 续期巡检 → 到期巡检 → 查余额 → 低于阈值自动申请 →
// 余额归零的账号进 CoolPlan 冷却（下一个轮询周期重查），余额恢复的账号自动解冻。
func (s *Scheduler) Tick(ctx context.Context) {
	s.renewSweep(ctx)
	s.expirySweep()
	for _, acct := range s.cfg.Pool.Accounts() {
		n, err := s.refreshBalance(ctx, acct)
		if err != nil {
			var ae *upstream.ApiError
			if ok := asAPI(err, &ae); ok && (ae.Status == 401 || ae.Code == 401) {
				s.cfg.Pool.Disable(acct.Name, "401 on credit balance")
			}
			log.Printf("quota balance account=%s error: %v", acct.Name, err)
			continue
		}
		switch {
		case n <= 0:
			// 余额归零：先试申请，再重查；仍为零则冷却一个轮询周期，避免空转打满错误计数。
			s.maybeApply(ctx, acct)
			if n2, err2 := s.refreshBalance(ctx, acct); err2 == nil && n2 > 0 {
				s.cfg.Pool.Unfreeze(acct.Name)
			} else {
				s.cfg.Pool.Cooldown(acct.Name, pool.CoolPlan, s.cfg.PollInterval, "balance exhausted")
			}
		case n < s.cfg.ApplyThreshold:
			s.maybeApply(ctx, acct)
			s.cfg.Pool.Unfreeze(acct.Name)
		default:
			s.cfg.Pool.Unfreeze(acct.Name)
		}
	}
}

// refreshBalance 拉一次余额并写回池缓存。
func (s *Scheduler) refreshBalance(ctx context.Context, acct *pool.Account) (int64, error) {
	balance, err := acct.Client.CreditBalance(ctx, acct.Auth.Token())
	if err != nil {
		return 0, err
	}
	n := upstream.ParseCredits(balance)
	s.cfg.Pool.SetBalance(acct.Name, n)
	log.Printf("quota balance account=%s credits=%d", acct.Name, n)
	return n, nil
}

// expirySweep 按 72h 推算 token 寿命：已过期直接禁用（不等 401），
// 24h 内将过期打告警日志提醒重登。
func (s *Scheduler) expirySweep() {
	for _, acct := range s.cfg.Pool.Accounts() {
		switch {
		case acct.Auth.Expired():
			if !s.cfg.Pool.IsDisabled(acct.Name) {
				s.cfg.Pool.Disable(acct.Name, "token expired (72h), re-login via catpaw2api-login")
			}
		case acct.Auth.ExpiringSoon(24 * time.Hour):
			log.Printf("quota token account=%s expiring soon (remaining=%s), run catpaw2api-login to refresh",
				acct.Name, acct.Auth.Remaining().Round(time.Minute))
		}
	}
}

// renewSweep 检测 token 即将过期（< RenewThreshold）的账号，自动发起 OAuth 续期。
//
// CatPaw 无 refresh_token，续期 = 重新走 OAuth login：
//  1. GET login-config → loginEntryUrl
//  2. 生成 sid/state，拼 auth_url（带 sid）
//  3. 轮询 poll-token?sid= 等待新 token（需浏览器打开 auth_url 完成登录）
//
// 服务器无浏览器，因此：
// - auth_url 记录到 RenewStatus 供 WebUI 展示（用户可点链接完成）
// - 若用户的浏览器保持美团登录态，打开 auth_url 会静默拿到 token
// - 轮询在后台进行，成功后自动更新 pool + 落盘
func (s *Scheduler) renewSweep(ctx context.Context) {
	if !s.cfg.AutoRenew {
		return
	}
	for _, acct := range s.cfg.Pool.Accounts() {
		if acct.Auth == nil {
			continue
		}
		remaining := acct.Auth.Remaining()
		if remaining <= 0 || remaining > s.cfg.RenewThreshold {
			continue
		}
		// 已在续期中则跳过
		s.renewMu.Lock()
		if s.renewing[acct.Name] {
			s.renewMu.Unlock()
			continue
		}
		s.renewing[acct.Name] = true
		s.renewMu.Unlock()

		log.Printf("renew account=%s token expiring soon (remaining=%s), starting auto-renew",
			acct.Name, remaining.Round(time.Minute))
		go s.renewAccount(ctx, acct)
	}
}

// renewAccount 执行单个账号的 OAuth 续期（后台 goroutine）。
func (s *Scheduler) renewAccount(ctx context.Context, acct *pool.Account) {
	defer func() {
		s.renewMu.Lock()
		delete(s.renewing, acct.Name)
		s.renewMu.Unlock()
	}()

	client := upstream.New(30 * time.Second)

	// 1. login-config
	loginEntry, err := client.LoginConfig(ctx)
	if err != nil {
		s.setRenewStatus(acct, "", false, "login-config 失败: "+err.Error())
		log.Printf("renew account=%s login-config error: %v", acct.Name, err)
		return
	}

	// 2. 拼 auth_url
	sid := randHex(16)
	state := randHex(16)
	u, err := url.Parse(loginEntry)
	if err != nil {
		s.setRenewStatus(acct, "", false, "解析 loginEntryUrl 失败: "+err.Error())
		return
	}
	q := u.Query()
	q.Set("state", state)
	q.Set("redirect", "http://127.0.0.1:37890/callback")
	q.Set("sid", sid)
	u.RawQuery = q.Encode()
	authURL := u.String()

	s.setRenewStatus(acct, authURL, false, "")
	log.Printf("renew account=%s auth_url=%s", acct.Name, authURL)

	// 3. 轮询 poll-token（最长等待 RenewWaitMax）
	renewCtx, cancel := context.WithTimeout(ctx, s.cfg.RenewWaitMax)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-renewCtx.Done():
			s.setRenewStatus(acct, authURL, false, "续期超时（未在期限内完成浏览器登录）")
			log.Printf("renew account=%s timeout (no browser login in %s)", acct.Name, s.cfg.RenewWaitMax)
			return
		case <-ticker.C:
			tok, err := client.PollToken(renewCtx, sid)
			if err != nil || tok == "" {
				continue
			}
			// 4. 拿到新 token：更新 pool + 落盘
			newAuth := auth.New(acct.UID, acct.UserName, tok)
			if s.cfg.AuthDir != "" {
				if err := auth.SaveNew(s.cfg.AuthDir, newAuth); err != nil {
					s.setRenewStatus(acct, authURL, false, "落盘失败: "+err.Error())
					log.Printf("renew account=%s save error: %v", acct.Name, err)
					return
				}
			}
			// 更新 pool 里的 token（AddAccount 同 UID 会覆盖 token）
			s.cfg.Pool.AddAccount(newAuth)
			// 解除禁用状态
			s.cfg.Pool.Enable(acct.Name, acct.Auth.Remaining().Round(time.Minute).Milliseconds()/int64(time.Minute))
			s.setRenewStatus(acct, authURL, true, "")
			log.Printf("renew account=%s SUCCESS new token remaining=72h", acct.Name)
			return
		}
	}
}

// setRenewStatus 更新续期状态（供 WebUI 查询）。
func (s *Scheduler) setRenewStatus(acct *pool.Account, authURL string, done bool, errMsg string) {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	s.renewStatus[acct.Name] = &RenewStatus{
		Account:   acct.Name,
		UID:       acct.UID,
		AuthURL:   authURL,
		StartedAt: time.Now(),
		Done:      done,
		Error:     errMsg,
	}
}

// RenewStatuses 返回所有账号的续期状态快照（供 WebUI 展示）。
func (s *Scheduler) RenewStatuses() []RenewStatus {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	out := make([]RenewStatus, 0, len(s.renewStatus))
	for _, st := range s.renewStatus {
		out = append(out, *st)
	}
	return out
}

func randHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())[:n]
	}
	return hex.EncodeToString(b)[:n]
}

// RegisterAll 启动时为每个账号领取注册奖励（幂等）。
func (s *Scheduler) RegisterAll(ctx context.Context) {
	for _, acct := range s.cfg.Pool.Accounts() {
		res, err := acct.Client.Register(ctx, acct.Auth.Token())
		if err != nil {
			log.Printf("quota register account=%s error: %v", acct.Name, err)
			continue
		}
		if res.NewUser {
			log.Printf("quota register account=%s NEW_USER bonus=%d expire_days=%d valid_until=%s",
				acct.Name, res.RegistrationBonus, res.RewardExpireDays, res.RewardLastValidDate)
		} else {
			log.Printf("quota register account=%s already registered (bonus=%d)", acct.Name, res.RegistrationBonus)
		}
	}
}

// maybeApply 按冷却与配置执行申请动作。
func (s *Scheduler) maybeApply(ctx context.Context, acct *pool.Account) {
	s.mu.Lock()
	last, seen := s.lastApply[acct.Name]
	if seen && time.Since(last) < s.cfg.ApplyCooldown {
		s.mu.Unlock()
		log.Printf("quota apply account=%s skipped (cooldown until %s)", acct.Name, last.Add(s.cfg.ApplyCooldown).Format(time.RFC3339))
		return
	}
	s.lastApply[acct.Name] = time.Now()
	s.mu.Unlock()

	switch s.cfg.ApplyMethod {
	case "register":
		res, err := acct.Client.Register(ctx, acct.Auth.Token())
		if err != nil {
			log.Printf("quota apply account=%s register failed: %v", acct.Name, err)
			return
		}
		log.Printf("quota apply account=%s register done newUser=%v bonus=%d", acct.Name, res.NewUser, res.RegistrationBonus)
	case "campaign":
		out, err := acct.Client.CampaignInit(ctx, acct.Auth.Token())
		if err != nil {
			log.Printf("quota apply account=%s campaign failed: %v", acct.Name, err)
			return
		}
		log.Printf("quota apply account=%s campaign done data=%v", acct.Name, out)
	case "none":
		log.Printf("quota apply account=%s below threshold but apply_method=none (manual action required)", acct.Name)
	default:
		log.Printf("quota apply account=%s unknown apply_method=%q", acct.Name, s.cfg.ApplyMethod)
	}
}

func asAPI(err error, target **upstream.ApiError) bool {
	if ae, ok := err.(*upstream.ApiError); ok {
		*target = ae
		return true
	}
	return false
}
