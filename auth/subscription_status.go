package auth

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"
)

// 订阅状态拆成两层：业务状态（active / expiring_today / expired ...）由到期时间
// 按业务时区计算；同步状态（confirmed / pending / failed ...）描述最近一次查询
// 的结果与数据新鲜度。两者分开保存，查询失败时仍能保留最后一次已知业务状态。
//
// 元数据落在账号 credentials 键值里，
// 不需要建表迁移。

// 订阅元数据 credentials 键。
const (
	SubscriptionCheckedAtCredentialKey         = "subscription_checked_at"
	SubscriptionSyncStateCredentialKey         = "subscription_sync_state"
	SubscriptionSourceCredentialKey            = "subscription_source"
	SubscriptionErrorCredentialKey             = "subscription_error"
	SubscriptionLastKnownStatusCredentialKey   = "subscription_last_known_status"
	SubscriptionRenewalDetectedAtCredentialKey = "subscription_renewal_detected_at"
	SubscriptionWillRenewCredentialKey         = "subscription_will_renew"
	SubscriptionGraceUntilCredentialKey        = "subscription_grace_until"
)

// 同步状态。
const (
	// SubscriptionSyncConfirmed 最近一次查询成功，业务状态已由订阅提供方确认。
	SubscriptionSyncConfirmed = "confirmed"
	// SubscriptionSyncPending 已观测到续费迹象（付费套餐仍在用而旧到期时间已过去）
	// 或用户已发起刷新，权威到期时间尚未拿到。
	SubscriptionSyncPending = "pending"
	// SubscriptionSyncFailed 最近一次查询失败，但仍有最后一次已知业务状态。
	SubscriptionSyncFailed = "failed"
	// SubscriptionSyncUnknown 从未成功查询过，或没有任何可用状态。
	SubscriptionSyncUnknown = "unknown"
	// SubscriptionSyncUnsupported 订阅提供方对该工作区没有订阅记录（k12/edu 等
	// 教育/团队计划），只能依赖登录令牌里的到期时间。
	SubscriptionSyncUnsupported = "unsupported"
)

// 到期时间来源。
const (
	SubscriptionSourceJWT         = "jwt"
	SubscriptionSourcePlanHeader  = "plan_header"
	SubscriptionSourceProviderAPI = "provider_api"
)

// 业务状态。
const (
	SubscriptionStatusActive        = "active"
	SubscriptionStatusExpiringToday = "expiring_today"
	SubscriptionStatusExpired       = "expired"
	SubscriptionStatusGracePeriod   = "grace_period"
	SubscriptionStatusUnknown       = "unknown"
)

// 自动续期状态：四值语义，不用 false 同时表示关闭和不支持。
const (
	SubscriptionAutoRenewEnabled     = "enabled"
	SubscriptionAutoRenewDisabled    = "disabled"
	SubscriptionAutoRenewUnsupported = "unsupported"
	SubscriptionAutoRenewUnknown     = "unknown"
)

// SubscriptionMeta 是订阅同步元数据（持久化在 credentials 里）。
type SubscriptionMeta struct {
	// CheckedAt 最近一次订阅查询的时间（无论成败），也用于探针节流。
	CheckedAt time.Time
	SyncState string
	Source    string
	// Error 最近一次查询失败原因；成功时清空。
	Error string
	// LastKnownStatus 最近一次确认过的业务状态，查询失败/陈旧值被清理后保留。
	LastKnownStatus string
	// RenewalDetectedAt 最近一次检测到有效期变化（续费）的时间，仅作事件信息。
	RenewalDetectedAt time.Time
	// WillRenew 自动续期状态：enabled / disabled / unsupported / unknown。
	WillRenew string
	// GraceUntil 宽限期结束时间（订阅提供方返回），零值表示无宽限期。
	GraceUntil time.Time
}

// SubscriptionMetaFromCredentials 从 credentials 读取元数据；get 为按键取值函数。
func SubscriptionMetaFromCredentials(get func(string) string) SubscriptionMeta {
	if get == nil {
		return SubscriptionMeta{}
	}
	return SubscriptionMeta{
		CheckedAt:         parseSubscriptionTime(get(SubscriptionCheckedAtCredentialKey)),
		SyncState:         strings.TrimSpace(get(SubscriptionSyncStateCredentialKey)),
		Source:            strings.TrimSpace(get(SubscriptionSourceCredentialKey)),
		Error:             strings.TrimSpace(get(SubscriptionErrorCredentialKey)),
		LastKnownStatus:   strings.TrimSpace(get(SubscriptionLastKnownStatusCredentialKey)),
		RenewalDetectedAt: parseSubscriptionTime(get(SubscriptionRenewalDetectedAtCredentialKey)),
		WillRenew:         strings.TrimSpace(get(SubscriptionWillRenewCredentialKey)),
		GraceUntil:        parseSubscriptionTime(get(SubscriptionGraceUntilCredentialKey)),
	}
}

// CredentialUpdates 生成写库用的 credentials 更新（全量八个键，空值写空串以便清理）。
func (m SubscriptionMeta) CredentialUpdates() map[string]interface{} {
	return map[string]interface{}{
		SubscriptionCheckedAtCredentialKey:         formatSubscriptionTime(m.CheckedAt),
		SubscriptionSyncStateCredentialKey:         m.SyncState,
		SubscriptionSourceCredentialKey:            m.Source,
		SubscriptionErrorCredentialKey:             m.Error,
		SubscriptionLastKnownStatusCredentialKey:   m.LastKnownStatus,
		SubscriptionRenewalDetectedAtCredentialKey: formatSubscriptionTime(m.RenewalDetectedAt),
		SubscriptionWillRenewCredentialKey:         m.WillRenew,
		SubscriptionGraceUntilCredentialKey:        formatSubscriptionTime(m.GraceUntil),
	}
}

func parseSubscriptionTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil && secs > 0 {
		return time.Unix(secs, 0).UTC()
	}
	return time.Time{}
}

func formatSubscriptionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// SubscriptionStatusView 是下发给管理端/前端的订阅状态对象。
type SubscriptionStatusView struct {
	BusinessStatus string `json:"business_status"`
	Plan           string `json:"plan,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	// DaysRemaining 按业务时区自然日计的剩余天数（active 时有值；expiring_today 为 0）。
	DaysRemaining int `json:"days_remaining"`
	// DaysOverdue 按业务时区自然日计的超时天数（expired / grace_period 时 ≥1）。
	DaysOverdue       int    `json:"days_overdue"`
	LastKnownStatus   string `json:"last_known_status,omitempty"`
	AutoRenew         string `json:"auto_renew"`
	GraceUntil        string `json:"grace_until,omitempty"`
	LastCheckedAt     string `json:"last_checked_at,omitempty"`
	Source            string `json:"source,omitempty"`
	SyncState         string `json:"sync_state"`
	RenewalDetectedAt string `json:"renewal_detected_at,omitempty"`
	Error             string `json:"error,omitempty"`
	Timezone          string `json:"timezone"`
}

// SubscriptionPlanTracked 判断该套餐是否需要订阅状态跟踪：
// 付费套餐一律跟踪；free 套餐仅在留有到期时间时跟踪（付费到期后降级为 free，
// 这时到期时间就是"真过期"的证据，不能因为降级而丢掉过期展示）。
func SubscriptionPlanTracked(planType string, expiresAt time.Time) bool {
	plan := strings.ToLower(strings.TrimSpace(planType))
	switch plan {
	case "", "api":
		return false
	case "free":
		return !expiresAt.IsZero()
	}
	return true
}

// businessDate 取业务时区下的自然日（当天零点）。
func businessDate(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// businessDayDiff 计算两个业务日之间相差的自然日数（later - earlier）。
// 按日期分量相减而不是除以 24h，夏令时切换日不会少算/多算一天。
func businessDayDiff(later, earlier time.Time) int {
	l := time.Date(later.Year(), later.Month(), later.Day(), 0, 0, 0, 0, time.UTC)
	e := time.Date(earlier.Year(), earlier.Month(), earlier.Day(), 0, 0, 0, 0, time.UTC)
	return int(l.Sub(e).Hours() / 24)
}

// ComputeSubscriptionBusinessStatus 只算业务状态与天数，供 ClearStale 等内部路径
// 记录 LastKnownStatus 时复用。
func ComputeSubscriptionBusinessStatus(expiresAt time.Time, graceUntil time.Time, now time.Time, loc *time.Location) (status string, daysRemaining int, daysOverdue int) {
	if expiresAt.IsZero() {
		return SubscriptionStatusUnknown, 0, 0
	}
	if loc == nil {
		loc = time.Local
	}
	today := businessDate(now, loc)
	expiryDay := businessDate(expiresAt, loc)
	if expiresAt.After(now) {
		diff := businessDayDiff(expiryDay, today)
		if diff <= 0 {
			return SubscriptionStatusExpiringToday, 0, 0
		}
		return SubscriptionStatusActive, diff, 0
	}
	overdue := businessDayDiff(today, expiryDay)
	if overdue < 1 {
		overdue = 1
	}
	if !graceUntil.IsZero() && graceUntil.After(now) {
		return SubscriptionStatusGracePeriod, 0, overdue
	}
	return SubscriptionStatusExpired, 0, overdue
}

// BuildSubscriptionStatusView 组装订阅状态对象；不需要跟踪的套餐返回 nil。
func BuildSubscriptionStatusView(planType string, expiresAt time.Time, meta SubscriptionMeta, now time.Time, loc *time.Location) *SubscriptionStatusView {
	if !SubscriptionPlanTracked(planType, expiresAt) {
		return nil
	}
	if loc == nil {
		loc = time.Local
	}
	status, remaining, overdue := ComputeSubscriptionBusinessStatus(expiresAt, meta.GraceUntil, now, loc)

	syncState := meta.SyncState
	if syncState == "" {
		syncState = SubscriptionSyncUnknown
	}
	source := meta.Source
	if source == "" && !expiresAt.IsZero() {
		// 历史数据：到期时间只可能来自登录令牌。
		source = SubscriptionSourceJWT
	}
	autoRenew := SubscriptionAutoRenewUnknown
	switch strings.ToLower(strings.TrimSpace(meta.WillRenew)) {
	case "true", SubscriptionAutoRenewEnabled:
		autoRenew = SubscriptionAutoRenewEnabled
	case "false", SubscriptionAutoRenewDisabled:
		autoRenew = SubscriptionAutoRenewDisabled
	case SubscriptionAutoRenewUnsupported:
		autoRenew = SubscriptionAutoRenewUnsupported
	}
	if syncState == SubscriptionSyncUnsupported {
		autoRenew = SubscriptionAutoRenewUnsupported
	}

	return &SubscriptionStatusView{
		BusinessStatus:    status,
		Plan:              strings.ToLower(strings.TrimSpace(planType)),
		ExpiresAt:         formatSubscriptionTime(expiresAt),
		DaysRemaining:     remaining,
		DaysOverdue:       overdue,
		LastKnownStatus:   meta.LastKnownStatus,
		AutoRenew:         autoRenew,
		GraceUntil:        formatSubscriptionTime(meta.GraceUntil),
		LastCheckedAt:     formatSubscriptionTime(meta.CheckedAt),
		Source:            source,
		SyncState:         syncState,
		RenewalDetectedAt: formatSubscriptionTime(meta.RenewalDetectedAt),
		Error:             meta.Error,
		Timezone:          loc.String(),
	}
}

// SubscriptionMeta 返回账号当前的订阅元数据快照。
func (a *Account) SubscriptionMetaSnapshot() SubscriptionMeta {
	if a == nil {
		return SubscriptionMeta{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.subscriptionMeta
}

// SubscriptionStatusView 按当前时间与业务时区组装账号的订阅状态对象。
func (a *Account) SubscriptionStatusView(now time.Time) *SubscriptionStatusView {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	plan, expiresAt, meta := a.PlanType, a.SubscriptionExpiresAt, a.subscriptionMeta
	a.mu.RUnlock()
	return BuildSubscriptionStatusView(plan, expiresAt, meta, now, time.Local)
}

// UpdateSubscriptionMeta 原子修改账号订阅元数据并持久化（全量八键）。
// mutate 在账号锁内执行，只能改 meta 本身。
func (s *Store) UpdateSubscriptionMeta(acc *Account, mutate func(m *SubscriptionMeta)) {
	if s == nil || acc == nil || mutate == nil {
		return
	}
	acc.mu.Lock()
	mutate(&acc.subscriptionMeta)
	snapshot := acc.subscriptionMeta
	acc.mu.Unlock()
	s.persistSubscriptionMeta(acc.DBID, snapshot, nil)
}

// persistSubscriptionMeta 把元数据（可附带额外 credentials 键）写库；失败只记日志。
func (s *Store) persistSubscriptionMeta(dbID int64, meta SubscriptionMeta, extra map[string]interface{}) {
	if s == nil || s.db == nil || dbID <= 0 {
		return
	}
	updates := meta.CredentialUpdates()
	for k, v := range extra {
		updates[k] = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.db.UpdateCredentials(ctx, dbID, updates); err != nil {
		log.Printf("[账号 %d] 持久化订阅同步元数据失败: %v", dbID, err)
	}
}

// GetSubscriptionExpiresAt 返回账号当前记录的订阅到期时间（零值表示未知）。
func (a *Account) GetSubscriptionExpiresAt() time.Time {
	if a == nil {
		return time.Time{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.SubscriptionExpiresAt
}

// BeginSubscriptionSync 尝试占用异步订阅同步槽；已有在途同步时返回 false。
func (a *Account) BeginSubscriptionSync() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.subscriptionSyncInFlight {
		return false
	}
	a.subscriptionSyncInFlight = true
	return true
}

// EndSubscriptionSync 释放异步订阅同步槽。
func (a *Account) EndSubscriptionSync() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.subscriptionSyncInFlight = false
	a.mu.Unlock()
}
