package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/database"
	"github.com/codex2cc/proxy"
	"github.com/gin-gonic/gin"
)

const subscriptionTestWorkspaceUUID = "288c5d93-a113-4ed3-b6a9-08b6a4d35417"

func runSubscriptionRefresh(t *testing.T, handler *Handler, id string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/accounts/"+id+"/subscription/refresh", nil)
	handler.RefreshAccountSubscription(c)
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response (%d): %v: %s", rec.Code, err, rec.Body.String())
	}
	return rec, payload
}

// 手动刷新：绕过后台 6 小时节流真去查一次；30 秒内再点返回 429 + retry_after；
// 响应带结果分类与服务端计算的订阅状态对象。
func TestRefreshAccountSubscription_ManualRefreshAndRateLimit(t *testing.T) {
	activeUntil := time.Now().Add(20 * 24 * time.Hour).UTC().Truncate(time.Second)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"plus","active_until":"` + activeUntil.Format(time.RFC3339) + `","will_renew":true}`))
	}))
	defer server.Close()
	defer proxy.SetSubscriptionsURLForTest(server.URL)()

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 31, AccessToken: "token", PlanType: "plus", AccountID: subscriptionTestWorkspaceUUID, Status: auth.StatusReady}
	// 模拟后台探针 1 小时前查过：后台节流窗口内，但手动刷新不受它限制。
	account.MarkSubscriptionExpiryProbed(time.Now().Add(-time.Hour))
	store.AddAccount(account)
	handler := &Handler{store: store}

	rec, payload := runSubscriptionRefresh(t, handler, "31")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if payload["outcome"] != string(proxy.SubscriptionSyncUpdated) {
		t.Fatalf("outcome = %v, want updated", payload["outcome"])
	}
	sub, _ := payload["subscription"].(map[string]any)
	if sub["business_status"] != auth.SubscriptionStatusActive || sub["sync_state"] != auth.SubscriptionSyncConfirmed || sub["auto_renew"] != auth.SubscriptionAutoRenewEnabled {
		t.Fatalf("subscription view = %v", sub)
	}
	if requests != 1 {
		t.Fatalf("upstream requests = %d, want 1", requests)
	}

	rec, payload = runSubscriptionRefresh(t, handler, "31")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second refresh status = %d, want 429: %s", rec.Code, rec.Body.String())
	}
	if _, ok := payload["retry_after"]; !ok || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("429 must carry retry_after: %v", payload)
	}
	if requests != 1 {
		t.Fatalf("rate-limited refresh must not hit upstream, requests = %d", requests)
	}
}

// 非 Codex 渠道与不跟踪订阅的套餐直接返回 unsupported，不打上游。
func TestRefreshAccountSubscription_Unsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called")
	}))
	defer server.Close()
	defer proxy.SetSubscriptionsURLForTest(server.URL)()

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2})
	free := &auth.Account{DBID: 42, AccessToken: "token", PlanType: "free", Status: auth.StatusReady, AccountID: subscriptionTestWorkspaceUUID}
	store.AddAccount(free)
	handler := &Handler{store: store}

	for _, id := range []string{"42"} {
		rec, payload := runSubscriptionRefresh(t, handler, id)
		if rec.Code != http.StatusOK || payload["outcome"] != string(proxy.SubscriptionSyncUnsupported) {
			t.Fatalf("account %s: status=%d payload=%v", id, rec.Code, payload)
		}
	}

	rec, _ := runSubscriptionRefresh(t, handler, "999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown account status = %d, want 404", rec.Code)
	}
}

// 列表/详情响应附带服务端计算的订阅状态对象；api 套餐不带。
func TestSubscriptionStatusViewForRow(t *testing.T) {
	past := time.Now().Add(-3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	row := &database.AccountRow{Credentials: map[string]any{
		"subscription_expires_at":               past,
		auth.SubscriptionSyncStateCredentialKey: auth.SubscriptionSyncFailed,
		auth.SubscriptionErrorCredentialKey:     "blocked",
		auth.SubscriptionCheckedAtCredentialKey: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}}
	view := subscriptionStatusViewForRow(row, "plus")
	if view == nil || view.BusinessStatus != auth.SubscriptionStatusExpired || view.SyncState != auth.SubscriptionSyncFailed || view.Error != "blocked" {
		t.Fatalf("view = %+v", view)
	}
	if view.DaysOverdue < 3 || view.LastCheckedAt == "" {
		t.Fatalf("view = %+v", view)
	}
	if v := subscriptionStatusViewForRow(row, "api"); v != nil {
		t.Fatalf("api plan should not carry subscription view: %+v", v)
	}
}

// 列表「订阅状态」筛选：按服务端状态对象匹配；不跟踪订阅的账号任何筛选值都不命中。
func TestSubscriptionFilterMatches(t *testing.T) {
	// 业务状态按 time.Local 自然日算，基准取本地正午，让 +6h 仍落在当天。
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.Local)
	mk := func(plan string, creds map[string]any) *accountListSnapshotItem {
		return &accountListSnapshotItem{PlanType: plan, Row: &database.AccountRow{Credentials: creds}}
	}
	iso := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	items := map[string]*accountListSnapshotItem{
		"active20":  mk("plus", map[string]any{"subscription_expires_at": iso(20 * 24 * time.Hour)}),
		"active5":   mk("plus", map[string]any{"subscription_expires_at": iso(5 * 24 * time.Hour)}),
		"active2":   mk("plus", map[string]any{"subscription_expires_at": iso(2 * 24 * time.Hour)}),
		"today":     mk("pro", map[string]any{"subscription_expires_at": iso(6 * time.Hour)}),
		"expired":   mk("free", map[string]any{"subscription_expires_at": iso(-3 * 24 * time.Hour)}),
		"grace":     mk("plus", map[string]any{"subscription_expires_at": iso(-24 * time.Hour), auth.SubscriptionGraceUntilCredentialKey: iso(5 * 24 * time.Hour)}),
		"pending":   mk("plus", map[string]any{auth.SubscriptionSyncStateCredentialKey: auth.SubscriptionSyncPending, auth.SubscriptionRenewalDetectedAtCredentialKey: iso(-time.Hour)}),
		"failed":    mk("plus", map[string]any{"subscription_expires_at": iso(10 * 24 * time.Hour), auth.SubscriptionSyncStateCredentialKey: auth.SubscriptionSyncFailed}),
		"unknown":   mk("plus", map[string]any{}),
		"apiPlan":   mk("api", map[string]any{"subscription_expires_at": iso(-24 * time.Hour)}),
		"freeNoExp": mk("free", map[string]any{}),
	}
	want := map[string][]string{
		subscriptionFilterActive:        {"active20", "active5", "active2", "failed"},
		subscriptionFilterExpiring9d:    {"active5", "active2", "today"},
		subscriptionFilterExpiring3d:    {"active2", "today"},
		subscriptionFilterExpiringToday: {"today"},
		subscriptionFilterExpired:       {"expired"},
		subscriptionFilterGracePeriod:   {"grace"},
		subscriptionFilterPending:       {"pending"},
		subscriptionFilterFailed:        {"failed"},
		subscriptionFilterUnknown:       {"unknown"},
	}
	for filter, names := range want {
		expected := map[string]bool{}
		for _, n := range names {
			expected[n] = true
		}
		for name, item := range items {
			if got := subscriptionFilterMatches(item, filter, now); got != expected[name] {
				t.Errorf("filter %s item %s = %v, want %v", filter, name, got, expected[name])
			}
		}
	}
	if err := validateAccountPageFilters(accountPageQuery{Subscription: "bogus"}); err == nil {
		t.Fatal("bogus subscription filter should be rejected")
	}
	if err := validateAccountPageFilters(accountPageQuery{Subscription: subscriptionFilterExpired}); err != nil {
		t.Fatalf("valid subscription filter rejected: %v", err)
	}
}
