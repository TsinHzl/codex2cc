package proxy

import (
	"testing"
	"time"
)

// 复现真实序列：function_call arguments 流中途到达 reasoning summary 事件，
// 触发 flush → beginThinkingTextBlock 提前关闭 tool_use 块，
// 后续 input_json_delta 落到错误的块（客户端报 "Content block is not a text block"）。
func TestAnthropicThinkingFlushDuringToolArguments(t *testing.T) {
	tr := newAnthropicStreamTranslatorWithThinkingText("gpt-5.6-sol", time.Now())
	seq := []string{
		`{"type":"response.output_item.added","item":{"type":"reasoning"}}`,
		`{"type":"response.reasoning_summary_part.added","summary_index":0}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"**Thinking**"}`, // pending 残留
		// reasoning item 未 done，直接进入 function_call
		`{"type":"response.output_item.added","item":{"type":"function_call","name":"Bash","call_id":"call_1"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"command\":"}`,
		// arguments 流中途来了 reasoning 事件
		`{"type":"response.reasoning_summary_part.added","summary_index":1}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"more"}`,
		`{"type":"response.function_call_arguments.delta","delta":"\"ls\"}"}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","name":"Bash","call_id":"call_1"}}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{}}}`,
	}
	verifyStreamInvariants(t, tr, seq)
}

func verifyStreamInvariants(t *testing.T, tr *anthropicStreamTranslator, seq []string) {
	t.Helper()
	blockTypes := map[int]string{}
	open := map[int]bool{}
	for _, ev := range seq {
		for _, evt := range tr.translateEvent([]byte(ev)) {
			idx := 0
			if evt.Index != nil {
				idx = *evt.Index
			}
			switch evt.Type {
			case "content_block_start":
				if open[idx] {
					t.Fatalf("重复 start index=%d (%s) ev=%s", idx, evt.ContentBlock.Type, ev)
				}
				blockTypes[idx] = evt.ContentBlock.Type
				open[idx] = true
			case "content_block_stop":
				if !open[idx] {
					t.Fatalf("stop 未打开的块 index=%d ev=%s", idx, ev)
				}
				open[idx] = false
				delete(blockTypes, idx)
			case "content_block_delta":
				typ, ok := blockTypes[idx]
				if !ok || !open[idx] {
					t.Fatalf("delta 落在未打开块 index=%d delta=%s ev=%s", idx, evt.Delta.Type, ev)
				}
				bad := (evt.Delta.Type == "text_delta" && typ != "text") ||
					(evt.Delta.Type == "input_json_delta" && typ != "tool_use") ||
					((evt.Delta.Type == "thinking_delta" || evt.Delta.Type == "signature_delta") && typ != "thinking")
				if bad {
					t.Fatalf("%s 落在 %s 块 index=%d ev=%s", evt.Delta.Type, typ, idx, ev)
				}
			}
		}
	}
}
