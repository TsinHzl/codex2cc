package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newBatchTestRedis(t testing.TB) *redisTokenCache {
	t.Helper()
	addr := os.Getenv("CODEX2API_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set CODEX2API_TEST_REDIS_ADDR to an isolated Redis")
	}
	options := &redis.Options{Addr: addr}
	if strings.HasPrefix(addr, "/") {
		options.Network = "unix"
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	return &redisTokenCache{client: client}
}

func TestRuntimeBatchDrivers(t *testing.T) {
	for _, driver := range []string{"memory", "redis"} {
		t.Run(driver, func(t *testing.T) {
			var tc TokenCache
			if driver == "redis" {
				tc = newBatchTestRedis(t)
			} else {
				tc = NewMemory(1)
				t.Cleanup(func() { _ = tc.Close() })
			}
			ctx := context.Background()
			ns := fmt.Sprintf("batch-test-%d", time.Now().UnixNano())
			keys := make([]string, runtimeReadBatchSize+3)
			for i := range keys {
				keys[i] = fmt.Sprint(i)
				if err := tc.SetRuntime(ctx, ns, keys[i], json.RawMessage(`{"ok":true}`), time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				for _, key := range keys {
					_ = tc.DeleteRuntime(ctx, ns, key)
				}
			})
			reader := tc.(RuntimeBatchReader)
			values, err := reader.GetRuntimeBatch(ctx, ns, append(append([]string{}, keys...), "missing", "", " 0 "))
			if err != nil || len(values) != len(keys) {
				t.Fatalf("batch: count=%d err=%v", len(values), err)
			}
			values["0"][0] = 'x'
			raw, found, err := tc.GetRuntime(ctx, ns, "0")
			if err != nil || !found || string(raw) != `{"ok":true}` {
				t.Fatal("batch result aliases cached payload")
			}
			other, err := reader.GetRuntimeBatch(ctx, ns+"-other", keys[:1])
			if err != nil || len(other) != 0 {
				t.Fatal("batch crossed namespace")
			}
			if err := tc.DeleteRuntime(ctx, ns, "0"); err != nil {
				t.Fatal(err)
			}
			values, err = reader.GetRuntimeBatch(ctx, ns, keys[:1])
			if err != nil || len(values) != 0 {
				t.Fatal("deleted value remained in batch")
			}
			if err := tc.SetRuntime(ctx, ns, "0", json.RawMessage(`1`), time.Millisecond); err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
			values, err = reader.GetRuntimeBatch(ctx, ns, keys[:1])
			if err != nil || len(values) != 0 {
				t.Fatal("expired value remained in batch")
			}
			if err := tc.IncrRuntimeCounters(ctx, ns, "0", map[string]float64{"requests": 2, "cost": 0.5}, time.Minute); err != nil {
				t.Fatal(err)
			}
			counts, err := tc.(RuntimeCounterBatchReader).GetRuntimeCountersBatch(ctx, ns, []string{"0", "missing"})
			if err != nil || len(counts) != 1 || counts["0"]["requests"] != 2 || counts["0"]["cost"] != 0.5 {
				t.Fatalf("counter batch: %v %v", counts, err)
			}
			counts["0"]["requests"] = 99
			count, err := tc.GetRuntimeCounters(ctx, ns, "0")
			if err != nil || count["requests"] != 2 {
				t.Fatal("counter results alias storage")
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := reader.GetRuntimeBatch(canceled, ns, keys[:1]); err == nil {
				t.Fatal("canceled batch succeeded")
			}
		})
	}
}

func TestRedisRuntimeCounterBatchPreservesPartialSuccess(t *testing.T) {
	tc := newBatchTestRedis(t)
	ctx := context.Background()
	ns := fmt.Sprintf("counter-batch-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = tc.DeleteRuntime(ctx, ns, "good"); _ = tc.DeleteRuntime(ctx, ns, "bad") })
	if err := tc.IncrRuntimeCounters(ctx, ns, "good", map[string]float64{"tokens": 42}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetRuntime(ctx, ns, "bad", json.RawMessage(`"wrong-type"`), time.Minute); err != nil {
		t.Fatal(err)
	}
	values, err := tc.GetRuntimeCountersBatch(ctx, ns, []string{"bad", "good"})
	if err == nil || values["good"]["tokens"] != 42 || values["bad"] != nil {
		t.Fatalf("partial result: %v %v", values, err)
	}
}

func BenchmarkRedisRuntimeReads(b *testing.B) {
	tc := newBatchTestRedis(b)
	ctx := context.Background()
	ns := fmt.Sprintf("batch-bench-%d", time.Now().UnixNano())
	keys := []string{"rpm", "rpd", "daily", "5h", "7d", "30d"}
	for _, key := range keys {
		if err := tc.SetRuntime(ctx, ns, key, json.RawMessage(`{"requests":1,"tokens":100,"user_billed":0.01}`), time.Minute); err != nil {
			b.Fatal(err)
		}
	}
	b.Cleanup(func() {
		for _, key := range keys {
			_ = tc.DeleteRuntime(ctx, ns, key)
		}
	})
	b.Run("sequential", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, key := range keys {
				if _, _, err := tc.GetRuntime(ctx, ns, key); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("batch", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := tc.GetRuntimeBatch(ctx, ns, keys); err != nil {
				b.Fatal(err)
			}
		}
	})
}
