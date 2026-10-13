package cache

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RuntimeBatchReader is optional so existing TokenCache adapters remain valid.
// Missing keys are omitted. Returned payloads belong to the caller.
type RuntimeBatchReader interface {
	GetRuntimeBatch(context.Context, string, []string) (map[string]json.RawMessage, error)
}

// RuntimeCounterBatchReader returns successful reads even if another key fails.
type RuntimeCounterBatchReader interface {
	GetRuntimeCountersBatch(context.Context, string, []string) (map[string]map[string]float64, error)
}

const runtimeReadBatchSize = 128

func runtimeBatchKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

func (tc *redisTokenCache) GetRuntimeBatch(ctx context.Context, namespace string, keys []string) (map[string]json.RawMessage, error) {
	keys = runtimeBatchKeys(keys)
	out := make(map[string]json.RawMessage, len(keys))
	for start := 0; start < len(keys); start += runtimeReadBatchSize {
		batch := keys[start:min(start+runtimeReadBatchSize, len(keys))]
		wireKeys := make([]string, len(batch))
		for i, key := range batch {
			wireKeys[i] = runtimeValueKey(namespace, key)
		}
		values, err := tc.client.MGet(ctx, wireKeys...).Result()
		if err != nil {
			return out, err
		}
		for i, value := range values {
			if raw, ok := value.(string); ok {
				out[batch[i]] = json.RawMessage(raw)
			}
		}
	}
	return out, ctx.Err()
}

func (tc *MemoryTokenCache) GetRuntimeBatch(ctx context.Context, namespace string, keys []string) (map[string]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys = runtimeBatchKeys(keys)
	out := make(map[string]json.RawMessage, len(keys))
	now := time.Now()
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	for _, key := range keys {
		entry, ok := tc.runtime[runtimeMapKey(namespace, key)]
		if ok && (entry.expiresAt.IsZero() || now.Before(entry.expiresAt)) {
			out[key] = append(json.RawMessage(nil), entry.value...)
		}
	}
	return out, nil
}

func (tc *redisTokenCache) GetRuntimeCountersBatch(ctx context.Context, namespace string, keys []string) (map[string]map[string]float64, error) {
	keys = runtimeBatchKeys(keys)
	out := make(map[string]map[string]float64, len(keys))
	var readErr error
	for start := 0; start < len(keys); start += runtimeReadBatchSize {
		batch := keys[start:min(start+runtimeReadBatchSize, len(keys))]
		pipe := tc.client.Pipeline()
		commands := make([]*redis.MapStringStringCmd, len(batch))
		for i, key := range batch {
			commands[i] = pipe.HGetAll(ctx, runtimeValueKey(namespace, key))
		}
		_, err := pipe.Exec(ctx)
		readErr = errors.Join(readErr, err)
		for i, command := range commands {
			values, err := command.Result()
			if err != nil || len(values) == 0 {
				continue
			}
			counters := make(map[string]float64, len(values))
			for field, raw := range values {
				if value, err := strconv.ParseFloat(raw, 64); err == nil {
					counters[field] = value
				}
			}
			out[batch[i]] = counters
		}
		if ctx.Err() != nil {
			break
		}
	}
	return out, errors.Join(readErr, ctx.Err())
}

func (tc *MemoryTokenCache) GetRuntimeCountersBatch(ctx context.Context, namespace string, keys []string) (map[string]map[string]float64, error) {
	values, err := tc.GetRuntimeBatch(ctx, namespace, keys)
	out := make(map[string]map[string]float64, len(values))
	for key, raw := range values {
		var counters map[string]float64
		if json.Unmarshal(raw, &counters) == nil && len(counters) > 0 {
			out[key] = counters
		}
	}
	return out, err
}
