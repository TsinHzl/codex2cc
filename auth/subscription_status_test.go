package auth

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%s): %v", name, err)
	}
	return loc
}

// 业务状态按业务时区自然日判定：到期前当天=今日到期，到期瞬间=已超时 1 天，
// 之后每跨一个业务日 +1；夏令时切换日按日期分量计算不受 23/25 小时影响。
func TestComputeSubscriptionBusinessStatus(t *testing.T) {
	sh := mustLoc(t, "Asia/Shanghai")
	ny := mustLoc(t, "America/New_York")
	at := func(loc *time.Location, y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, loc)
	}

	cases := []struct {
		name        string
		loc         *time.Location
		expiresAt   time.Time
		graceUntil  time.Time
		now         time.Time
		wantStatus  string
		wantRemain  int
		wantOverdue int
	}{
		{"无到期时间", sh, time.Time{}, time.Time{}, at(sh, 2026, 9, 12, 10, 0), SubscriptionStatusUnknown, 0, 0},
		{"当天晚些时候到期=今日到期", sh, at(sh, 2026, 9, 12, 23, 59), time.Time{}, at(sh, 2026, 9, 12, 8, 0), SubscriptionStatusExpiringToday, 0, 0},
		{"次日凌晨到期=剩余 1 天", sh, at(sh, 2026, 9, 13, 0, 30), time.Time{}, at(sh, 2026, 9, 12, 23, 0), SubscriptionStatusActive, 1, 0},
		{"UTC 时刻在业务日内但 UTC 日期已变=仍算今日", sh, at(sh, 2026, 9, 12, 23, 59), time.Time{}, at(sh, 2026, 9, 12, 10, 0).In(time.UTC), SubscriptionStatusExpiringToday, 0, 0},
		{"到期瞬间=已超时 1 天", sh, at(sh, 2026, 9, 12, 12, 0), time.Time{}, at(sh, 2026, 9, 12, 12, 0), SubscriptionStatusExpired, 0, 1},
		{"到期当天稍后仍=已超时 1 天", sh, at(sh, 2026, 9, 12, 12, 0), time.Time{}, at(sh, 2026, 9, 12, 20, 0), SubscriptionStatusExpired, 0, 1},
		{"跨过一个业务日=已超时 1 天", sh, at(sh, 2026, 9, 12, 23, 59), time.Time{}, at(sh, 2026, 9, 13, 0, 1), SubscriptionStatusExpired, 0, 1},
		{"跨过两个业务日=已超时 2 天", sh, at(sh, 2026, 9, 12, 12, 0), time.Time{}, at(sh, 2026, 9, 14, 0, 1), SubscriptionStatusExpired, 0, 2},
		{"宽限期内", sh, at(sh, 2026, 9, 12, 12, 0), at(sh, 2026, 9, 20, 0, 0), at(sh, 2026, 9, 14, 0, 1), SubscriptionStatusGracePeriod, 0, 2},
		{"宽限期结束后=已过期", sh, at(sh, 2026, 9, 12, 12, 0), at(sh, 2026, 9, 13, 0, 0), at(sh, 2026, 9, 14, 0, 1), SubscriptionStatusExpired, 0, 2},
		// 2026-11-01 纽约夏令时结束（当天 25 小时）：10-31 12:00 → 11-02 12:00 实际 49 小时，仍是 2 天。
		{"夏令时结束跨日=剩余 2 天", ny, at(ny, 2026, 11, 2, 12, 0), time.Time{}, at(ny, 2026, 10, 31, 12, 0), SubscriptionStatusActive, 2, 0},
		{"夏令时结束后超时天数", ny, at(ny, 2026, 10, 31, 12, 0), time.Time{}, at(ny, 2026, 11, 2, 11, 0), SubscriptionStatusExpired, 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, remain, overdue := ComputeSubscriptionBusinessStatus(tc.expiresAt, tc.graceUntil, tc.now, tc.loc)
			if status != tc.wantStatus || remain != tc.wantRemain || overdue != tc.wantOverdue {
				t.Fatalf("got (%s, %d, %d), want (%s, %d, %d)", status, remain, overdue, tc.wantStatus, tc.wantRemain, tc.wantOverdue)
			}
		})
	}
}

// 套餐跟踪规则：付费一律跟踪；free 只在留有到期时间（真过期降级）时跟踪；api 不跟踪。
func TestBuildSubscriptionStatusView(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	past := now.Add(-48 * time.Hour)

	if v := BuildSubscriptionStatusView("api", past, SubscriptionMeta{}, now, time.UTC); v != nil {
		t.Fatalf("api plan should not be tracked, got %+v", v)
	}
	if v := BuildSubscriptionStatusView("free", time.Time{}, SubscriptionMeta{}, now, time.UTC); v != nil {
		t.Fatalf("free plan without expiry should not be tracked, got %+v", v)
	}
	v := BuildSubscriptionStatusView("free", past, SubscriptionMeta{}, now, time.UTC)
	if v == nil || v.BusinessStatus != SubscriptionStatusExpired || v.DaysOverdue != 2 {
		t.Fatalf("free plan with past expiry should be expired 2d, got %+v", v)
	}
	if v.Source != SubscriptionSourceJWT || v.SyncState != SubscriptionSyncUnknown || v.AutoRenew != SubscriptionAutoRenewUnknown {
		t.Fatalf("legacy defaults: source=%s sync=%s autoRenew=%s", v.Source, v.SyncState, v.AutoRenew)
	}

	meta := SubscriptionMeta{
		CheckedAt: now.Add(-time.Hour), SyncState: SubscriptionSyncConfirmed, Source: SubscriptionSourceProviderAPI,
		WillRenew: "true", RenewalDetectedAt: now.Add(-2 * time.Hour),
	}
	v = BuildSubscriptionStatusView("plus", now.Add(10*24*time.Hour), meta, now, time.UTC)
	if v == nil || v.BusinessStatus != SubscriptionStatusActive || v.DaysRemaining != 10 {
		t.Fatalf("plus active 10d, got %+v", v)
	}
	if v.AutoRenew != SubscriptionAutoRenewEnabled || v.LastCheckedAt == "" || v.RenewalDetectedAt == "" || v.Timezone != "UTC" {
		t.Fatalf("meta not carried: %+v", v)
	}
	// 待确认（陈旧值已清理）：无到期时间但同步状态 pending。
	v = BuildSubscriptionStatusView("plus", time.Time{}, SubscriptionMeta{SyncState: SubscriptionSyncPending, Source: SubscriptionSourcePlanHeader, LastKnownStatus: SubscriptionStatusExpired}, now, time.UTC)
	if v == nil || v.BusinessStatus != SubscriptionStatusUnknown || v.SyncState != SubscriptionSyncPending || v.LastKnownStatus != SubscriptionStatusExpired {
		t.Fatalf("pending view: %+v", v)
	}
	// 提供方无订阅记录：自动续期归为不支持。
	v = BuildSubscriptionStatusView("k12", now.Add(24*time.Hour), SubscriptionMeta{SyncState: SubscriptionSyncUnsupported}, now, time.UTC)
	if v == nil || v.AutoRenew != SubscriptionAutoRenewUnsupported {
		t.Fatalf("unsupported view: %+v", v)
	}
}

// 元数据 credentials 往返：八个键全量写入，空值写空串。
func TestSubscriptionMetaCredentialsRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	meta := SubscriptionMeta{CheckedAt: now, SyncState: SubscriptionSyncFailed, Source: SubscriptionSourceJWT, Error: "boom", LastKnownStatus: SubscriptionStatusActive, WillRenew: "false"}
	updates := meta.CredentialUpdates()
	if len(updates) != 8 {
		t.Fatalf("updates has %d keys, want 8", len(updates))
	}
	if updates[SubscriptionGraceUntilCredentialKey] != "" || updates[SubscriptionRenewalDetectedAtCredentialKey] != "" {
		t.Fatalf("zero times must serialize to empty: %+v", updates)
	}
	got := SubscriptionMetaFromCredentials(func(k string) string {
		v, _ := updates[k].(string)
		return v
	})
	if got != meta {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, meta)
	}
	// unix 秒也能读回（兼容手工/第三方写入）。
	if ts := parseSubscriptionTime("1789000000"); ts.IsZero() {
		t.Fatal("unix seconds should parse")
	}
}

// 陈旧值清理不再静默：同步状态切 pending、来源 plan_header、记录续费检测时间与
// 清理前的业务状态；宽限期内的已过去到期时间不算陈旧。
func TestClearStaleSubscriptionExpiresAt_RecordsPendingMeta(t *testing.T) {
	store := NewStore(nil, nil, nil)
	past := time.Now().Add(-72 * time.Hour)
	acc := &Account{DBID: 1, AccessToken: "at", Status: StatusReady, PlanType: "plus", SubscriptionExpiresAt: past}
	store.AddAccount(acc)

	var hooked *Account
	prev := OnStaleSubscriptionCleared
	OnStaleSubscriptionCleared = func(_ *Store, a *Account) { hooked = a }
	defer func() { OnStaleSubscriptionCleared = prev }()

	if !store.ClearStaleSubscriptionExpiresAt(acc) {
		t.Fatal("stale value should be cleared")
	}
	meta := acc.SubscriptionMetaSnapshot()
	if meta.SyncState != SubscriptionSyncPending || meta.Source != SubscriptionSourcePlanHeader {
		t.Fatalf("meta = %+v, want pending/plan_header", meta)
	}
	if meta.LastKnownStatus != SubscriptionStatusExpired || meta.RenewalDetectedAt.IsZero() {
		t.Fatalf("meta = %+v, want last_known=expired + renewal_detected_at", meta)
	}
	if hooked != acc {
		t.Fatal("OnStaleSubscriptionCleared hook should fire with the account")
	}
	view := acc.SubscriptionStatusView(time.Now())
	if view == nil || view.BusinessStatus != SubscriptionStatusUnknown || view.SyncState != SubscriptionSyncPending {
		t.Fatalf("view after clear = %+v", view)
	}

	// 宽限期内：不清理。
	grace := &Account{DBID: 2, AccessToken: "at", Status: StatusReady, PlanType: "plus", SubscriptionExpiresAt: past}
	grace.subscriptionMeta.GraceUntil = time.Now().Add(72 * time.Hour)
	store.AddAccount(grace)
	hooked = nil
	if store.ClearStaleSubscriptionExpiresAt(grace) {
		t.Fatal("expiry inside grace period must not be treated as stale")
	}
	if hooked != nil {
		t.Fatal("hook must not fire when nothing was cleared")
	}
	if got := grace.GetSubscriptionExpiresAt(); !got.Equal(past) {
		t.Fatalf("grace account expiry = %v, want %v", got, past)
	}
}

// RT 刷新：令牌里更早的到期时间不得覆盖订阅提供方给的权威值；更晚的仍照写。
func TestCodexRefreshedCredentials_JWTDoesNotOverrideAuthoritativeExpiry(t *testing.T) {
	now := time.Now()
	authoritative := now.Add(40 * 24 * time.Hour)
	acc := &Account{DBID: 1, PlanType: "plus", SubscriptionExpiresAt: authoritative}
	acc.subscriptionMeta.Source = SubscriptionSourceProviderAPI
	td := &TokenData{AccessToken: "at-new", RefreshToken: "rt-new", ExpiresAt: now.Add(time.Hour)}

	older := &AccountInfo{PlanType: "plus", SubscriptionExpiresAt: now.Add(10 * 24 * time.Hour)}
	updates := codexRefreshedCredentials(acc, td, older, "")
	if _, ok := updates["subscription_expires_at"]; ok {
		t.Fatalf("older JWT expiry must not overwrite provider value: %v", updates["subscription_expires_at"])
	}

	newer := &AccountInfo{PlanType: "plus", SubscriptionExpiresAt: now.Add(70 * 24 * time.Hour)}
	updates = codexRefreshedCredentials(acc, td, newer, "")
	if got := updates["subscription_expires_at"]; got != newer.SubscriptionExpiresAt.Format(time.RFC3339) {
		t.Fatalf("newer JWT expiry should be written, got %v", got)
	}
	if got := updates[SubscriptionSourceCredentialKey]; got != SubscriptionSourceJWT {
		t.Fatalf("source should flip to jwt when the token supplies a newer value, got %v", got)
	}

	// 无权威来源时行为不变：令牌值直接写入。
	legacy := &Account{DBID: 2, PlanType: "plus", SubscriptionExpiresAt: authoritative}
	updates = codexRefreshedCredentials(legacy, td, older, "")
	if got := updates["subscription_expires_at"]; got != older.SubscriptionExpiresAt.Format(time.RFC3339) {
		t.Fatalf("legacy account should accept JWT expiry, got %v", got)
	}
}
