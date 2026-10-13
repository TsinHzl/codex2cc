package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/cache"
	"github.com/codex2cc/config"
	"github.com/codex2cc/database"
	"github.com/codex2cc/proxy"
	"github.com/gin-gonic/gin"
)

func TestAdminChangesInvalidateAPIKeyAuthCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestAdminDB(t)
	tc := cache.NewMemory(1)
	t.Cleanup(func() { _ = tc.Close() })
	store := auth.NewStore(db, tc, nil)
	t.Cleanup(store.Stop)
	p := proxy.NewHandler(store, db, &config.Config{APIKeyAuthCacheEnabled: true}, nil)
	p.SetRuntimeCache(tc)
	t.Cleanup(p.CloseAPIKeyAuthCache)
	h := NewHandler(store, db, tc, nil, "admin-test")
	h.SetAPIKeyAuthCacheHandler(p)
	r := gin.New()
	h.RegisterRoutes(r)
	r.GET("/guard", p.APIKeyAuthMiddleware(), func(c *gin.Context) {
		status, _ := p.EnforceAPIKeyLimits(c, "gpt-5.4")
		if status == 0 {
			status = 200
		}
		c.Status(status)
	})
	request := func(method, path, key string, body any) int {
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if key == "" {
			req.Header.Set("X-Admin-Key", "admin-test")
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	key := "sk-admin-auth-cache"
	id, err := db.InsertAPIKeyWithOptions(context.Background(), database.APIKeyInput{Name: "test", Key: key, Limits: database.APIKeyLimits{ModelAllow: []string{"gpt-5.4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := request("GET", "/guard", key, nil); got != 200 {
		t.Fatalf("initial guard: %d", got)
	}
	if got := request("GET", "/guard", key, nil); got != 200 || p.APIKeyAuthCacheStats().LocalHits == 0 {
		t.Fatal("L1 did not serve repeated request")
	}
	path := fmt.Sprintf("/api/admin/keys/%d", id)
	if got := request("PATCH", path, "", map[string]any{"limits": map[string]any{"model_allow": []string{"other-model"}}}); got != 200 {
		t.Fatalf("patch limits: %d", got)
	}
	if got := request("GET", "/guard", key, nil); got != 403 {
		t.Fatalf("cached model permission bypassed new restriction: %d", got)
	}
	if got := request("PATCH", path, "", map[string]any{"enabled": false}); got != 200 {
		t.Fatalf("disable: %d", got)
	}
	if got := request("GET", "/guard", key, nil); got != 401 {
		t.Fatalf("cached key bypassed disable: %d", got)
	}
	if got := request("DELETE", path, "", nil); got != 200 {
		t.Fatalf("delete: %d", got)
	}
	if got := request("GET", "/guard", key, nil); got == 200 {
		t.Fatal("deleted key authenticated")
	}
}
