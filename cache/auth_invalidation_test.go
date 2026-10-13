package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestRedisAuthInvalidationAndBoundedRead(t *testing.T) {
	tc := newBatchTestRedis(t)
	scope := fmt.Sprintf("auth-bus-test-%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notices := make(chan struct{}, 4)
	done := make(chan error, 1)
	go func() { done <- tc.SubscribeAuthInvalidations(ctx, scope, func() { notices <- struct{}{} }) }()
	select {
	case <-notices:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not become ready")
	}
	if err := tc.PublishAuthInvalidation(ctx, scope); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notices:
	case <-time.After(time.Second):
		t.Fatal("invalidation not delivered")
	}
	if err := tc.SetRuntime(ctx, scope, "snapshot", json.RawMessage(`1234567890`), time.Minute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tc.DeleteRuntime(context.Background(), scope, "snapshot") })
	if _, _, err := tc.GetRuntimeBounded(ctx, scope, "snapshot", 5); err == nil {
		t.Fatal("oversized snapshot fetched")
	}
	raw, ok, err := tc.GetRuntimeBounded(ctx, scope, "snapshot", 10)
	if err != nil || !ok || string(raw) != "1234567890" {
		t.Fatal("bounded snapshot read failed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not stop")
	}
}

func TestRedisAuthSubscriptionCancellationWhileUnavailable(t *testing.T) {
	tc := newBatchTestRedis(t)
	if err := tc.client.Do(context.Background(), "CLIENT", "PAUSE", "1500", "ALL").Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tc.client.Do(context.Background(), "CLIENT", "UNPAUSE").Err() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tc.SubscribeAuthInvalidations(ctx, "paused-auth-subscription", func() {}) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled subscription returned success")
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("subscription startup did not honor cancellation/connection budget")
	}
}
