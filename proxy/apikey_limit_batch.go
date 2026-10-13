package proxy

import (
	"context"
	"encoding/json"
	"time"

	"github.com/codex2cc/cache"
	"github.com/codex2cc/database"
)

type apiKeyLimitBatchContextKey struct{}

// A request-local snapshot removes serial Redis reads without adding any TTL
// or changing SQL fallback. Entries absent from a completed batch are misses.
type apiKeyLimitBatch map[string]json.RawMessage

func (h *Handler) withAPIKeyLimitBatch(ctx context.Context, row *database.APIKeyRow, dayStart time.Time) context.Context {
	reader, ok := h.cache.(cache.RuntimeBatchReader)
	if !ok || row == nil {
		return ctx
	}
	limits := row.Limits
	enabled := [...]bool{
		limits.RPM > 0, limits.RPD > 0,
		limits.CostLimitDaily > 0 || limits.TokenLimitDaily > 0,
		limits.CostLimit5h > 0 || limits.TokenLimit5h > 0,
		limits.CostLimit7d > 0 || limits.TokenLimit7d > 0,
		limits.CostLimit30d > 0 || limits.TokenLimit30d > 0,
	}
	count := 0
	for _, on := range enabled {
		if on {
			count++
		}
	}
	if count < 2 {
		return ctx
	}
	keys := make([]string, 0, 6)
	add := func(enabled bool, kind, label string) {
		if enabled {
			keys = append(keys, apiKeyLimitsCacheKey(row.ID, kind, label))
		}
	}
	add(enabled[0], "req", "rpm")
	add(enabled[1], "req", "rpd")
	add(enabled[2], "usage", "daily:"+dayStart.Format("2006-01-02"))
	add(enabled[3], "usage", "5h")
	add(enabled[4], "usage", "7d")
	add(enabled[5], "usage", "30d")
	values, _ := reader.GetRuntimeBatch(ctx, apiKeyLimitsCacheNamespace, keys)
	snapshot := make(apiKeyLimitBatch, len(keys))
	for _, key := range keys {
		snapshot[key] = values[key]
	}
	return context.WithValue(ctx, apiKeyLimitBatchContextKey{}, snapshot)
}

func (h *Handler) apiKeyLimitPayload(ctx context.Context, key string) json.RawMessage {
	if batch, ok := ctx.Value(apiKeyLimitBatchContextKey{}).(apiKeyLimitBatch); ok {
		if raw, included := batch[key]; included {
			return raw
		}
	}
	raw, ok, err := h.cache.GetRuntime(ctx, apiKeyLimitsCacheNamespace, key)
	if err != nil || !ok {
		return nil
	}
	return raw
}
