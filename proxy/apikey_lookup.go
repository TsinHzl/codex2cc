package proxy

import (
	"context"
	"crypto/sha256"
	"slices"
	"strings"
	"time"

	"github.com/codex2cc/database"
)

// resolveAPIKey uses the configured authentication cache. With the layered
// cache disabled, only overlapping lookups share the legacy read-through result.
// Infrastructure errors are never treated as a confirmed missing credential.
func (h *Handler) resolveAPIKey(key string) (*database.APIKeyRow, bool, error) {
	return h.resolveAPIKeyContext(context.Background(), key)
}

func (h *Handler) resolveAPIKeyContext(ctx context.Context, key string) (*database.APIKeyRow, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || h.configKeys[key] {
		return h.resolveAPIKeyUnshared(key)
	}
	if h.authCache != nil {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		row, ok, err := h.authCache.resolve(ctx, key)
		if err == nil && ok {
			h.syncAPIKeyAllowedGroups(row)
		}
		return row, ok, err
	}
	digest := sha256.Sum256([]byte(key))
	value, err, shared := h.apiKeyLookups.Do(string(digest[:]), func() (any, error) {
		row, _, err := h.resolveAPIKeyUnshared(key)
		return row, err
	})
	if err != nil {
		return nil, false, err
	}
	row, _ := value.(*database.APIKeyRow)
	if row != nil && shared {
		row = cloneAPIKeyLookupRow(row)
	}
	return row, row != nil, nil
}

func cloneAPIKeyLookupRow(row *database.APIKeyRow) *database.APIKeyRow {
	copy := *row
	copy.AllowedGroupIDs = slices.Clone(row.AllowedGroupIDs)
	copy.Limits.ModelRequestLimits = slices.Clone(row.Limits.ModelRequestLimits)
	copy.Limits.ModelAllow = slices.Clone(row.Limits.ModelAllow)
	copy.Limits.ModelDeny = slices.Clone(row.Limits.ModelDeny)
	copy.Limits.PlanAllow = slices.Clone(row.Limits.PlanAllow)
	copy.Limits.NoAffinityGroupIDs = slices.Clone(row.Limits.NoAffinityGroupIDs)
	copy.Limits.ScopeLimits = slices.Clone(row.Limits.ScopeLimits)
	return &copy
}
