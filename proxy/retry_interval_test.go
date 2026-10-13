package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/config"
	"github.com/codex2cc/database"
)

func newRetryTestHandler(t *testing.T) (*Handler, *auth.Store) {
	t.Helper()
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.5", MaxRetries: 2})
	t.Cleanup(store.Stop)
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	return handler, store
}

func TestWaitBeforeRetry(t *testing.T) {
	h, store := newRetryTestHandler(t)

	t.Run("间隔为 0 立即返回", func(t *testing.T) {
		store.SetRetryIntervalMS(0)
		start := time.Now()
		if !h.waitBeforeRetry(context.Background()) {
			t.Fatal("want true")
		}
		if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
			t.Fatalf("interval 0 should not wait, took %v", elapsed)
		}
	})

	t.Run("ctx 已取消返回 false", func(t *testing.T) {
		store.SetRetryIntervalMS(0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if h.waitBeforeRetry(ctx) {
			t.Fatal("want false for canceled ctx")
		}
	})

	t.Run("按配置间隔等待", func(t *testing.T) {
		store.SetRetryIntervalMS(60)
		start := time.Now()
		if !h.waitBeforeRetry(context.Background()) {
			t.Fatal("want true")
		}
		if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
			t.Fatalf("should wait ~60ms, took %v", elapsed)
		}
	})

	t.Run("等待中客户端断开返回 false", func(t *testing.T) {
		store.SetRetryIntervalMS(5000)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		if h.waitBeforeRetry(ctx) {
			t.Fatal("want false when canceled mid-wait")
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("cancel should abort the wait promptly, took %v", elapsed)
		}
	})

	t.Run("unlimited Retry-After 等待可被客户端取消", func(t *testing.T) {
		store.SetRetryIntervalMS(0)
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Retry-After", "60")
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		if h.waitBeforeRetryWithBudget(ctx, 1, -1, resp) {
			t.Fatal("want false when canceled during Retry-After wait")
		}
		elapsed := time.Since(start)
		if elapsed < 10*time.Millisecond {
			t.Fatalf("Retry-After was ignored, returned after %v", elapsed)
		}
		if elapsed > time.Second {
			t.Fatalf("cancel should abort Retry-After promptly, took %v", elapsed)
		}
	})

	t.Run("finite retry ignores upstream Retry-After", func(t *testing.T) {
		store.SetRetryIntervalMS(0)
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Retry-After", "60")
		started := time.Now()
		if !h.waitBeforeRetryWithBudget(context.Background(), 1, 2, resp) {
			t.Fatal("finite retry was unexpectedly canceled")
		}
		if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
			t.Fatalf("finite retry inherited upstream Retry-After, took %v", elapsed)
		}
	})
}

func TestParseRetryAfterHeaderAt(t *testing.T) {
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "delta seconds", value: "12", want: 12 * time.Second},
		{name: "HTTP date", value: now.Add(90 * time.Second).Format(http.TimeFormat), want: 90 * time.Second},
		{name: "expired HTTP date", value: now.Add(-time.Second).Format(http.TimeFormat), want: 0},
		{name: "zero", value: "0", want: 0},
		{name: "negative", value: "-1", want: 0},
		{name: "invalid", value: "later", want: 0},
		{name: "overflow", value: "999999999999999999999999", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetryAfterHeaderAt(tc.value, now); got != tc.want {
				t.Fatalf("parseRetryAfterHeaderAt(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestRetryableUpstreamStatuses(t *testing.T) {
	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusUpgradeRequired,
	} {
		if !isRetryableStatus(status) {
			t.Errorf("legacy status %d should be retryable", status)
		}
	}
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusNotFound,
		http.StatusUnprocessableEntity,
		http.StatusBadGateway,
		http.StatusGatewayTimeout,
	} {
		if isRetryableStatus(status) {
			t.Errorf("status %d should not be retried by the legacy classifier", status)
		}
	}
}

func TestRetryBudgetAvailable(t *testing.T) {
	cases := []struct {
		used, limit int
		want        bool
	}{
		{used: 0, limit: -1, want: true},
		{used: 100_000, limit: -1, want: true},
		{used: 0, limit: 0, want: false},
		{used: 0, limit: 1, want: true},
		{used: 1, limit: 1, want: false},
		{used: 0, limit: -2, want: false},
	}
	for _, tc := range cases {
		if got := retryBudgetAvailable(tc.used, tc.limit); got != tc.want {
			t.Errorf("retryBudgetAvailable(%d, %d) = %v, want %v", tc.used, tc.limit, got, tc.want)
		}
	}
}

func TestUnlimitedRetryBackoff(t *testing.T) {
	cases := []struct {
		ordinal int
		jitter  float64
		want    time.Duration
	}{
		{ordinal: 1, jitter: 0, want: 250 * time.Millisecond},
		{ordinal: 2, jitter: 0, want: 500 * time.Millisecond},
		{ordinal: 3, jitter: 0, want: time.Second},
		{ordinal: 1, jitter: 1, want: 375 * time.Millisecond},
		{ordinal: 1, jitter: -1, want: 250 * time.Millisecond},
		{ordinal: 100, jitter: 1, want: 30 * time.Second},
	}
	for _, tc := range cases {
		if got := unlimitedRetryBackoff(tc.ordinal, tc.jitter); got != tc.want {
			t.Errorf("unlimitedRetryBackoff(%d, %v) = %v, want %v", tc.ordinal, tc.jitter, got, tc.want)
		}
	}
}

func TestUnlimitedRetryWaitIsCancellable(t *testing.T) {
	h, store := newRetryTestHandler(t)
	store.SetRetryIntervalMS(0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if h.waitBeforeRetryWithBudget(ctx, 1, -1) {
		t.Fatal("unlimited retry wait returned true after context cancellation")
	}
	elapsed := time.Since(started)
	if elapsed < 10*time.Millisecond {
		t.Fatalf("unlimited retry backoff was skipped: %v", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("context did not interrupt unlimited retry backoff promptly: %v", elapsed)
	}
}

func TestFirstTokenTimeoutRetryWaitPolicy(t *testing.T) {
	h, store := newRetryTestHandler(t)
	store.SetRetryIntervalMS(5000)

	t.Run("finite retry preserves immediate shortcut", func(t *testing.T) {
		started := time.Now()
		if !h.waitBeforeRetryWithFirstTokenTimeout(context.Background(), true, 1, 2) {
			t.Fatal("finite first-token timeout retry was canceled")
		}
		if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
			t.Fatalf("finite first-token timeout added a retry delay: %v", elapsed)
		}
	})

	t.Run("finite retry still honors cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if h.waitBeforeRetryWithFirstTokenTimeout(ctx, true, 1, 2) {
			t.Fatal("canceled finite first-token timeout retry continued")
		}
	})

	t.Run("unlimited retry uses cancellable backoff", func(t *testing.T) {
		store.SetRetryIntervalMS(0)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		started := time.Now()
		if h.waitBeforeRetryWithFirstTokenTimeout(ctx, true, 1, -1) {
			t.Fatal("unlimited first-token timeout retry ignored cancellation")
		}
		if elapsed := time.Since(started); elapsed < 10*time.Millisecond {
			t.Fatalf("unlimited first-token timeout backoff was skipped: %v", elapsed)
		}
	})
}

func TestUnlimitedRetrySmallRetryAfterDoesNotBypassBackoff(t *testing.T) {
	h, store := newRetryTestHandler(t)
	store.SetRetryIntervalMS(0)
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("Retry-After", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 1100*time.Millisecond)
	defer cancel()

	if h.waitBeforeRetryWithBudget(ctx, 8, -1, resp) {
		t.Fatal("small Retry-After bypassed the larger unlimited retry backoff")
	}
}

func TestRetryAttemptProgress(t *testing.T) {
	if got := retryAttemptProgress(2, 4); got != "3/5" {
		t.Fatalf("finite retry progress = %q, want 3/5", got)
	}
	if got := retryAttemptProgress(2, -1); got != "3/unlimited" {
		t.Fatalf("unlimited retry progress = %q, want 3/unlimited", got)
	}
}

func TestRetrySettingsNormalization(t *testing.T) {
	_, store := newRetryTestHandler(t)

	store.SetRetryIntervalMS(-5)
	if got := store.GetRetryIntervalMS(); got != 0 {
		t.Errorf("negative interval → %d, want 0", got)
	}
	store.SetRetryIntervalMS(99999)
	if got := store.GetRetryIntervalMS(); got != 30000 {
		t.Errorf("oversized interval → %d, want 30000", got)
	}

	store.SetTransportRetryPolicy(" STICKY ")
	if got := store.GetTransportRetryPolicy(); got != "sticky" {
		t.Errorf("policy STICKY → %q, want sticky", got)
	}
	store.SetTransportRetryPolicy("whatever")
	if got := store.GetTransportRetryPolicy(); got != "rotate" {
		t.Errorf("unknown policy → %q, want rotate", got)
	}
}
