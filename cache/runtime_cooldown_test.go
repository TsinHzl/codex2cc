package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestMemoryRuntimeCooldownMerge(t *testing.T) {
	c := NewMemory(1)
	t.Cleanup(func() { _ = c.Close() })
	testRuntimeCooldownMerge(t, c)
}

func TestRedisRuntimeCooldownMerge(t *testing.T) {
	addr := os.Getenv("CODEX2API_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set CODEX2API_TEST_REDIS_ADDR to an isolated Redis for Lua integration tests")
	}
	options := &redis.Options{Addr: addr}
	if strings.HasPrefix(addr, "/") {
		options.Network = "unix"
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	testRuntimeCooldownMerge(t, &redisTokenCache{client: client})
}

func testRuntimeCooldownMerge(t *testing.T, c TokenCache) {
	t.Helper()
	merger := c.(RuntimeCooldownMerger)
	ctx := context.Background()
	ns := "scheduler-cooldown-test"
	key := fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())
	t.Cleanup(func() {
		if err := c.DeleteRuntime(ctx, ns, key); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC()
	short := RuntimeCooldown{Kind: CooldownKindTransient, Reason: "responses_rate_limited", ResetAt: now.Add(time.Minute), UpdatedAt: now, BackoffLevel: 3}
	extended := short
	extended.ResetAt = now.Add(2 * time.Minute)
	extended.BackoffLevel = 1
	for _, incoming := range []RuntimeCooldown{short, extended, short} {
		if _, err := merger.MergeRuntimeCooldown(ctx, ns, key, incoming); err != nil {
			t.Fatal(err)
		}
	}
	got := readTestCooldown(t, c, ns, key)
	if !got.ResetAt.Equal(extended.ResetAt) || got.BackoffLevel != 3 || got.Kind != CooldownKindTransient {
		t.Fatalf("out-of-order throttle lost deadline/backoff: %+v", got)
	}
	// Even a shorter quota window must outrank a transient freeze.
	quota := RuntimeCooldown{Reason: "usage_limit", ResetAt: now.Add(30 * time.Second), UpdatedAt: now}
	if _, err := merger.MergeRuntimeCooldown(ctx, ns, key, quota); err != nil {
		t.Fatal(err)
	}
	if got, err := merger.MergeRuntimeCooldown(ctx, ns, key, extended); err != nil || got.Kind == CooldownKindTransient || got.Reason != quota.Reason {
		t.Fatalf("late throttle replaced quota: %+v, %v", got, err)
	}
	// Legacy JSON has no kind/absolute-millisecond field. It remains a quota.
	legacy := RuntimeCooldown{Reason: "responses_rate_limited", ResetAt: now.Add(time.Hour), UpdatedAt: now}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetRuntime(ctx, ns, key, raw, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err = merger.MergeRuntimeCooldown(ctx, ns, key, short)
	if err != nil || got.Kind != "" || !got.ResetAt.Equal(legacy.ResetAt) {
		t.Fatalf("legacy cooldown weakened: %+v, %v", got, err)
	}
	// Explicit clear allows a new short freeze.
	if err := c.DeleteRuntime(ctx, ns, key); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			incoming := short
			incoming.ResetAt = now.Add(time.Duration(i+1) * time.Second)
			incoming.BackoffLevel = i%5 + 1
			if _, err := merger.MergeRuntimeCooldown(ctx, ns, key, incoming); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got = readTestCooldown(t, c, ns, key)
	if !got.ResetAt.Equal(now.Add(24*time.Second)) || got.BackoffLevel != 5 {
		t.Fatalf("concurrent merge lost strongest state: %+v", got)
	}
}

func readTestCooldown(t *testing.T, c TokenCache, namespace, key string) RuntimeCooldown {
	t.Helper()
	raw, ok, err := c.GetRuntime(context.Background(), namespace, key)
	if err != nil || !ok {
		t.Fatalf("read cooldown: ok=%v err=%v", ok, err)
	}
	var record RuntimeCooldown
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}
