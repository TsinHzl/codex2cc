package proxy

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/config"
	"github.com/codex2cc/database"
	"github.com/gin-gonic/gin"
)

// TestKeepaliveReadCancellationPreservesDrainBudget checks the stream pump,
// including a body that stays silent past the downstream cancellation.
func TestKeepaliveReadCancellationPreservesDrainBudget(t *testing.T) {
	for _, continuous := range []bool{false, true} {
		name := "ordinary"
		if continuous {
			name = "continuous-retry"
		}
		t.Run(name, func(t *testing.T) {
			clientCtx, cancelClient := context.WithCancel(context.Background())
			defer cancelClient()
			keepalive := &requestContinuousRetryKeepalive{ctx: clientCtx}
			keepalive.Activate()
			clientCtx = context.WithValue(clientCtx, continuousRetryKeepaliveContextKey{}, continuousRetryKeepalive(keepalive))
			upstreamCtx, cancelUpstream := newDrainableUpstreamContext(clientCtx, 200*time.Millisecond)
			defer cancelUpstream()
			readCtx := upstreamResponseReadContext(clientCtx, upstreamCtx, database.ContinuousRetryPolicy{Enabled: continuous})
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- readSSEStreamWithContinuousRetryKeepalive(readCtx, reader, func(_ string, _ []byte) bool {
					close(started)
					return true
				})
			}()
			_, err := io.WriteString(writer, "data: {\"type\":\"response.created\"}\n\n")
			if err != nil {
				t.Fatal(err)
			}
			<-started
			cancelClient()
			if !continuous {
				select {
				case err := <-done:
					t.Fatalf("ordinary read skipped its drain window: %v", err)
				case <-time.After(30 * time.Millisecond):
				}
			}
			timeout := time.Second
			if continuous {
				timeout = 100 * time.Millisecond
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("read cancellation = %v", err)
				}
			case <-time.After(timeout):
				t.Fatal("stream read exceeded its cancellation budget")
			}
		})
	}
}

// TestClaudeDisabledKeepaliveSurvivesQuotaAdmission exercises the authenticated
// Messages path, including the real quota store and Claude executor.
// TestClaudeDisabledKeepaliveSurvivesQuotaAdmission 随 claude 原生上游一起从
// codex2cc 裁剪：auth.UpstreamClaude、ClaudeAuthKind 与 installClaudeBoundaryTransport
// 均不再存在，Claude keepalive 开关随渠道移除。

// TestMessagesKeepalivePreservesPendingQuotaRejection holds a real SQLite
// writer lock so heartbeat deadlines elapse before the exhausted quota is read.
func TestMessagesKeepalivePreservesPendingQuotaRejection(t *testing.T) {
	previous := continuousRetryKeepaliveInterval
	continuousRetryKeepaliveInterval = 5 * time.Millisecond
	t.Cleanup(func() { continuousRetryKeepaliveInterval = previous })
	dbPath := filepath.Join(t.TempDir(), "locked-quota.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	keyID, err := db.InsertAPIKeyWithOptions(context.Background(), database.APIKeyInput{
		Key: modelQuotaTestKey, Name: "review",
		Limits: database.APIKeyLimits{ModelRequestLimits: []database.APIKeyModelRequestLimit{{Model: "gpt-6*", MaxRequests: 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAPIKeyByID(context.Background(), keyID)
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 4, MaxRetries: 0, MaxRateLimitRetries: 0})
	t.Cleanup(store.Stop)
	store.AddAccount(&auth.Account{DBID: 1, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "http://127.0.0.1:1", APIKey: "review", PlanType: "api", Models: []string{"gpt-6-astra"}})
	h := NewHandler(store, db, &config.Config{}, nil)
	hit, err := h.db.ConsumeAPIKeyModelRequest(context.Background(), row.ID, "review-exhaust-budget", "gpt-6-astra", row.Limits.ModelRequestLimits, time.Now())
	if err != nil || hit != nil {
		t.Fatalf("prime quota: %v %v", hit, err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-6-astra","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	c.Set(contextAPIKeyRow, row)
	c.Set(contextAPIKeyID, row.ID)
	h.attachAPIKeyModelRequestQuota(c, false)
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	unlocked := make(chan struct{})
	go func() {
		time.Sleep(60 * time.Millisecond)
		_, unlockErr := conn.ExecContext(context.Background(), "ROLLBACK")
		if unlockErr != nil {
			t.Errorf("release SQLite write lock: %v", unlockErr)
		}
		close(unlocked)
	}()
	h.Messages(c)
	<-unlocked
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("pending quota was already committed: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

type cancelOnContentWriter struct {
	*httptest.ResponseRecorder
	cancel                context.CancelFunc
	once                  sync.Once
	heartbeatsAfterCancel int
	failHeartbeat         bool
	disconnected          bool
}

func (w *cancelOnContentWriter) WriteString(value string) (int, error) {
	return w.Write([]byte(value))
}

func (w *cancelOnContentWriter) Write(p []byte) (int, error) {
	if w.disconnected && (strings.Contains(string(p), ": keepalive") || strings.Contains(string(p), "event: ping")) {
		w.heartbeatsAfterCancel++
		if w.failHeartbeat {
			return 0, io.ErrClosedPipe
		}
	}
	n, err := w.ResponseRecorder.Write(p)
	if strings.Contains(string(p), "review-content") {
		w.disconnected = true
		if !w.failHeartbeat {
			w.once.Do(w.cancel)
		}
	}
	return n, err
}

// TestDownstreamKeepaliveDrainsUsageAfterClientCancel covers the Messages
// translator with downstream heartbeats.（responses-relay / responses-codex /
// chat 子用例随 /v1/responses 与 /v1/chat/completions 入口一起从 codex2cc 裁剪。）
func TestDownstreamKeepaliveDrainsUsageAfterClientCancel(t *testing.T) {
	previous := continuousRetryKeepaliveInterval
	continuousRetryKeepaliveInterval = 5 * time.Millisecond
	t.Cleanup(func() { continuousRetryKeepaliveInterval = previous })
	for _, tc := range []struct {
		name, path, body string
		native           bool
	}{
		{"messages", "/v1/messages", `{"model":"gpt-6-astra","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`, false},
	} {
		for _, disconnect := range []string{"context-canceled", "heartbeat-write-failed"} {
			t.Run(tc.name+"/"+disconnect, func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_review\"}}\n\n"+
						"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n\n"+
						"data: {\"type\":\"response.output_text.delta\",\"delta\":\"review-content\"}\n\n")
					w.(http.Flusher).Flush()
					time.Sleep(60 * time.Millisecond)
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_review\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1000,\"output_tokens\":40}}}\n\n")
				}))
				t.Cleanup(upstream.Close)
				h, _, router := newModelQuotaTestHandler(t, 2, upstream.URL, tc.native)
				if tc.native {
					previousResin := resinCfg.Load()
					t.Cleanup(func() { resinCfg.Store(previousResin) })
					SetResinConfig(&ResinConfig{BaseURL: upstream.URL, PlatformName: "keepalive-test"})
				}
				clientCtx, cancel := context.WithCancel(context.Background())
				defer cancel()
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)).WithContext(clientCtx)
				req.Header.Set("Authorization", "Bearer "+modelQuotaTestKey)
				req.Header.Set("Content-Type", "application/json")
				w := &cancelOnContentWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, failHeartbeat: disconnect == "heartbeat-write-failed"}
				router.ServeHTTP(w, req)
				if !w.disconnected {
					t.Fatalf("fixture never canceled downstream after content: status=%d body=%q", w.Code, w.Body.String())
				}
				wantHeartbeats := 0
				if w.failHeartbeat {
					wantHeartbeats = 1
				}
				if w.heartbeatsAfterCancel != wantHeartbeats {
					t.Fatalf("sent %d heartbeats after client cancellation", w.heartbeatsAfterCancel)
				}
				h.db.FlushUsageLogs()
				logs, err := h.db.ListUsageLogsByFilter(context.Background(), database.UsageLogFilter{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute), IncludeCanceled: true})
				if err != nil {
					t.Fatal(err)
				}
				if len(logs) != 1 {
					t.Fatalf("usage log count=%d, want 1", len(logs))
				}
				entry := logs[0]
				t.Logf("status=%d prompt=%d completion=%d total=%d", entry.StatusCode, entry.PromptTokens, entry.CompletionTokens, entry.TotalTokens)
				if entry.PromptTokens != 1000 || entry.CompletionTokens != 40 {
					t.Fatalf("terminal usage lost after client cancel: prompt=%d completion=%d, want 1000/40", entry.PromptTokens, entry.CompletionTokens)
				}
			})
		}
	}
}
