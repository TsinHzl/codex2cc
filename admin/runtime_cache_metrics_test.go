package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/codex2cc/cache"
	"github.com/gin-gonic/gin"
)

type metricsTokenCache struct{ cache.TokenCache }

func (c metricsTokenCache) Stats() cache.PoolStats {
	return cache.PoolStats{TotalConns: 10, IdleConns: 4, StaleConns: 99, WaitCount: 23, WaitDurationNs: 4500, Timeouts: 2, PendingRequests: 3}
}
func (c metricsTokenCache) PoolSize() int { return 20 }

func TestRuntimeCacheMetricsDoNotSubtractRemovedConnections(t *testing.T) {
	h, _, _ := newResponseCacheSettingsAdminHandler(t)
	h.cache = metricsTokenCache{h.cache}
	status := h.runtimeCacheStatus(context.Background())
	if status.UsagePercent != 30 || status.WaitCount != 23 || status.Timeouts != 2 || status.PendingRequests != 3 || status.WaitDurationNs != 4500 {
		t.Fatalf("runtime metrics: %+v", status)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/admin/ops/overview", nil)
	h.GetOpsOverview(c)
	var overview opsOverviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || overview.Redis.UsagePercent != 30 || overview.Redis.WaitCount != 23 || overview.Redis.PendingRequests != 3 || overview.Redis.Timeouts != 2 || overview.Redis.WaitDurationNs != 4500 {
		t.Fatalf("ops metrics: %+v", overview.Redis)
	}
}
