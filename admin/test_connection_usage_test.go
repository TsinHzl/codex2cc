package admin

import (
	"testing"
)

func TestConnectionTestReasoningEffort(t *testing.T) {
	if got := connectionTestReasoningEffort([]byte(`{"model":"gpt-5.5","reasoning":{"effort":"xhigh"}}`)); got != "xhigh" {
		t.Fatalf("responses effort = %q, want xhigh", got)
	}
	if got := connectionTestReasoningEffort([]byte(`{"model":"gpt-5.5"}`)); got != "" {
		t.Fatalf("missing effort = %q, want empty", got)
	}
}

func TestConnectionTestUsageFromCodexProjectsDiagnostics(t *testing.T) {
	d := &codexTestDiagnostics{
		HTTPStatus:     200,
		DurationMS:     int64Ptr(1500),
		FirstContentMS: int64Ptr(400),
		ResponseModel:  "gpt-5.5-2026-01-01",
		Transport:      "websocket",
		RequestID:      "req_abc",
		Usage: &codexTestUsage{
			InputTokens: int64Ptr(30), OutputTokens: int64Ptr(12),
			CachedTokens: int64Ptr(8), ReasoningTokens: int64Ptr(5),
		},
	}
	in := connectionTestUsageFromCodex(d, "gpt-5.5")
	if in.Model != "gpt-5.5" || in.EffectiveModel != "gpt-5.5-2026-01-01" {
		t.Fatalf("model projection = %+v", in)
	}
	if in.StatusCode != 200 || in.DurationMs != 1500 || in.FirstTokenMs != 400 || !in.ViaWebsocket || in.UpstreamRequestID != "req_abc" {
		t.Fatalf("timing/transport projection = %+v", in)
	}
	if in.InputTokens != 30 || in.OutputTokens != 12 || in.CachedTokens != 8 || in.ReasoningTokens != 5 {
		t.Fatalf("usage projection = %+v", in)
	}
	// 没有首内容时间时退回首帧时间(WebSocket 传输)。
	d.FirstContentMS = nil
	d.FirstFrameMS = int64Ptr(120)
	if in := connectionTestUsageFromCodex(d, "gpt-5.5"); in.FirstTokenMs != 120 {
		t.Fatalf("first token fallback = %d, want 120", in.FirstTokenMs)
	}
}
