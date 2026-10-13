package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/proxy"
	"github.com/gin-gonic/gin"
)

const codexUsageRefreshFixture = `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":23,"limit_window_seconds":18000,"reset_after_seconds":3600},"secondary_window":{"used_percent":57,"limit_window_seconds":604800,"reset_after_seconds":86400}}}`

func callCodexUsageRefresh(h *Handler, ctx context.Context, stream bool) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/accounts/batch-refresh-usage?stream=%t", stream), nil).WithContext(ctx)
	h.BatchRefreshCodexUsage(c)
	return w
}

func TestBatchRefreshCodexUsageUpdatesSnapshotsWithoutFallback(t *testing.T) {
	var requests, unexpected, fallback, tokenRefresh atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/backend-api/wham/usage" || r.Method != http.MethodGet {
			unexpected.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer codex-denied":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"private diagnostic must not leak"}`))
		case "Bearer codex-empty":
			_, _ = w.Write([]byte(`{"rate_limit":{}}`))
		default:
			_, _ = w.Write([]byte(codexUsageRefreshFixture))
		}
	}))
	defer upstream.Close()
	defer proxy.SetWhamUsageURLForTest(upstream.URL + "/backend-api/wham/usage")()
	store := auth.NewStore(nil, nil, nil)
	defer store.Stop()
	store.SetUsageProbeResponsesFallbackEnabled(true)
	accounts := []*auth.Account{
		{DBID: 1, AccessToken: "codex-ok", Status: auth.StatusReady},
		{DBID: 2, AccessToken: "codex-denied", Status: auth.StatusReady},
		{DBID: 3, AccessToken: "codex-empty", Status: auth.StatusReady},
		{DBID: 7, AccessToken: "relay-token", UpstreamType: auth.UpstreamOpenAIResponses, Status: auth.StatusReady},
		{DBID: 8, RefreshToken: "no-at", Status: auth.StatusReady},
		{DBID: 9, CodexAuthMode: auth.CodexAuthModeAgentIdentity, AccessToken: "identity-token", Status: auth.StatusReady},
	}
	for _, account := range accounts {
		account.SetUsageSnapshot5h(9, time.Now().Add(time.Hour))
		store.AddAccount(account)
	}
	h := &Handler{store: store, probeUsage: func(context.Context, *auth.Account) error {
		fallback.Add(1)
		return nil
	}, refreshAccount: func(context.Context, int64) error {
		tokenRefresh.Add(1)
		return nil
	}}
	generation := h.accountCachesGen.Load()
	response := callCodexUsageRefresh(h, context.Background(), true)
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var events []batchOperationEvent
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event batchOperationEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 5 || events[0].Type != "start" || events[0].Total != 3 {
		t.Fatalf("unexpected events: %+v", events)
	}
	for i, event := range events[1:4] {
		if event.Action != "batch_usage_refresh" || event.Type != "progress" || event.Current != i+1 || event.Success+event.Failed != int64(i+1) {
			t.Fatalf("nonmonotonic progress: %+v", event)
		}
	}
	complete := events[len(events)-1]
	if complete.Type != "complete" || complete.Total != 3 || complete.Current != 3 || complete.Success != 1 || complete.Failed != 2 {
		t.Fatalf("wrong completion: %+v", complete)
	}
	if requests.Load() != 3 || unexpected.Load() != 0 || fallback.Load() != 0 || tokenRefresh.Load() != 0 {
		t.Fatalf("requests=%d unexpected=%d fallback=%d refresh=%d", requests.Load(), unexpected.Load(), fallback.Load(), tokenRefresh.Load())
	}
	if pct, ok := accounts[0].GetUsagePercent5h(); !ok || pct != 23 {
		t.Fatalf("5h snapshot=(%v,%v)", pct, ok)
	}
	if pct, ok := accounts[0].GetUsagePercent7d(); !ok || pct != 57 {
		t.Fatalf("7d snapshot=(%v,%v)", pct, ok)
	}
	for _, account := range accounts[1:] {
		if pct, _ := account.GetUsagePercent5h(); pct != 9 {
			t.Fatalf("failed/unsupported account %d snapshot changed to %v", account.DBID, pct)
		}
	}
	if accounts[1].RuntimeStatus() != "active" || strings.Contains(response.Body.String(), "private diagnostic") {
		t.Fatal("WHAM 401 changed account authentication or exposed response body")
	}
	if h.accountCachesGen.Load() <= generation {
		t.Fatal("completed refresh did not invalidate account projection caches")
	}
}

func TestBatchRefreshCodexUsageBoundsConcurrencyAndCancels(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write([]byte(codexUsageRefreshFixture))
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)
	defer proxy.SetWhamUsageURLForTest(upstream.URL)()
	store := auth.NewStore(nil, nil, nil)
	defer store.Stop()
	store.SetUsageProbeConcurrency(2)
	for id := int64(1); id <= 8; id++ {
		store.AddAccount(&auth.Account{DBID: id, AccessToken: "codex-token", Status: auth.StatusReady})
	}
	h := &Handler{store: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		callCodexUsageRefresh(h, ctx, true)
		close(done)
	}()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	if response := callCodexUsageRefresh(h, context.Background(), false); response.Code != http.StatusConflict {
		t.Fatalf("overlapping batch status=%d, want 409", response.Code)
	}
	if calls.Load() != 2 {
		t.Fatalf("concurrency exceeded: %d requests", calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not stop after client cancellation")
	}
	if calls.Load() != 2 || h.codexUsageRefreshRunning.Load() {
		t.Fatalf("queued work continued or batch lock leaked: calls=%d", calls.Load())
	}
	for _, account := range store.Accounts() {
		if !account.TryBeginUsageProbe() {
			t.Fatalf("account %d retained probe lock", account.DBID)
		}
		account.FinishUsageProbe()
	}
}

func TestBatchRefreshCodexUsageEmptyPoolAndBusyAccount(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	defer store.Stop()
	h := &Handler{store: store}
	response := callCodexUsageRefresh(h, context.Background(), false)
	var result batchOperationEvent
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.Total != 0 || result.Type != "complete" {
		t.Fatalf("empty batch: %d %s", response.Code, response.Body.String())
	}
	account := &auth.Account{DBID: 1, AccessToken: "codex-token", Status: auth.StatusReady}
	store.AddAccount(account)
	account.TryBeginUsageProbe()
	defer account.FinishUsageProbe()
	response = callCodexUsageRefresh(h, context.Background(), false)
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Total != 1 || result.Failed != 1 || result.Success != 0 {
		t.Fatalf("busy account: %s", response.Body.String())
	}
}
