package proxy

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestShortenCodexToolName(t *testing.T) {
	cases := map[string]string{
		"read_file":       "read_file",
		"mcp.server.tool": "mcp_server_tool",
		"my tool:run":     "my_tool_run",
		"查询天气":            "____",
		"a-b_C9":          "a-b_C9",
		"mcp__" + strings.Repeat("s", 70) + "__last": "mcp__last",
		strings.Repeat("x", 70):                      strings.Repeat("x", 64),
	}
	for in, want := range cases {
		if got := shortenCodexToolName(in); got != want {
			t.Errorf("shortenCodexToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildCodexToolNameMap_ValidNamesUntouchedAndCollisionsSuffixed(t *testing.T) {
	// "a_b" 是合法原名，必须保住；"a.b" 与 "a:b" 都会净化成 "a_b"，得让位。
	names := []string{"a.b", "a_b", "a:b", "plain"}
	m := buildCodexToolNameMap(names)
	if _, ok := m["a_b"]; ok {
		t.Fatal("valid original name must not be renamed")
	}
	if _, ok := m["plain"]; ok {
		t.Fatal("valid original name must not enter the map")
	}
	if m["a.b"] != "a_b_1" {
		t.Fatalf(`m["a.b"] = %q, want a_b_1`, m["a.b"])
	}
	if m["a:b"] != "a_b_2" {
		t.Fatalf(`m["a:b"] = %q, want a_b_2`, m["a:b"])
	}
	if buildCodexToolNameMap([]string{"ok", "fine"}) != nil {
		t.Fatal("all-valid input must yield nil map (zero-cost fast path)")
	}
}

func TestTranslateRequest_SanitizesToolNamesAndRestoreMapRoundTrips(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.5",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "mcp.fs.read", "arguments": "{}"}},
				{"id": "call_2", "type": "custom", "custom": {"name": "shell:exec", "input": "ls"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "ok"},
			{"role": "tool", "tool_call_id": "call_2", "content": "ok"}
		],
		"tools": [
			{"type": "function", "function": {"name": "mcp.fs.read", "parameters": {"type":"object","properties":{}}}},
			{"type": "function", "function": {"name": "mcp_fs_read", "parameters": {"type":"object","properties":{}}}},
			{"type": "custom", "custom": {"name": "shell:exec"}}
		],
		"tool_choice": {"type": "custom", "custom": {"name": "shell:exec"}}
	}`)
	out, err := TranslateRequest(raw)
	if err != nil {
		t.Fatalf("TranslateRequest: %v", err)
	}
	tools := gjson.GetBytes(out, "tools").Array()
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Get("name").String())
	}
	joined := strings.Join(names, ",")
	if strings.Contains(joined, ".") || strings.Contains(joined, ":") {
		t.Fatalf("tool names must be sanitized, got %s", joined)
	}
	if !strings.Contains(joined, "mcp_fs_read_1") {
		t.Fatalf("collision with the valid name mcp_fs_read must be suffixed, got %s", joined)
	}
	if !strings.Contains(joined, "shell_exec") {
		t.Fatalf("custom tool name must be sanitized, got %s", joined)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "shell_exec" {
		t.Fatalf("tool_choice.name = %q", got)
	}
	var historyNames []string
	for _, item := range gjson.GetBytes(out, "input").Array() {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			historyNames = append(historyNames, item.Get("name").String())
		}
	}
	if strings.Join(historyNames, ",") != "mcp_fs_read_1,shell_exec" {
		t.Fatalf("history call names = %v", historyNames)
	}

	restore := ChatToolNameRestoreMap(raw)
	if restore["mcp_fs_read_1"] != "mcp.fs.read" || restore["shell_exec"] != "shell:exec" {
		t.Fatalf("restore map = %v", restore)
	}
	if _, ok := restore["mcp_fs_read"]; ok {
		t.Fatal("valid name must not be in restore map")
	}

	st := NewStreamTranslator("chatcmpl-x", "gpt-5.5", 1)
	st.SetToolNameRestore(restore)
	chunk, _ := st.TranslateParsed(gjson.Parse(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_9","name":"mcp_fs_read_1","arguments":""}}`))
	if got := gjson.GetBytes(chunk, "choices.0.delta.tool_calls.0.function.name").String(); got != "mcp.fs.read" {
		t.Fatalf("stream tool name not restored: %s", chunk)
	}
	calls := restoreToolCallNames([]ToolCallResult{{ID: "call_9", Type: "function_call", Name: "mcp_fs_read_1"}, {ID: "c2", Type: "custom_tool_call", Name: "shell_exec"}}, restore)
	if calls[0].Name != "mcp.fs.read" || calls[1].Name != "shell:exec" {
		t.Fatalf("non-stream tool names not restored: %+v", calls)
	}
}

func TestTranslateRequest_ValidToolNamesLeaveNoRestoreMap(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{}}}}]}`)
	if restore := ChatToolNameRestoreMap(raw); restore != nil {
		t.Fatalf("expected nil restore map, got %v", restore)
	}
	out, err := TranslateRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "read_file" {
		t.Fatalf("tools.0.name = %q", got)
	}
}

func TestTranslateRequest_FunctionToolStrictDefaultsFalse(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"tools":[
		{"type":"function","function":{"name":"omitted","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}},
		{"type":"function","function":{"name":"explicit_true","strict":true,"parameters":{"type":"object","properties":{}}}},
		{"type":"function","function":{"name":"explicit_false","strict":false,"parameters":{"type":"object","properties":{}}}}
	]}`)
	out, err := TranslateRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"false", "true", "false"} {
		strict := gjson.GetBytes(out, "tools."+strconv.Itoa(i)+".strict")
		if !strict.Exists() || strict.Raw != want {
			t.Fatalf("tools[%d].strict = %q, want %s", i, strict.Raw, want)
		}
	}
}

func TestNormalizeFunctionToolsInArray_ChatShapedStrictDefaultsFalse(t *testing.T) {
	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "a", "parameters": map[string]any{"type": "object"}}},
		map[string]any{"type": "function", "name": "native", "parameters": map[string]any{"type": "object"}},
	}
	kept, _ := normalizeFunctionToolsInArray(tools)
	chatShaped := kept[0].(map[string]any)
	if strict, ok := chatShaped["strict"].(bool); !ok || strict {
		t.Fatalf("chat-shaped tool strict = %v, want explicit false", chatShaped["strict"])
	}
	native := kept[1].(map[string]any)
	if _, ok := native["strict"]; ok {
		t.Fatal("native Responses tool must keep the upstream default (no strict key)")
	}
}

func TestConvertAnthropicTools_StrictFalse(t *testing.T) {
	tools := convertAnthropicTools([]anthropicTool{{Name: "Read", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}})
	if len(tools) != 1 {
		t.Fatalf("tools = %d", len(tools))
	}
	item := tools[0].(map[string]any)
	if strict, ok := item["strict"].(bool); !ok || strict {
		t.Fatalf("strict = %v, want false", item["strict"])
	}
}
