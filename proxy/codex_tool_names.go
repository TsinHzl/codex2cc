package proxy

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// Codex 上游要求函数/自定义工具名匹配 ^[a-zA-Z0-9_-]{1,64}$。Chat Completions
// 客户端（尤其是把 MCP 工具原名透传的 agent 框架）常带 `.`、`:`、空格或非 ASCII，
// 原样转发必 400 "Invalid tools[N].name"。这里在 Chat→Responses 转换时把非法
// 字符替换成 `_`、超长截断并去重，响应侧再按同一张映射把名字还原，客户端看到
// 的仍是原名。合法名字不进映射，常规请求零开销。
//
// 只作用于 /v1/chat/completions 路由：原生 Responses 请求视为已按上游规范构造，
// 其他协议路由有自己的命名空间映射。
const codexToolNameLimit = 64

func isCodexToolNameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
}

// sanitizeCodexToolName 把 [a-zA-Z0-9_-] 之外的字符替换成下划线。
func sanitizeCodexToolName(name string) string {
	changed := false
	var sb strings.Builder
	for _, r := range name {
		if isCodexToolNameRune(r) {
			sb.WriteRune(r)
			continue
		}
		sb.WriteByte('_')
		changed = true
	}
	if !changed {
		return name
	}
	return sb.String()
}

// shortenCodexToolName 净化后再按 64 字节截断；mcp__ 前缀的名字优先保留前缀与
// 最后一段（服务器名段最长也最没有区分度）。净化后全是 ASCII，按字节切安全。
func shortenCodexToolName(name string) string {
	sanitized := sanitizeCodexToolName(name)
	if len(sanitized) <= codexToolNameLimit {
		return sanitized
	}
	if strings.HasPrefix(sanitized, "mcp__") {
		if idx := strings.LastIndex(sanitized, "__"); idx > 0 {
			candidate := "mcp__" + sanitized[idx+2:]
			if len(candidate) > codexToolNameLimit {
				candidate = candidate[:codexToolNameLimit]
			}
			return candidate
		}
	}
	return sanitized[:codexToolNameLimit]
}

// collectChatToolNames 按确定顺序收集请求里出现的全部工具名：tools 声明、
// tool_choice、历史 assistant tool_calls。请求侧改名与响应侧还原都从同一份
// 列表推导，保证两边的去重后缀一致。
func collectChatToolNames(req openAIRequest) []string {
	var names []string
	seen := map[string]struct{}{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, raw := range req.Tools {
		tool := gjson.ParseBytes(raw)
		switch tool.Get("type").String() {
		case "function", "":
			name := tool.Get("function.name").String()
			if name == "" {
				name = tool.Get("name").String()
			}
			add(name)
		case "custom":
			name := tool.Get("custom.name").String()
			if name == "" {
				name = tool.Get("name").String()
			}
			add(name)
		}
	}
	if choice := gjson.ParseBytes(req.ToolChoice); choice.IsObject() {
		switch choice.Get("type").String() {
		case "function":
			name := choice.Get("function.name").String()
			if name == "" {
				name = choice.Get("name").String()
			}
			add(name)
		case "custom":
			name := choice.Get("custom.name").String()
			if name == "" {
				name = choice.Get("name").String()
			}
			add(name)
		}
	}
	for _, msg := range req.Messages {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Type == "custom" && tc.Custom != nil {
				add(tc.Custom.Name)
				continue
			}
			add(tc.Function.Name)
		}
	}
	return names
}

// buildCodexToolNameMap 生成 原名 → 上游名 的映射，只收录需要改写的名字。
// 先把本来就合法的名字占位，再给非法名字分配去重后缀，避免把合法名字挤走。
func buildCodexToolNameMap(names []string) map[string]string {
	used := make(map[string]struct{}, len(names))
	var pending []string
	for _, name := range names {
		if shortenCodexToolName(name) == name {
			used[name] = struct{}{}
			continue
		}
		pending = append(pending, name)
	}
	if len(pending) == 0 {
		return nil
	}
	mapped := make(map[string]string, len(pending))
	for _, name := range pending {
		short := uniqueCodexToolName(shortenCodexToolName(name), used)
		used[short] = struct{}{}
		mapped[name] = short
	}
	return mapped
}

func uniqueCodexToolName(base string, used map[string]struct{}) string {
	if _, taken := used[base]; !taken {
		return base
	}
	for i := 1; ; i++ {
		suffix := "_" + strconv.Itoa(i)
		candidate := base
		if len(candidate)+len(suffix) > codexToolNameLimit {
			candidate = candidate[:codexToolNameLimit-len(suffix)]
		}
		candidate += suffix
		if _, taken := used[candidate]; !taken {
			return candidate
		}
	}
}

// applyCodexToolNameMap 把映射套到已转换好的 Responses 请求体：tools 声明、
// tool_choice、input 里的 function_call / custom_tool_call 历史项。
func applyCodexToolNameMap(out map[string]any, nameMap map[string]string) {
	if len(nameMap) == 0 || out == nil {
		return
	}
	rename := func(item map[string]any) {
		name, ok := item["name"].(string)
		if !ok {
			return
		}
		if short, ok := nameMap[name]; ok {
			item["name"] = short
		}
	}
	if tools, ok := out["tools"].([]any); ok {
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok || isReservedCodexTool(tool) {
				continue
			}
			switch strings.TrimSpace(firstNonEmptyAnyString(tool["type"])) {
			case "function", "custom":
				rename(tool)
			}
		}
	}
	if choice, ok := out["tool_choice"].(map[string]any); ok {
		rename(choice)
	}
	if input, ok := out["input"].([]any); ok {
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch strings.TrimSpace(firstNonEmptyAnyString(item["type"])) {
			case "function_call", "custom_tool_call":
				rename(item)
			}
		}
	}
}

// ChatToolNameRestoreMap 返回 上游名 → 原名 的还原映射；请求里没有需要改写的
// 名字时返回 nil。与 TranslateRequest 从同一份解析结果推导。
func ChatToolNameRestoreMap(rawJSON []byte) map[string]string {
	nameMap := buildCodexToolNameMap(collectChatToolNames(cachedOrParse(rawJSON)))
	if len(nameMap) == 0 {
		return nil
	}
	restore := make(map[string]string, len(nameMap))
	for original, short := range nameMap {
		restore[short] = original
	}
	return restore
}

func restoreCodexToolName(restore map[string]string, name string) string {
	if len(restore) == 0 {
		return name
	}
	if original, ok := restore[name]; ok {
		return original
	}
	return name
}

// restoreToolCallNames 把非流式 Chat 响应里的工具调用名还原成客户端原名。
func restoreToolCallNames(toolCalls []ToolCallResult, restore map[string]string) []ToolCallResult {
	if len(restore) == 0 {
		return toolCalls
	}
	for i := range toolCalls {
		toolCalls[i].Name = restoreCodexToolName(restore, toolCalls[i].Name)
	}
	return toolCalls
}
