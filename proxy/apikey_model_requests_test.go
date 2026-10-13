package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2cc/api"
	"github.com/codex2cc/auth"
	"github.com/codex2cc/config"
	"github.com/codex2cc/database"
	"github.com/gin-gonic/gin"
)

const modelQuotaTestKey = "sk-weekly-model-quota-test"
const modelQuotaSSE = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_quota\"}}\n\n" +
	"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n" +
	"data: {\"type\":\"response.output_text.done\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_quota\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"

func newModelQuotaTestHandler(t *testing.T, limit int64, upstream string, native bool) (*Handler, *database.APIKeyRow, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previous := CurrentRuntimeSettings()
	settings := DefaultRuntimeSettings()
	settings.CodexForceWebsocket = false
	ApplyRuntimeSettings(settings)
	t.Cleanup(func() { ApplyRuntimeSettings(previous) })
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	id, err := db.InsertAPIKeyWithOptions(context.Background(), database.APIKeyInput{
		Key: modelQuotaTestKey, Name: "weekly model quota",
		Limits: database.APIKeyLimits{ModelRequestLimits: []database.APIKeyModelRequestLimit{{Model: "gpt-6*", MaxRequests: limit}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAPIKeyByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 4, MaxRetries: 0, MaxRateLimitRetries: 0})
	t.Cleanup(store.Stop)
	if native {
		store.AddAccount(&auth.Account{DBID: 1, AccessToken: "test-token", PlanType: "pro", Models: []string{"gpt-6-astra", "gpt-5.5"}})
	} else {
		// The early global mapping is deliberately outside the quota; only the
		// later account mapping exposes the actual model that must be charged.
		store.SetCodexModelMapping(`{"team-model":"gpt-5.5"}`)
		store.AddAccount(&auth.Account{DBID: 1, UpstreamType: auth.UpstreamOpenAIResponses,
			BaseURL: upstream, APIKey: "relay-test", PlanType: "api",
			Models: []string{"gpt-6-astra", "gpt-5.5"}, ModelMapping: `{"team-model":"gpt-6-astra"}`})
	}
	h := NewHandler(store, db, &config.Config{}, nil)
	r := gin.New()
	h.RegisterRoutes(r)
	return h, row, r
}

func performModelQuotaRequest(r http.Handler, endpoint, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+modelQuotaTestKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestModelRequestQuotaHTTPMappedModelAndProtocolErrors、
// TestModelRequestQuotaActivatesSSEKeepaliveAfterAdmission、
// TestModelRequestQuotaWebsocketFramesAndOtherModel 随 Responses HTTP 入口、
// ChatCompletions 入口与 WebSocket 入口一起从 codex2cc 裁剪：RegisterRoutes 仅保留
// /v1/messages、/v1/messages/count_tokens、/v1/responses/input_tokens、/v1/models，
// 额度链路保留不依赖入站路由的以下测试继续覆盖。

// TestModelRequestQuotaExecutorRetriesAndFreshRequest 验证同一逻辑请求的重试共享一次额度扣减，
// 且重新进入处理器时仍使用新的请求身份。
func TestModelRequestQuotaExecutorRetriesAndFreshRequest(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":{"message":"test transport attempt"}}`)
	}))
	defer upstream.Close()
	h, row, _ := newModelQuotaTestHandler(t, 1, upstream.URL, false)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(contextAPIKeyRow, row)
	h.attachAPIKeyModelRequestQuota(c, false)
	ctx := c.Request.Context()
	account := h.store.Accounts()[0]
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := ExecuteOpenAIResponsesRequest(ctx, account, []byte(`{"model":"gpt-6-astra","input":"hi"}`), "", nil)
		if err != nil {
			t.Fatalf("internal retry %d: %v", attempt, err)
		}
		_ = resp.Body.Close()
		// Handler re-entry must retain the original request identity.
		h.attachAPIKeyModelRequestQuota(c, false)
	}
	usage, err := h.db.GetAPIKeyModelRequestUsage(context.Background(), row.ID, row.Limits.ModelRequestLimits, time.Now())
	if err != nil || usage[0].Used != 1 || calls.Load() != 3 {
		t.Fatalf("usage=%#v calls=%d err=%v", usage, calls.Load(), err)
	}
	h.attachAPIKeyModelRequestQuota(c, true)
	_, err = ExecuteOpenAIResponsesRequest(c.Request.Context(), account, []byte(`{"model":"gpt-6-astra"}`), "", nil)
	if apiKeyModelRequestError(err) == nil || calls.Load() != 3 {
		t.Fatalf("new request bypassed quota: %v calls=%d", err, calls.Load())
	}
	if isRetryableRequestErrorForContext(context.Background(), err, database.ContinuousRetryPolicy{Enabled: true, CatchAll: true}) {
		t.Fatal("local quota must never enter upstream retry policy")
	}
}

// TestModelRequestQuotaActivatesSSEKeepaliveAfterAdmission 验证额度准入成功后，
// 上游尚未返回响应头时也会激活下游 SSE 保活。（该验证依赖 /v1/responses 入口，
// 随入口裁剪一并移除，保活逻辑由 downstream_keepalive 系列测试经 /v1/messages 覆盖。）

func TestModelRequestQuotaPreviouslyUnrestrictedSnapshotCannotBypassNewRule(t *testing.T) {
	h, row, _ := newModelQuotaTestHandler(t, 1, "", true)
	stale := *row
	stale.Limits = database.APIKeyLimits{}
	newRequest := func() context.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Set(contextAPIKeyRow, &stale)
		h.attachAPIKeyModelRequestQuota(c, false)
		return c.Request.Context()
	}
	if err := ConsumeAPIKeyModelRequestQuota(newRequest(), "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeAPIKeyModelRequestQuota(newRequest(), "gpt-6-astra"); apiKeyModelRequestError(err) == nil {
		t.Fatalf("stale unrestricted auth cache bypassed saved budget: %v", err)
	}
}

// TestModelRequestQuotaAntigravityUsesFinalWireModel 随 antigravity 上游一起
// 从 codex2cc 裁剪：auth.UpstreamAntigravity 与 executeAntigravityInteractionsRequest
// 不再存在。

func TestModelRequestQuotaErrorClaimsRetryTerminal(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	deadline := &continuousRetryDeadline{duration: time.Hour, cancel: cancel}
	ctx = context.WithValue(ctx, continuousRetryDeadlineContextKey{}, deadline)
	deadline.Activate()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	err := &apiKeyModelRequestQuotaError{http.StatusTooManyRequests, api.NewAPIError(api.ErrCodeRateLimitReached, "weekly limit", api.ErrorTypeRateLimit)}
	if !sendAPIKeyModelRequestQuotaError(c, err) {
		t.Fatal("quota error was not handled")
	}
	deadline.mu.Lock()
	settled := deadline.succeeded && deadline.stopped
	deadline.mu.Unlock()
	if !settled || w.Code != 429 {
		t.Fatalf("quota terminal did not settle deadline: settled=%v status=%d", settled, w.Code)
	}
}
