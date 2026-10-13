package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codex2cc/auth"
	"github.com/tidwall/gjson"
)

func turnStateTraceContext() (context.Context, *upstreamTraceAudit) {
	audit := &upstreamTraceAudit{requestID: "req-1"}
	return context.WithValue(context.Background(), upstreamTraceContextKey{}, audit), audit
}

// 凭据级注入是强制覆盖：写在客户端回带值与账号自定义头之后，还要挺过 HTTP 最后一跳
// 的头装配；同时出站头与追踪（用量日志）对"注入了没有"必须给出同一个答案。
func TestExecuteRequestInjectsCredentialTurnState(t *testing.T) {
	const injected = "gAAAAABcredential-turn-state"
	for _, tc := range []struct {
		name         string
		scope        string
		clientModel  string
		customHeader string
		wantHeader   string
		wantInjected string
	}{
		{name: "override beats client header and custom header", customHeader: "custom-header-state", wantHeader: injected, wantInjected: injected},
		{name: "scope hit on upstream model", scope: "gpt-5*", wantHeader: injected, wantInjected: injected},
		{name: "scope hit on client model after mapping", scope: "my-alias", clientModel: "my-alias", wantHeader: injected, wantInjected: injected},
		{name: "scope miss keeps client echo", scope: "claude-*", wantHeader: "client-state"},
		{name: "scope miss lets custom header win as before", scope: "claude-*", customHeader: "custom-header-state", wantHeader: "custom-header-state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &auth.Account{DBID: 4242, AccessToken: "token-1", CodexTurnState: injected, CodexTurnStateModels: tc.scope}
			if tc.customHeader != "" {
				account.CustomHeaders = map[string]string{"X-Codex-Turn-State": tc.customHeader}
			}

			var capturedHeader http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedHeader = r.Header.Clone()
				w.Header().Set("X-Codex-Turn-State", "minted-by-upstream")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			previousResin := resinCfg.Load()
			t.Cleanup(func() { resinCfg.Store(previousResin) })
			SetResinConfig(&ResinConfig{BaseURL: server.URL, PlatformName: "test"})
			clientPool.Delete(fmt.Sprintf("resin|%d", account.ID()))

			ctx, _ := turnStateTraceContext()
			ctx = WithCodexClientModel(ctx, tc.clientModel)
			downstream := http.Header{}
			downstream.Set("X-Codex-Turn-State", "client-state")
			resp, err := ExecuteRequest(ctx, account, []byte(`{"model":"gpt-5.5","input":"hi"}`), "", "", "api-key-1", nil, downstream, false)
			if err != nil {
				t.Fatalf("ExecuteRequest: %v", err)
			}
			_ = resp.Body.Close()
			if got := capturedHeader.Get("X-Codex-Turn-State"); got != tc.wantHeader {
				t.Fatalf("outbound X-Codex-Turn-State = %q, want %q", got, tc.wantHeader)
			}
			if got := downstream.Get("X-Codex-Turn-State"); got != "client-state" {
				t.Fatalf("downstream headers mutated: %q", got)
			}
			snapshot := snapshotUpstreamTrace(ctx)
			if snapshot.InjectedTurnState != tc.wantInjected {
				t.Fatalf("trace injected = %q, want %q", snapshot.InjectedTurnState, tc.wantInjected)
			}
			if snapshot.UpstreamTurnState != "minted-by-upstream" {
				t.Fatalf("trace upstream = %q, want minted-by-upstream", snapshot.UpstreamTurnState)
			}
		})
	}
}

// WS 路径：握手头逐连接冻结，复用连接只认帧体，所以帧体 client_metadata 必须写。
func TestPrepareCodexTurnStateInjectionWebsocketBody(t *testing.T) {
	account := &auth.Account{DBID: 7, CodexTurnState: "ws-state"}
	ctx, body, headers := prepareCodexTurnStateInjection(context.Background(), account, []byte(`{"model":"gpt-5.5"}`), nil, true)
	if got := gjson.GetBytes(body, "client_metadata.x-codex-turn-state").String(); got != "ws-state" {
		t.Fatalf("frame client_metadata = %q, want ws-state", got)
	}
	if got := headers.Get("X-Codex-Turn-State"); got != "ws-state" {
		t.Fatalf("handshake header = %q, want ws-state", got)
	}
	if got := CodexTurnStateInjectionFromContext(ctx); got != "ws-state" {
		t.Fatalf("ctx injection = %q, want ws-state", got)
	}
	// HTTP 路径不动帧体。
	_, httpBody, _ := prepareCodexTurnStateInjection(context.Background(), account, []byte(`{"model":"gpt-5.5"}`), nil, false)
	if gjson.GetBytes(httpBody, "client_metadata").Exists() {
		t.Fatal("HTTP path must not fabricate client_metadata")
	}
	// 未配置：全部原样。
	plain := &auth.Account{DBID: 8}
	ctx, body, headers = prepareCodexTurnStateInjection(context.Background(), plain, []byte(`{"model":"gpt-5.5"}`), nil, true)
	if headers != nil || gjson.GetBytes(body, "client_metadata").Exists() || CodexTurnStateInjectionFromContext(ctx) != "" {
		t.Fatal("unconfigured account must be a no-op")
	}
}

func TestObserveCodexTurnStateFrame(t *testing.T) {
	ctx, audit := turnStateTraceContext()
	audit.current = &upstreamTraceAttempt{accountID: 1}
	ObserveCodexTurnStateFrame(ctx, []byte(`{"type":"response.output_text.delta","delta":"turn-state is a phrase"}`))
	if audit.current.upstreamTurnState != "" {
		t.Fatalf("content frame must not be treated as turn state: %q", audit.current.upstreamTurnState)
	}
	ObserveCodexTurnStateFrame(ctx, []byte(`{"type":"response.metadata","headers":{"X-Codex-Turn-State":"frame-state"}}`))
	if audit.current.upstreamTurnState != "frame-state" {
		t.Fatalf("metadata frame turn state = %q, want frame-state", audit.current.upstreamTurnState)
	}
	ObserveCodexTurnStateFrame(ctx, []byte(`{"type":"response.metadata","client_metadata":{"x-codex-turn-state":"later-state"}}`))
	if audit.current.upstreamTurnState != "later-state" {
		t.Fatalf("later frame must win: %q", audit.current.upstreamTurnState)
	}
	if got := codexTurnStateFromFrame([]byte(`{"headers":{"x-codex-turn-state":"bad\nvalue"}}`)); got != "" {
		t.Fatalf("control characters must be rejected: %q", got)
	}
}
