package proxy

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAnthropicThinkingTokensFromUsage(t *testing.T) {
	cases := map[string]struct {
		usage string
		want  *anthropicOutputTokensDetails
	}{
		"present":          {`{"output_tokens":518,"output_tokens_details":{"reasoning_tokens":163}}`, &anthropicOutputTokensDetails{ThinkingTokens: 163}},
		"explicit zero":    {`{"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0}}`, &anthropicOutputTokensDetails{ThinkingTokens: 0}},
		"clamped":          {`{"output_tokens":10,"output_tokens_details":{"reasoning_tokens":40}}`, &anthropicOutputTokensDetails{ThinkingTokens: 10}},
		"absent":           {`{"output_tokens":5}`, nil},
		"null":             {`{"output_tokens":5,"output_tokens_details":{"reasoning_tokens":null}}`, nil},
		"negative ignored": {`{"output_tokens":5,"output_tokens_details":{"reasoning_tokens":-1}}`, nil},
		"string ignored":   {`{"output_tokens":5,"output_tokens_details":{"reasoning_tokens":"7"}}`, nil},
	}
	for name, tc := range cases {
		got := anthropicThinkingTokensFromUsage(gjson.Parse(tc.usage))
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: want nil, got %+v", name, got)
		case tc.want != nil && (got == nil || got.ThinkingTokens != tc.want.ThinkingTokens):
			t.Errorf("%s: want %+v, got %+v", name, tc.want, got)
		}
	}
}

func TestAnthropicStreamTranslatorReportsThinkingTokens(t *testing.T) {
	tr := newAnthropicStreamTranslator("claude-sonnet-4-5")
	tr.translateEvent([]byte(`{"type":"response.created"}`))
	completed := []byte(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":420,"output_tokens":518,"output_tokens_details":{"reasoning_tokens":163}}}}`)
	var usage *anthropicUsage
	for _, evt := range tr.translateEvent(completed) {
		if evt.Type == "message_delta" && evt.Usage != nil {
			usage = evt.Usage
		}
	}
	if usage == nil || usage.OutputTokensDetails == nil || usage.OutputTokensDetails.ThinkingTokens != 163 {
		t.Fatalf("message_delta usage = %+v, want thinking_tokens 163", usage)
	}
	if usage.OutputTokens != 518 {
		t.Fatal("output_tokens must stay inclusive of thinking")
	}
	encoded, _ := json.Marshal(usage)
	if gjson.GetBytes(encoded, "output_tokens_details.thinking_tokens").Int() != 163 {
		t.Fatalf("wire spelling must be output_tokens_details.thinking_tokens: %s", encoded)
	}

	// 上游没报 reasoning_tokens 时整个字段省略,而不是写 0。
	tr = newAnthropicStreamTranslator("claude-sonnet-4-5")
	tr.translateEvent([]byte(`{"type":"response.created"}`))
	for _, evt := range tr.translateEvent([]byte(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":2}}}`)) {
		if evt.Type == "message_delta" && evt.Usage != nil {
			encoded, _ := json.Marshal(evt.Usage)
			if gjson.GetBytes(encoded, "output_tokens_details").Exists() {
				t.Fatalf("output_tokens_details must be omitted when upstream did not report it: %s", encoded)
			}
		}
	}
}

func TestBuildAnthropicResponseFromCompletedReportsThinkingTokens(t *testing.T) {
	completed := []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":7,"output_tokens":9,"output_tokens_details":{"reasoning_tokens":4}}}}`)
	resp := buildAnthropicResponseFromCompleted(completed, "claude-sonnet-4-5")
	if resp == nil || resp.Usage.OutputTokensDetails == nil || resp.Usage.OutputTokensDetails.ThinkingTokens != 4 {
		t.Fatalf("non-stream usage = %+v, want thinking_tokens 4", resp.Usage)
	}
	encoded, _ := json.Marshal(resp)
	if gjson.GetBytes(encoded, "usage.output_tokens_details.thinking_tokens").Int() != 4 || gjson.GetBytes(encoded, "usage.output_tokens").Int() != 9 {
		t.Fatalf("unexpected usage wire shape: %s", gjson.GetBytes(encoded, "usage").Raw)
	}
}
