package proxy

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 上游偶发以 HTTP 200 + response.incomplete 结束一次"什么都没生成"的响应：
// usage.output_tokens 精确为 0、response.output 为空、此前也没有任何非空 delta
// 或 output_item.done。这不是 max_output_tokens 截断，而是上游静默中止（已知触发
// 之一是工具 schema 里的大 oneOf/const 联合）。原样转发会让 Chat 下游拿到
// finish_reason=length 的空回复且不会重试，看起来像"模型死了"。
//
// 这里把它就地改写成 response.failed，复用既有的首包前透明重试/换号路径。判定
// 条件刻意收紧（数值精确 0、无 output 项、无非空 delta），避免把正常截断重新
// 误判成断流（v2.8.3 修过的那类回归）。故障按请求维度计：不进账号健康度。
const (
	codexEmptyIncompleteErrorCode    = "empty_incomplete_response"
	codexEmptyIncompleteErrorMessage = "upstream terminated with an incomplete empty response (0 output tokens)"
	codexEmptyIncompleteFailureKind  = "empty_incomplete"
)

// emptyIncompleteTracker 记录一次上游尝试内是否出现过真实输出。每个 attempt 一份。
type emptyIncompleteTracker struct {
	sawOutputDelta bool
	outputItems    int
}

// Observe 记录事件。只有非空的文本/思考/工具参数 delta 与 output_item.done 计入。
func (t *emptyIncompleteTracker) Observe(eventType string, parsed gjson.Result) {
	if t == nil {
		return
	}
	switch eventType {
	case "response.output_text.delta",
		"response.reasoning_text.delta",
		"response.reasoning_summary_text.delta",
		"response.function_call_arguments.delta",
		"response.custom_tool_call_input.delta":
		if strings.TrimSpace(parsed.Get("delta").String()) != "" {
			t.sawOutputDelta = true
		}
	case "response.output_item.done":
		t.outputItems++
	}
}

// IsEmptyIncomplete 判断该终态事件是否为"零输出"的 response.incomplete。
func (t *emptyIncompleteTracker) IsEmptyIncomplete(eventType string, parsed gjson.Result) bool {
	if eventType != "response.incomplete" {
		return false
	}
	if t != nil && (t.sawOutputDelta || t.outputItems > 0) {
		return false
	}
	return isEmptyIncompleteResponse(parsed.Get("response"))
}

// isEmptyIncompleteResponse 要求 output 为空且 usage.output_tokens 是字面量整数 0。
// 缺失/null/浮点/非数字一律不算，宁可漏判也不误判。
func isEmptyIncompleteResponse(response gjson.Result) bool {
	if !response.Exists() || !response.IsObject() {
		return false
	}
	if output := response.Get("output"); output.IsArray() && len(output.Array()) > 0 {
		return false
	}
	tokens := response.Get("usage.output_tokens")
	if !tokens.Exists() || tokens.Type != gjson.Number {
		return false
	}
	return strings.TrimSpace(tokens.Raw) == "0"
}

// isEmptyIncompleteResponseBody 是非流式 Responses 对象的同款判定。
func isEmptyIncompleteResponseBody(body []byte) bool {
	root := gjson.ParseBytes(body)
	if strings.ToLower(strings.TrimSpace(root.Get("status").String())) != "incomplete" {
		return false
	}
	return isEmptyIncompleteResponse(root)
}

// synthesizeEmptyIncompleteFailureEvent 把 response.incomplete 事件改写成
// response.failed，保留 response.id/model/usage 等字段供日志与计费使用。
func synthesizeEmptyIncompleteFailureEvent(data []byte) []byte {
	out := append([]byte(nil), data...)
	out, _ = sjson.SetBytes(out, "type", "response.failed")
	out, _ = sjson.DeleteBytes(out, "response.incomplete_details")
	return setEmptyIncompleteFailure(out, "response.")
}

// synthesizeEmptyIncompleteFailureBody 是非流式 Responses 对象的同款改写。
func synthesizeEmptyIncompleteFailureBody(body []byte) []byte {
	out := append([]byte(nil), body...)
	out, _ = sjson.DeleteBytes(out, "incomplete_details")
	return setEmptyIncompleteFailure(out, "")
}

func setEmptyIncompleteFailure(payload []byte, prefix string) []byte {
	payload, _ = sjson.SetBytes(payload, prefix+"status", "failed")
	payload, _ = sjson.SetBytes(payload, prefix+"error.type", "server_error")
	payload, _ = sjson.SetBytes(payload, prefix+"error.code", codexEmptyIncompleteErrorCode)
	payload, _ = sjson.SetBytes(payload, prefix+"error.status_code", http.StatusBadGateway)
	payload, _ = sjson.SetBytes(payload, prefix+"error.message", codexEmptyIncompleteErrorMessage)
	return payload
}

// isEmptyIncompleteFailurePayload 识别本文件合成的失败载荷（流式事件或非流式对象）。
func isEmptyIncompleteFailurePayload(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	return gjson.GetBytes(responseFailedErrorBody(payload), "error.code").String() == codexEmptyIncompleteErrorCode
}

// rewriteEmptyIncompleteTerminal 是各条 SSE 读取循环共用的入口：命中时返回改写后的
// 事件与 response.failed 类型，未命中时原样返回。调用方把返回值赋回本地变量即可。
func rewriteEmptyIncompleteTerminal(tracker *emptyIncompleteTracker, eventType string, data []byte, parsed gjson.Result) (string, []byte, gjson.Result) {
	tracker.Observe(eventType, parsed)
	if !tracker.IsEmptyIncomplete(eventType, parsed) {
		return eventType, data, parsed
	}
	data = synthesizeEmptyIncompleteFailureEvent(data)
	return "response.failed", data, gjson.ParseBytes(data)
}
