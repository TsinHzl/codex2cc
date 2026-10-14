package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

var anthropicJSONNull = json.RawMessage("null")

const anthropicStreamPingEveryNDeltas = 40

func anthropicStopReason(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func anthropicStopReasonValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ==================== Anthropic Messages API 类型定义 ====================

// anthropicRequest 表示 Anthropic Messages API 请求
type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    *int                   `json:"max_tokens"`
	System       json.RawMessage        `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	Stream       bool                   `json:"stream,omitempty"`
	Temperature  *float64               `json:"temperature,omitempty"`
	TopP         *float64               `json:"top_p,omitempty"`
	TopK         json.RawMessage        `json:"top_k,omitempty"`
	StopSeqs     []string               `json:"stop_sequences,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	ToolChoice   json.RawMessage        `json:"tool_choice,omitempty"`
	Metadata     json.RawMessage        `json:"metadata,omitempty"`
	Speed        string                 `json:"speed,omitempty"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type anthropicOutputConfig struct {
	Effort string          `json:"effort,omitempty"`
	Format json.RawMessage `json:"format,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// anthropicContentBlock 统一内容块（text/thinking/tool_use/tool_result/image）
type anthropicContentBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	Thinking     string                 `json:"thinking,omitempty"`
	Signature    string                 `json:"signature,omitempty"`
	Source       *anthropicImageSource  `json:"source,omitempty"`
	ID           string                 `json:"id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        json.RawMessage        `json:"input,omitempty"`
	ToolUseID    string                 `json:"tool_use_id,omitempty"`
	Content      json.RawMessage        `json:"content,omitempty"`
	IsError      bool                   `json:"is_error,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type,omitempty"`
}

func (b anthropicContentBlock) MarshalJSON() ([]byte, error) {
	type contentBlock struct {
		Type      string                `json:"type"`
		Text      *string               `json:"text,omitempty"`
		Thinking  *string               `json:"thinking,omitempty"`
		Signature string                `json:"signature,omitempty"`
		Source    *anthropicImageSource `json:"source,omitempty"`
		ID        string                `json:"id,omitempty"`
		Name      string                `json:"name,omitempty"`
		Input     json.RawMessage       `json:"input,omitempty"`
		ToolUseID string                `json:"tool_use_id,omitempty"`
		Content   json.RawMessage       `json:"content,omitempty"`
		IsError   bool                  `json:"is_error,omitempty"`
	}

	out := contentBlock{
		Type:      b.Type,
		Signature: b.Signature,
		Source:    b.Source,
		ID:        b.ID,
		Name:      b.Name,
		Input:     b.Input,
		ToolUseID: b.ToolUseID,
		Content:   b.Content,
		IsError:   b.IsError,
	}
	if b.Type == "text" || b.Text != "" {
		out.Text = &b.Text
	}
	if b.Type == "thinking" || b.Thinking != "" {
		out.Thinking = &b.Thinking
	}
	return json.Marshal(out)
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicTool struct {
	Type         string                 `json:"type,omitempty"`
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  json.RawMessage        `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// ==================== Anthropic 响应类型 ====================

// anthropicResponse 非流式响应
type anthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []anthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	StopDetails  json.RawMessage         `json:"stop_details"`
	Usage        anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	// OutputTokensDetails 回传 Codex 的 reasoning_tokens。开了 thinking-token-count
	// beta 的 Claude Code 按 thinking_tokens 拼写读取;output_tokens 已含思考部分,
	// 这里只是细分而非额外计费。缺失时整个字段省略,不能把"没报"说成 0。
	OutputTokensDetails *anthropicOutputTokensDetails `json:"output_tokens_details,omitempty"`
}

type anthropicOutputTokensDetails struct {
	ThinkingTokens int `json:"thinking_tokens"`
}

// anthropicThinkingTokensFromUsage 从 Responses usage 取 reasoning_tokens:只认非负
// 数值,并钳到 output_tokens(思考是输出的子集,不能超过总输出)。
func anthropicThinkingTokensFromUsage(usage gjson.Result) *anthropicOutputTokensDetails {
	detail := usage.Get("output_tokens_details.reasoning_tokens")
	if !detail.Exists() || detail.Type != gjson.Number || detail.Num < 0 {
		return nil
	}
	tokens := int(detail.Int())
	if output := int(usage.Get("output_tokens").Int()); tokens > output {
		tokens = max(output, 0)
	}
	return &anthropicOutputTokensDetails{ThinkingTokens: tokens}
}

// ==================== Anthropic 流式事件类型 ====================

type anthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Message      *anthropicResponse     `json:"message,omitempty"`
	Index        *int                   `json:"index,omitempty"`
	ContentBlock *anthropicContentBlock `json:"content_block,omitempty"`
	Delta        *anthropicDelta        `json:"delta,omitempty"`
	Usage        *anthropicUsage        `json:"usage,omitempty"`
}

type anthropicDelta struct {
	Type         string          `json:"type,omitempty"`
	Text         string          `json:"text,omitempty"`
	PartialJSON  string          `json:"partial_json,omitempty"`
	Thinking     string          `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	StopReason   *string         `json:"stop_reason,omitempty"`
	StopSequence json.RawMessage `json:"stop_sequence,omitempty"`
	StopDetails  json.RawMessage `json:"stop_details,omitempty"`
}

// ==================== 模型映射 ====================

// defaultAnthropicModelMap 默认的模型映射（当数据库中无配置时使用）。
// 目标只用当前上游仍在线的模型：gpt-5.4 全系、gpt-5.3-codex、gpt-5.2 已下线，
// 映射过去只会得到 400。opus/sonnet 落到 gpt-5.5（free/plus/pro 三档均可用），
// haiku 落到 gpt-5.6-luna（上游"更快的模型"档位，同样三档可用）。
var defaultAnthropicModelMap = map[string]string{
	"claude-opus-4-6":            "gpt-5.5",
	"claude-opus-4-6-20250610":   "gpt-5.5",
	"claude-haiku-4-5-20251001":  "gpt-5.6-luna",
	"claude-haiku-4-5":           "gpt-5.6-luna",
	"claude-sonnet-4-6":          "gpt-5.5",
	"claude-sonnet-4-5-20250929": "gpt-5.5",
	"claude-opus-4-5-20251101":   "gpt-5.5",
	"claude-sonnet-4-5-20250514": "gpt-5.5",
	"claude-sonnet-4-5":          "gpt-5.5",
	"claude-sonnet-4.5":          "gpt-5.5",
	"claude-sonnet-4-20250514":   "gpt-5.5",
	"claude-sonnet-4":            "gpt-5.5",
	"claude-opus-4-20250514":     "gpt-5.5",
	"claude-opus-4":              "gpt-5.5",
	"claude-3-5-sonnet-20241022": "gpt-5.5",
	"claude-3-5-haiku-20241022":  "gpt-5.6-luna",
}

// defaultAnthropicFallbackModel / defaultAnthropicHaikuFallbackModel 是模糊匹配与
// 兜底阶段使用的 Codex 模型。
const (
	defaultAnthropicFallbackModel      = "gpt-5.5"
	defaultAnthropicHaikuFallbackModel = "gpt-5.6-luna"
)

func canonicalizeCodexModel(model string, supportedModels []string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}
	for _, supported := range supportedModels {
		if strings.EqualFold(trimmed, supported) {
			return supported
		}
	}

	lower := strings.ToLower(trimmed)
	aliases := map[string]string{
		"gpt5-5":       "gpt-5.5",
		"gpt5.5":       "gpt-5.5",
		"gpt5-6-sol":   "gpt-5.6-sol",
		"gpt5.6-sol":   "gpt-5.6-sol",
		"gpt5-6-terra": "gpt-5.6-terra",
		"gpt5.6-terra": "gpt-5.6-terra",
		"gpt5-6-luna":  "gpt-5.6-luna",
		"gpt5.6-luna":  "gpt-5.6-luna",
		"gpt6-astra":   "gpt-6-astra",
	}
	if canonical, ok := aliases[lower]; ok {
		for _, supported := range supportedModels {
			if canonical == supported {
				return canonical
			}
		}
		return trimmed
	}
	return trimmed
}

// resolveAnthropicModel 将 Anthropic 模型名解析为 Codex 模型名
// 优先使用数据库中的动态映射，回退到默认映射
func resolveAnthropicModel(model string, dynamicMappingJSON string, supportedModels []string) string {
	model = strings.TrimSpace(model)

	// 1. 尝试动态映射（从系统设置，支持精确和 * 通配）
	if mapped, ok := resolveConfiguredModelMapping(model, dynamicMappingJSON, supportedModels); ok {
		return mapped
	}

	// 2. 尝试默认映射
	if mapped, ok := defaultAnthropicModelMap[model]; ok {
		return canonicalizeCodexModel(mapped, supportedModels)
	}

	// 3. 允许直接传入 Codex 模型名
	if canonical := canonicalizeCodexModel(model, supportedModels); canonical != model || canonical != "" {
		for _, supported := range supportedModels {
			if canonical == supported {
				return canonical
			}
		}
	}

	// 4. 模糊匹配
	lower := strings.ToLower(model)
	if strings.Contains(lower, "haiku") {
		return defaultAnthropicHaikuFallbackModel
	}
	if strings.Contains(lower, "claude") {
		return defaultAnthropicFallbackModel
	}

	// 5. 默认
	if len(supportedModels) > 0 {
		return supportedModels[0]
	}
	return defaultAnthropicFallbackModel
}

// ==================== Call ID 转换 ====================

// toCodexCallID 将 Anthropic tool_use id 转换为 Codex call_id
func toCodexCallID(anthropicID string) string {
	if id, _, custom := parseAnthropicCustomToolID(anthropicID); custom {
		return id
	}
	if strings.HasPrefix(anthropicID, "fc_") {
		return anthropicID
	}
	return "fc_" + anthropicID
}

// fromCodexCallID 将 Codex call_id 转回 Anthropic tool_use id
func fromCodexCallID(codexID string) string {
	if after, ok := strings.CutPrefix(codexID, "fc_"); ok {
		if strings.HasPrefix(after, "toolu_") || strings.HasPrefix(after, "call_") {
			return after
		}
	}
	return codexID
}

// ==================== 请求翻译: Anthropic Messages → Codex Responses ====================

// TranslateAnthropicToCodex 将 Anthropic Messages 请求转换为 Codex Responses 格式
// 返回: (codex 请求体, 原始 Anthropic model 名, error)
func TranslateAnthropicToCodex(rawJSON []byte, modelMappingJSON string) ([]byte, string, error) {
	return TranslateAnthropicToCodexWithModels(rawJSON, modelMappingJSON, SupportedModels)
}

func shouldUseCodexPriorityForAnthropicSpeed(speed string) bool {
	return strings.ToLower(strings.TrimSpace(speed)) == "fast"
}

// TranslateAnthropicToCodexWithModels 将 Anthropic Messages 请求转换为 Codex Responses 格式
// 返回: (codex 请求体, 原始 Anthropic model 名, error)
func TranslateAnthropicToCodexWithModels(rawJSON []byte, modelMappingJSON string, supportedModels []string) ([]byte, string, error) {
	return translateAnthropicToResponses(rawJSON, modelMappingJSON, supportedModels)
}

func translateAnthropicToResponses(rawJSON []byte, modelMappingJSON string, supportedModels []string) ([]byte, string, error) {
	var req anthropicRequest
	if err := json.Unmarshal(rawJSON, &req); err != nil {
		return nil, "", fmt.Errorf("parse anthropic request: %w", err)
	}

	originalModel := req.Model
	codexModel := resolveAnthropicModel(req.Model, modelMappingJSON, supportedModels)
	if err := validateAnthropicToolReferences(req.Messages); err != nil {
		return nil, originalModel, err
	}

	if err := validateAnthropicCustomToolHistory(req.Messages); err != nil {
		return nil, originalModel, err
	}
	input := buildCodexInput(req.System, req.Messages)

	// 构建输出 map（对齐 PrepareResponsesBody 的字段处理）
	out := map[string]any{
		"model":   codexModel,
		"stream":  true,
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
		"input":   input,
	}

	// 注意：不设置 max_output_tokens，上游 Codex 不支持该字段

	// reasoning: summary=detailed 请求完整思考摘要（auto 只回简短标题）。
	effort := resolveReasoningEffort(req.OutputConfig, codexModel)
	out["reasoning"] = map[string]any{
		"effort":  effort,
		"summary": "detailed",
	}

	if shouldUseCodexPriorityForAnthropicSpeed(req.Speed) {
		if upstreamTier, ok := upstreamServiceTier("priority"); ok {
			out["service_tier"] = upstreamTier
		}
	}

	// tools。不把 cache_control 传给上游。
	if len(req.Tools) > 0 {
		out["tools"] = convertAnthropicTools(req.Tools)
	}

	// tool_choice
	if len(req.ToolChoice) > 0 {
		if tc := convertAnthropicToolChoice(req.ToolChoice); tc != nil {
			out["tool_choice"] = tc
		}
	}

	restoreAnthropicCustomToolDeclarations(out)
	body, err := json.Marshal(out)
	if err != nil {
		return nil, "", fmt.Errorf("marshal codex request: %w", err)
	}
	return body, originalModel, nil
}

// validateAnthropicToolReferences rejects request semantics that cannot be
// represented safely in Responses. A detached tool_result must not be silently
// converted into ordinary user text because doing so changes tool causality.
func validateAnthropicToolReferences(messages []anthropicMessage) error {
	known := make(map[string]struct{})
	for messageIndex, message := range messages {
		blocks := parseAnthropicContent(message.Content)
		for blockIndex, block := range blocks {
			switch block.Type {
			case "tool_use":
				id := strings.TrimSpace(block.ID)
				if id == "" {
					return fmt.Errorf("messages[%d].content[%d] tool_use requires id", messageIndex, blockIndex)
				}
				known[id] = struct{}{}
			case "tool_result":
				id := strings.TrimSpace(block.ToolUseID)
				if id == "" {
					return fmt.Errorf("messages[%d].content[%d] tool_result requires tool_use_id", messageIndex, blockIndex)
				}
				if _, ok := known[id]; !ok {
					return fmt.Errorf("messages[%d].content[%d] orphan tool_result for tool_use_id %q", messageIndex, blockIndex, id)
				}
			}
		}
	}
	return nil
}

func appendTranslatedMessages(input []any, messages []anthropicMessage) []any {
	for _, msg := range messages {
		blocks := parseAnthropicContent(msg.Content)
		switch msg.Role {
		case "user":
			input = appendUserBlocks(input, blocks)
		case "assistant":
			input = appendAssistantBlocks(input, blocks)
		}
	}
	return input
}

// buildCodexInput 将 Anthropic system + messages 转换为 Codex input 数组
func buildCodexInput(system json.RawMessage, messages []anthropicMessage) []any {
	var input []any
	if sysText := parseAnthropicSystem(system); sysText != "" {
		input = append(input, map[string]any{
			"type": "message",
			"role": "developer",
			"content": []any{
				map[string]any{"type": "input_text", "text": sysText},
			},
		})
	}
	return appendTranslatedMessages(input, messages)
}

// parseAnthropicSystem 解析 system 字段（string 或 []block）
func parseAnthropicSystem(raw json.RawMessage) string {
	var parts []string
	for _, block := range parseAnthropicSystemBlocks(raw) {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func parseAnthropicSystemBlocks(raw json.RawMessage) []anthropicContentBlock {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []anthropicContentBlock{{Type: "text", Text: s}}
	}
	var blocks []anthropicContentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		return blocks
	}
	return nil
}

// parseAnthropicContent 解析 message content（string 或 []block）
func parseAnthropicContent(raw json.RawMessage) []anthropicContentBlock {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	// 尝试纯字符串
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []anthropicContentBlock{{Type: "text", Text: s}}
	}
	// 尝试块数组
	var blocks []anthropicContentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		return blocks
	}
	return nil
}

// appendUserBlocks 将 user 消息的内容块转换并追加到 input
func appendUserBlocks(input []any, blocks []anthropicContentBlock) []any {
	var contentParts []any
	for _, b := range blocks {
		switch b.Type {
		case "text":
			contentParts = append(contentParts, map[string]any{"type": "input_text", "text": b.Text})
		case "image":
			if b.Source != nil {
				dataURI := fmt.Sprintf("data:%s;base64,%s", b.Source.MediaType, b.Source.Data)
				contentParts = append(contentParts, map[string]any{"type": "input_image", "image_url": dataURI})
			}
		case "tool_result":
			// tool_result → function_call_output（独立 item）
			output := extractToolResultText(b)
			images := extractToolResultImages(b)
			if output == "" && len(images) > 0 {
				// 纯图片工具结果：function_call_output 不能为空，留占位指向随后的 user 消息。
				output = toolResultImageMovedMarker
			}
			callID := toCodexCallID(b.ToolUseID)
			outputType := "function_call_output"
			if _, _, custom := parseAnthropicCustomToolID(b.ToolUseID); custom {
				outputType = "custom_tool_call_output"
			}
			input = append(input, map[string]any{
				"type":    outputType,
				"call_id": callID,
				"output":  output,
			})
			// Chat/Responses 协议下 function_call_output 只能是文本，图片视觉模型看不见；
			// 抽出来放进紧随其后的合成 user 消息，附带 call 归属说明。
			if len(images) > 0 {
				mediaParts := []any{map[string]any{
					"type": "input_text",
					"text": fmt.Sprintf(toolResultImageAttribution, callID),
				}}
				for _, dataURI := range images {
					mediaParts = append(mediaParts, map[string]any{"type": "input_image", "image_url": dataURI})
				}
				input = append(input, map[string]any{
					"type":    "message",
					"role":    "user",
					"content": mediaParts,
				})
			}
		}
	}
	if len(contentParts) > 0 {
		input = append(input, map[string]any{
			"type":    "message",
			"role":    "user",
			"content": contentParts,
		})
	}
	return input
}

// appendAssistantBlocks 将 assistant 消息的内容块转换并追加到 input
func appendAssistantBlocks(input []any, blocks []anthropicContentBlock) []any {
	var textParts []any
	for _, b := range blocks {
		switch b.Type {
		case "text":
			textParts = append(textParts, map[string]any{"type": "output_text", "text": b.Text})
		case "tool_use":
			// 先把已有文本作为 message 输出
			if len(textParts) > 0 {
				input = append(input, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": textParts,
				})
				textParts = nil
			}
			if id, namespace, custom := parseAnthropicCustomToolID(b.ID); custom {
				item := map[string]any{"type": "custom_tool_call", "call_id": id, "name": b.Name, "input": gjson.GetBytes(b.Input, "input").String()}
				if namespace != "" {
					item["namespace"] = namespace
				}
				input = append(input, item)
				continue
			}
			args := "{}"
			if len(b.Input) > 0 {
				if cleaned := sanitizeToolInputJSON(b.Name, string(b.Input)); cleaned != "" {
					args = cleaned
				}
			}
			input = append(input, map[string]any{
				"type":      "function_call",
				"call_id":   toCodexCallID(b.ID),
				"name":      b.Name,
				"arguments": args,
			})
		case "thinking":
			// 带 signature 的 thinking 块重建为 reasoning item 回传：signature 即
			// 输出侧下发的 reasoning encrypted_content，同账号上游可解，保留完整
			// 推理上下文。无 signature 的块仍跳过——凭空构造的 reasoning item
			// 无密文可解，上游可能拒绝。
			if b.Signature == "" {
				continue
			}
			if len(textParts) > 0 {
				input = append(input, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": textParts,
				})
				textParts = nil
			}
			summary := []any{}
			if b.Thinking != "" {
				summary = append(summary, map[string]any{"type": "summary_text", "text": b.Thinking})
			}
			input = append(input, map[string]any{
				"type":              "reasoning",
				"summary":           summary,
				"encrypted_content": b.Signature,
			})
		}
	}
	if len(textParts) > 0 {
		input = append(input, map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": textParts,
		})
	}
	return input
}

const (
	toolResultImageMovedMarker = "[Tool output image moved to the following user message]"
	toolResultImageAttribution = "[Tool output image for call %s]"
)

// extractToolResultImages 从 tool_result 块的 content 中提取图片，返回 data URI 列表。
// 只识别 content 为 []block 时的 image 块（Anthropic base64 source）；字符串或非法
// content 不含结构化图片，返回 nil。图片能力缺失时该函数是无副作用的 no-op。
func extractToolResultImages(b anthropicContentBlock) []string {
	if len(b.Content) == 0 || string(b.Content) == "null" {
		return nil
	}
	var blocks []anthropicContentBlock
	if json.Unmarshal(b.Content, &blocks) != nil {
		return nil
	}
	var images []string
	for _, cb := range blocks {
		if cb.Type == "image" && cb.Source != nil && cb.Source.Data != "" {
			images = append(images, fmt.Sprintf("data:%s;base64,%s", cb.Source.MediaType, cb.Source.Data))
		}
	}
	return images
}

// extractToolResultText 从 tool_result 块提取文本输出
func extractToolResultText(b anthropicContentBlock) string {
	// content 可能是 string 或 []block
	if len(b.Content) == 0 || string(b.Content) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(b.Content, &s) == nil {
		return s
	}
	var blocks []anthropicContentBlock
	if json.Unmarshal(b.Content, &blocks) == nil {
		var parts []string
		for _, cb := range blocks {
			if cb.Type == "text" && cb.Text != "" {
				parts = append(parts, cb.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(b.Content)
}

// resolveReasoningEffort maps Claude output_config.effort to Responses reasoning.effort.
// Claude thinking.type/budget_tokens only indicates that thinking mode exists; it
// does not control effort on this OpenAI/Codex compatibility path.
func resolveReasoningEffort(outputConfig *anthropicOutputConfig, model string) string {
	if outputConfig != nil && strings.TrimSpace(outputConfig.Effort) != "" {
		return normalizeReasoningEffortForModel(outputConfig.Effort, model)
	}
	return "high"
}

// convertAnthropicTools 将 Anthropic 工具格式转为 Codex 格式
func convertAnthropicTools(tools []anthropicTool) []any {
	result := make([]any, 0, len(tools))
	for _, t := range tools {
		item := map[string]any{
			"type": "function",
			"name": t.Name,
			// Anthropic 工具没有 strict 概念，input_schema 常带可选属性；Responses
			// 默认 strict=true 会按严格模式校验 schema 而 400，显式关掉。
			"strict": false,
		}
		if t.Description != "" {
			item["description"] = t.Description
		}
		if len(t.InputSchema) > 0 {
			var params map[string]any
			if json.Unmarshal(t.InputSchema, &params) == nil {
				sanitizeSchemaForUpstream(params)
				item["parameters"] = params
			}
		}
		result = append(result, item)
	}
	if len(result) > maxTools {
		result = result[:maxTools]
	}
	return result
}

// convertAnthropicToolChoice 将 Anthropic tool_choice 转为 Codex 格式
func convertAnthropicToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	// 尝试对象格式
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	}
	if json.Unmarshal(raw, &tc) != nil {
		return nil
	}
	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		if tc.Name != "" {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": tc.Name,
				},
			}
		}
		return "auto"
	default:
		return nil
	}
}

// ==================== 响应翻译: Codex SSE → Anthropic ====================

// anthropicStreamTranslator 有状态的流式响应翻译器（Codex → Anthropic）
type anthropicStreamTranslator struct {
	model                     string
	responseID                string
	messageStartSent          bool
	contentBlockIndex         int
	contentBlockOpen          bool
	currentBlockType          string // "text" | "thinking" | "tool_use"
	currentToolUseID          string
	currentToolUseName        string
	currentToolInputBuffer    strings.Builder
	currentToolCustom         bool
	currentToolInputFinalized bool
	toolInputError            error
	hasToolUse                bool
	inputTokens               int
	outputTokens              int
	cachedTokens              int
	thinkingTokens            *anthropicOutputTokensDetails
	pingAfterStartSent        bool
	deltasSincePing           int
	// thinkingAsText 为 true 时把 thinking 块渲染为 dim 文本块（仅 Claude Code UA）。
	thinkingAsText       bool
	thinkingTextPending  strings.Builder // 尚未凑成完整行的 reasoning delta
	thinkingTextFirst    bool            // 渲染块是否尚未发出任何 text_delta（决定 header 行）
	thinkingTextOpen     bool            // 渲染 text 块是否已发过 content_block_start
	thinkingTextHasLines bool            // 是否已产出过至少一行（决定 stop 时是否补 duration）
	thinkingTextStartAt  time.Time       // 时长锚点：请求到达时刻
}

// newAnthropicStreamTranslator 创建流式翻译器
func newAnthropicStreamTranslator(model string) *anthropicStreamTranslator {
	return &anthropicStreamTranslator{
		model:      model,
		responseID: "msg_" + uuid.New().String()[:24],
	}
}

// newAnthropicStreamTranslatorWithThinkingText 创建渲染模式的流式翻译器：
// thinking 块输出为 dim 文本（💭 Thinking 头行 + 逐行 dim + Thought for Xs），
// 供 Claude Code 终端展示。时长锚点取请求到达时刻，网关侧缓冲也算入真实
// 思考耗时（对齐 cc-proxy 的 startedAtMs 约定）。
func newAnthropicStreamTranslatorWithThinkingText(model string, startedAt time.Time) *anthropicStreamTranslator {
	t := newAnthropicStreamTranslator(model)
	t.thinkingAsText = true
	t.thinkingTextFirst = true
	t.thinkingTextStartAt = startedAt
	return t
}

// beginThinkingTextBlock 懒开渲染 text 块（首个完整行到达时才调用）。
func (t *anthropicStreamTranslator) beginThinkingTextBlock() []anthropicStreamEvent {
	events := t.closeCurrentBlock()
	t.currentBlockType = "text"
	events = append(events, t.startContentBlock(anthropicContentBlock{
		Type: "text",
		Text: "",
	})...)
	t.thinkingTextOpen = true
	return events
}

// emitThinkingTextLine 把一行渲染进渲染块：懒开块、首行补 header、newline
// 引导本行（块尾不带 '\n'）、逐行独立 dim 包裹。
func (t *anthropicStreamTranslator) emitThinkingTextLine(line string) []anthropicStreamEvent {
	var events []anthropicStreamEvent
	if !t.thinkingTextOpen {
		events = append(events, t.beginThinkingTextBlock()...)
	}
	if t.thinkingTextFirst {
		t.thinkingTextFirst = false
		events = append(events, t.contentBlockDelta(anthropicDelta{
			Type: "text_delta",
			Text: thinkingTextHeaderLine,
		})...)
	}
	t.thinkingTextHasLines = true
	events = append(events, t.contentBlockDelta(anthropicDelta{
		Type: "text_delta",
		Text: "\n" + ansiDim + stripReasoningSummaryBold(line) + ansiReset,
	})...)
	return events
}

// flushThinkingTextLine 把 pending 残余落成一行（段边界与块收尾共用）。
func (t *anthropicStreamTranslator) flushThinkingTextLine() []anthropicStreamEvent {
	line := strings.TrimSpace(t.thinkingTextPending.String())
	t.thinkingTextPending.Reset()
	if line == "" {
		return nil
	}
	return t.emitThinkingTextLine(line)
}

// translateEvent 将单个 Codex SSE 事件翻译为零或多个 Anthropic SSE 事件
func (t *anthropicStreamTranslator) translateEvent(eventData []byte) []anthropicStreamEvent {
	if t.toolInputError != nil {
		return nil
	}
	eventType := gjson.GetBytes(eventData, "type").String()

	switch eventType {
	case "response.created":
		return t.handleCreated()

	case "response.output_item.added":
		return t.handleOutputItemAdded(eventData)

	case "response.output_text.delta":
		return t.handleTextDelta(eventData)

	case "response.reasoning_summary_part.added":
		// 段边界先落行再开新段。summary delta 整段下发且不含 '\n'（实测
		// 每 part 恰好一个 delta），文本会一直滞留 pending 从未 flush；
		// 若只在块已打开时补换行，门控与"打开块需先 flush"互为前提，
		// 多段标题会被拼进同一行。
		if !t.thinkingAsText {
			return nil
		}
		return t.flushThinkingTextLine()

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return t.handleThinkingDelta(eventData)

	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return t.handleToolInputDelta(eventData)
	case "response.custom_tool_call_input.done":
		if t.currentToolCustom && t.contentBlockOpen {
			return t.finishCustomToolInput(gjson.GetBytes(eventData, "input"))
		}
		return nil

	case "response.output_text.done", "response.reasoning_summary_text.done",
		"response.reasoning_text.done":
		return t.handleContentDone()

	case "response.output_item.done":
		return t.handleOutputItemDone(eventData)

	// response.incomplete 是 max_output_tokens 截断的正常终态，走同一条收尾：
	// handleCompleted 已按 response.status/incomplete_details 推导 stop_reason。
	case "response.completed", "response.incomplete":
		return t.handleCompleted(eventData)

	case "response.failed":
		return t.handleFailed()

	default:
		return nil
	}
}

// handleCreated 处理 response.created → message_start
func (t *anthropicStreamTranslator) handleCreated() []anthropicStreamEvent {
	if t.messageStartSent {
		return nil
	}
	t.messageStartSent = true
	return []anthropicStreamEvent{{
		Type: "message_start",
		Message: &anthropicResponse{
			ID:      t.responseID,
			Type:    "message",
			Role:    "assistant",
			Content: []anthropicContentBlock{},
			Model:   t.model,
			Usage:   anthropicUsage{},
		},
	}}
}

func (t *anthropicStreamTranslator) startContentBlock(block anthropicContentBlock) []anthropicStreamEvent {
	idx := t.contentBlockIndex
	t.contentBlockIndex++
	t.contentBlockOpen = true
	events := []anthropicStreamEvent{{
		Type:         "content_block_start",
		Index:        &idx,
		ContentBlock: &block,
	}}
	if !t.pingAfterStartSent {
		t.pingAfterStartSent = true
		events = append(events, anthropicStreamEvent{Type: "ping"})
	}
	return events
}

func (t *anthropicStreamTranslator) contentBlockDelta(delta anthropicDelta) []anthropicStreamEvent {
	idx := t.contentBlockIndex - 1
	events := []anthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &idx,
		Delta: &delta,
	}}
	t.deltasSincePing++
	if t.deltasSincePing >= anthropicStreamPingEveryNDeltas {
		t.deltasSincePing = 0
		events = append(events, anthropicStreamEvent{Type: "ping"})
	}
	return events
}

// handleOutputItemAdded 处理新的输出项（reasoning/message/function_call）
func (t *anthropicStreamTranslator) handleOutputItemAdded(data []byte) []anthropicStreamEvent {
	var events []anthropicStreamEvent

	// 确保 message_start 已发送
	if !t.messageStartSent {
		events = append(events, t.handleCreated()...)
	}

	itemType := gjson.GetBytes(data, "item.type").String()

	switch itemType {
	case "reasoning":
		// 渲染模式下连续 reasoning item 合并共享同一个渲染 text 块：
		// 不关块、不重置 header/duration 状态，等收尾时机统一处理。
		if t.thinkingAsText && t.thinkingTextOpen {
			return events
		}
		// 关闭当前块。渲染模式下不开 thinking 块，等首个 delta 懒开 text 块。
		events = append(events, t.closeCurrentBlock()...)
		if t.thinkingAsText {
			// 异常序列兜底：前一个 item 的 done 未到达时 pending 可能残留
			// 未 flush 文本，先落行再重置，避免静默丢弃。
			events = append(events, t.flushThinkingTextLine()...)
			t.currentBlockType = "text"
			t.thinkingTextPending.Reset()
			t.thinkingTextOpen = false
			return events
		}
		t.currentBlockType = "thinking"
		events = append(events, t.startContentBlock(anthropicContentBlock{
			Type:     "thinking",
			Thinking: "",
		})...)

	case "function_call", "custom_tool_call":
		events = append(events, t.closeCurrentBlock()...)
		callID := fromCodexCallID(gjson.GetBytes(data, "item.call_id").String())
		if callID == "" {
			callID = fromCodexCallID(gjson.GetBytes(data, "item.id").String())
		}
		name := gjson.GetBytes(data, "item.name").String()
		t.currentBlockType = "tool_use"
		t.currentToolUseID = callID
		t.currentToolUseName = name
		t.currentToolCustom = itemType == "custom_tool_call"
		t.currentToolInputFinalized = false
		if t.currentToolCustom {
			id := gjson.GetBytes(data, "item.call_id").String()
			if id == "" {
				id = gjson.GetBytes(data, "item.id").String()
			}
			callID = anthropicCustomToolID(id, gjson.GetBytes(data, "item.namespace").String())
			t.currentToolUseID = callID
		}
		t.hasToolUse = true
		events = append(events, t.startContentBlock(anthropicContentBlock{
			Type:  "tool_use",
			ID:    callID,
			Name:  name,
			Input: json.RawMessage("{}"),
		})...)
		if t.currentToolCustom {
			events = append(events, t.contentBlockDelta(anthropicDelta{Type: "input_json_delta", PartialJSON: `{"input":"`})...)
		}

	case "message":
		// text block 延迟到第一个 delta 时打开；渲染模式下若 thinking 渲染块
		// 已开（reasoning 先行），必须先收尾（补 duration 行）再关闭，否则
		// 回答文本会因懒开条件不触发而追加进 thinking 渲染块之后。
		if t.thinkingAsText && t.thinkingTextOpen {
			events = append(events, t.finishThinkingTextBlock()...)
			events = append(events, t.closeCurrentBlock()...)
		}
	}

	return events
}

// handleTextDelta 处理文本增量
func (t *anthropicStreamTranslator) handleTextDelta(data []byte) []anthropicStreamEvent {
	delta := gjson.GetBytes(data, "delta").String()
	if delta == "" {
		return nil
	}

	var events []anthropicStreamEvent

	// 确保 message_start 已发送
	if !t.messageStartSent {
		events = append(events, t.handleCreated()...)
	}

	// 懒开 text block
	if !t.contentBlockOpen || t.currentBlockType != "text" {
		events = append(events, t.closeCurrentBlock()...)
		t.currentBlockType = "text"
		events = append(events, t.startContentBlock(anthropicContentBlock{
			Type: "text",
			Text: "",
		})...)
	}

	events = append(events, t.contentBlockDelta(anthropicDelta{
		Type: "text_delta",
		Text: delta,
	})...)
	return events
}

// handleThinkingDelta 处理推理增量
func (t *anthropicStreamTranslator) handleThinkingDelta(data []byte) []anthropicStreamEvent {
	delta := gjson.GetBytes(data, "delta").String()
	if delta == "" {
		return nil
	}

	var events []anthropicStreamEvent
	if !t.messageStartSent {
		events = append(events, t.handleCreated()...)
	}

	// 渲染模式：攒行后逐行 flush 为 text_delta，约定 newline 引导下一行
	//（块尾不带 '\n'）、空行滤除，逐字节对齐 cc-proxy transformThinkingAsTextEvents。
	if t.thinkingAsText {
		t.thinkingTextPending.WriteString(delta)
		pending := t.thinkingTextPending.String()
		for {
			newline := strings.IndexByte(pending, '\n')
			if newline < 0 {
				break
			}
			line := strings.TrimSuffix(pending[:newline], "\r")
			pending = pending[newline+1:]
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			events = append(events, t.emitThinkingTextLine(line)...)
		}
		t.thinkingTextPending.Reset()
		t.thinkingTextPending.WriteString(pending)
		return events
	}

	// 确保 thinking block 已打开
	if !t.contentBlockOpen || t.currentBlockType != "thinking" {
		events = append(events, t.closeCurrentBlock()...)
		t.currentBlockType = "thinking"
		events = append(events, t.startContentBlock(anthropicContentBlock{
			Type:     "thinking",
			Thinking: "",
		})...)
	}

	events = append(events, t.contentBlockDelta(anthropicDelta{
		Type:     "thinking_delta",
		Thinking: delta,
	})...)
	return events
}

// handleToolInputDelta 按官方 Anthropic 形状把 arguments 碎片转成
// input_json_delta。空可选字段的清洗留到非流式聚合（compactAnthropicContent /
// buildAnthropicResponseFromCompleted），避免把整段 JSON 攒完再一次吐给 Claude Code。
func (t *anthropicStreamTranslator) handleToolInputDelta(data []byte) []anthropicStreamEvent {
	delta := gjson.GetBytes(data, "delta").String()
	if delta == "" {
		return nil
	}
	if t.currentToolCustom {
		return t.customToolInputDelta(delta)
	}
	t.currentToolInputBuffer.WriteString(delta)
	return t.contentBlockDelta(anthropicDelta{
		Type:        "input_json_delta",
		PartialJSON: delta,
	})
}

// handleContentDone 处理内容完成（文本/推理块）
func (t *anthropicStreamTranslator) handleContentDone() []anthropicStreamEvent {
	// thinking 块不在 *_text.done 关闭：reasoning item 的 encrypted_content 要到
	// output_item.done 才出现，提前关块就发不出 signature_delta。多段 summary part
	// 也因此合并进同一个 thinking 块。
	if t.contentBlockOpen && t.currentBlockType == "thinking" {
		return nil
	}
	// 渲染模式下 reasoning 的 *_text.done 同样不关块：渲染 text 块要等
	// output_item.done 收尾（补 duration 行），提前关块会截断后续 delta。
	if t.thinkingAsText && t.thinkingTextOpen {
		return nil
	}
	return t.closeCurrentBlock()
}

// handleOutputItemDone 处理输出项完成。
// reasoning item 携带 encrypted_content 时先发 signature_delta 再关块：
// signature 即密文本身，输入侧据此重建 reasoning item 回传上游。
func (t *anthropicStreamTranslator) handleOutputItemDone(data []byte) []anthropicStreamEvent {
	if t.contentBlockOpen && t.currentToolCustom && t.currentBlockType == "tool_use" && gjson.GetBytes(data, "item.type").String() == "custom_tool_call" {
		events := t.finishCustomToolInput(gjson.GetBytes(data, "item.input"))
		if t.toolInputError != nil {
			return nil
		}
		return append(events, t.closeCurrentBlock()...)
	}
	if t.contentBlockOpen && t.currentBlockType == "thinking" &&
		gjson.GetBytes(data, "item.type").String() == "reasoning" {
		if sig := gjson.GetBytes(data, "item.encrypted_content").String(); sig != "" {
			events := t.contentBlockDelta(anthropicDelta{
				Type:      "signature_delta",
				Signature: sig,
			})
			return append(events, t.closeCurrentBlock()...)
		}
	}
	// 渲染模式收尾（连续 reasoning item 合并共享一个渲染块）：把该 item 的
	// 残余 pending 落成行，但不关块也不补 duration 行——后续可能还有下一个
	// reasoning item 继续追加进同一块；真正的收尾（duration + 关块）延迟到
	// message item 或响应终止（handleCompleted/handleFailed）时。
	// 与上方 signature_delta 分支互斥：该分支要求 currentBlockType ==
	// "thinking"，而渲染模式从不打开 thinking 块（currentBlockType 恒为
	// "text"），因此本分支仅会在非渲染 thinking 块已关闭后命中。
	if t.thinkingAsText &&
		(t.thinkingTextOpen || strings.TrimSpace(t.thinkingTextPending.String()) != "") &&
		gjson.GetBytes(data, "item.type").String() == "reasoning" {
		return t.flushThinkingTextLine()
	}
	return t.closeCurrentBlock()
}

// finishThinkingTextBlock 在渲染块的收尾补齐残余行 + duration 行。
// newline 仍引导（`\n` + duration 行），块尾不带 '\n'；块从未产出任何行时
// 不补任何内容（避免发出空 text 块的孤儿 stop 事件）。
func (t *anthropicStreamTranslator) finishThinkingTextBlock() []anthropicStreamEvent {
	if !t.thinkingAsText {
		return nil
	}
	// 残余行可能尚未开块（单 part 无换行的常见形态），flush 内部会懒开，
	// 不再要求调用方先 beginThinkingTextBlock。
	events := t.flushThinkingTextLine()
	if !t.thinkingTextOpen {
		return events
	}
	if t.thinkingTextHasLines {
		elapsed := time.Since(t.thinkingTextStartAt)
		events = append(events, t.contentBlockDelta(anthropicDelta{
			Type: "text_delta",
			Text: "\n" + formatThinkingDurationLine(elapsed),
		})...)
	}
	t.thinkingTextOpen = false
	return events
}

// handleCompleted 处理 response.completed → message_delta + message_stop
func (t *anthropicStreamTranslator) handleCompleted(data []byte) []anthropicStreamEvent {
	var events []anthropicStreamEvent

	if !t.messageStartSent {
		events = append(events, t.handleCreated()...)
	}

	// 渲染模式下 message item 未出现（例如纯 reasoning 截断）时，渲染块
	// 仍处于打开状态，终止前必须收尾（补 duration 行）再关块。
	if t.thinkingAsText && t.thinkingTextOpen {
		events = append(events, t.finishThinkingTextBlock()...)
	}
	events = append(events, t.closeCurrentBlock()...)

	// 提取 usage
	usage := gjson.GetBytes(data, "response.usage")
	if usage.Exists() {
		// OpenAI Responses 的 input_tokens 含缓存命中部分，而 Anthropic Messages
		// 的 input_tokens 不含缓存（缓存另计在 cache_read_input_tokens）。
		// 直接透传会把缓存 token 重复计入，导致费用偏高，因此这里扣除缓存部分。
		t.cachedTokens = int(usage.Get("input_tokens_details.cached_tokens").Int())
		t.inputTokens = max(int(usage.Get("input_tokens").Int())-t.cachedTokens, 0)
		t.outputTokens = int(usage.Get("output_tokens").Int())
		t.thinkingTokens = anthropicThinkingTokensFromUsage(usage)
	}

	// 确定 stop_reason
	stopReason := "end_turn"
	status := gjson.GetBytes(data, "response.status").String()
	if status == "incomplete" {
		reason := gjson.GetBytes(data, "response.incomplete_details.reason").String()
		if reason == "max_output_tokens" {
			stopReason = "max_tokens"
		}
	}
	if t.hasToolUse && stopReason == "end_turn" {
		stopReason = "tool_use"
	}

	events = append(events, anthropicStreamEvent{
		Type: "message_delta",
		Delta: &anthropicDelta{
			StopReason:   anthropicStopReason(stopReason),
			StopSequence: anthropicJSONNull,
			StopDetails:  anthropicJSONNull,
		},
		Usage: &anthropicUsage{
			InputTokens:          t.inputTokens,
			OutputTokens:         t.outputTokens,
			CacheReadInputTokens: t.cachedTokens,
			OutputTokensDetails:  t.thinkingTokens,
		},
	})

	events = append(events, anthropicStreamEvent{Type: "message_stop"})
	return events
}

// handleFailed 处理 response.failed
func (t *anthropicStreamTranslator) handleFailed() []anthropicStreamEvent {
	var events []anthropicStreamEvent
	if !t.messageStartSent {
		events = append(events, t.handleCreated()...)
	}
	if t.thinkingAsText && t.thinkingTextOpen {
		events = append(events, t.finishThinkingTextBlock()...)
	}
	events = append(events, t.closeCurrentBlock()...)
	events = append(events, anthropicStreamEvent{
		Type: "message_delta",
		Delta: &anthropicDelta{
			StopReason:   anthropicStopReason("end_turn"),
			StopSequence: anthropicJSONNull,
			StopDetails:  anthropicJSONNull,
		},
		Usage: &anthropicUsage{},
	})
	events = append(events, anthropicStreamEvent{Type: "message_stop"})
	return events
}

// closeCurrentBlock 关闭当前打开的 content block。
// tool_use 的 arguments 已在 handleToolInputDelta 按碎片下发，这里只关块。
func (t *anthropicStreamTranslator) closeCurrentBlock() []anthropicStreamEvent {
	if !t.contentBlockOpen {
		return nil
	}
	var events []anthropicStreamEvent
	if t.currentBlockType == "tool_use" && t.currentToolCustom && !t.currentToolInputFinalized {
		events = append(events, t.finishCustomToolInput(gjson.Result{})...)
	}
	t.contentBlockOpen = false
	t.currentToolCustom = false
	idx := t.contentBlockIndex - 1
	t.currentToolInputBuffer.Reset()

	return append(events, anthropicStreamEvent{Type: "content_block_stop", Index: &idx})
}

// anthropicEventToSSE 将 Anthropic 事件序列化为 SSE 格式
func anthropicEventToSSE(evt anthropicStreamEvent) string {
	data, _ := json.Marshal(evt)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", evt.Type, data)
}

// ==================== 非流式响应构建 ====================

type anthropicResponseAccumulator struct {
	response *anthropicResponse
	content  []anthropicContentBlock
}

func newAnthropicResponseAccumulator(model string) *anthropicResponseAccumulator {
	return &anthropicResponseAccumulator{
		response: &anthropicResponse{
			ID:      "msg_" + uuid.New().String()[:24],
			Type:    "message",
			Role:    "assistant",
			Content: []anthropicContentBlock{},
			Model:   model,
		},
	}
}

func (a *anthropicResponseAccumulator) apply(events []anthropicStreamEvent) {
	for _, evt := range events {
		switch evt.Type {
		case "message_start":
			if evt.Message != nil {
				msg := *evt.Message
				msg.Content = []anthropicContentBlock{}
				a.response = &msg
				a.content = a.content[:0]
			}
		case "content_block_start":
			if evt.Index == nil || evt.ContentBlock == nil {
				continue
			}
			a.ensureContentIndex(*evt.Index)
			a.content[*evt.Index] = *evt.ContentBlock
		case "content_block_delta":
			if evt.Index == nil || evt.Delta == nil {
				continue
			}
			a.ensureContentIndex(*evt.Index)
			switch evt.Delta.Type {
			case "text_delta":
				a.content[*evt.Index].Text += evt.Delta.Text
			case "thinking_delta":
				a.content[*evt.Index].Thinking += evt.Delta.Thinking
			case "signature_delta":
				a.content[*evt.Index].Signature += evt.Delta.Signature
			case "input_json_delta":
				prev := a.content[*evt.Index].Input
				fragment := []byte(evt.Delta.PartialJSON)
				if len(prev) == 0 || bytes.Equal(bytes.TrimSpace(prev), []byte("{}")) {
					a.content[*evt.Index].Input = json.RawMessage(fragment)
				} else {
					merged := make([]byte, 0, len(prev)+len(fragment))
					merged = append(merged, prev...)
					merged = append(merged, fragment...)
					a.content[*evt.Index].Input = merged
				}
			}
		case "message_delta":
			if evt.Delta != nil && evt.Delta.StopReason != nil {
				a.response.StopReason = evt.Delta.StopReason
			}
			if evt.Usage != nil {
				a.response.Usage = *evt.Usage
			}
		}
	}
}

func (a *anthropicResponseAccumulator) ensureContentIndex(idx int) {
	for len(a.content) <= idx {
		a.content = append(a.content, anthropicContentBlock{})
	}
}

func (a *anthropicResponseAccumulator) build(completedData []byte) *anthropicResponse {
	fallback := buildAnthropicResponseFromCompleted(completedData, a.response.Model)
	resp := *a.response
	resp.Content = compactAnthropicContent(a.content)

	if len(resp.Content) == 0 && len(fallback.Content) > 0 {
		return fallback
	}
	if anthropicStopReasonValue(resp.StopReason) == "" {
		resp.StopReason = fallback.StopReason
	}
	if anthropicStopReasonValue(resp.StopReason) == "" {
		resp.StopReason = anthropicStopReason("end_turn")
	}
	if resp.Usage == (anthropicUsage{}) {
		resp.Usage = fallback.Usage
	}
	return &resp
}

func compactAnthropicContent(content []anthropicContentBlock) []anthropicContentBlock {
	out := make([]anthropicContentBlock, 0, len(content))
	for _, block := range content {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
		case "thinking":
			// 只有 signature 没有摘要文本的块也保留：密文是回传推理上下文的载体。
			if block.Thinking == "" && block.Signature == "" {
				continue
			}
		case "tool_use":
			if cleaned := sanitizeToolInputJSON(block.Name, string(block.Input)); cleaned != "" {
				block.Input = json.RawMessage(cleaned)
			} else if block.Input == nil || !json.Valid(block.Input) {
				block.Input = json.RawMessage("{}")
			}
		default:
			continue
		}
		out = append(out, block)
	}
	return out
}

// buildAnthropicResponseFromCompleted 从 response.completed 事件构建完整的 Anthropic 响应
func buildAnthropicResponseFromCompleted(completedData []byte, model string) *anthropicResponse {
	responseID := "msg_" + uuid.New().String()[:24]

	resp := &anthropicResponse{
		ID:      responseID,
		Type:    "message",
		Role:    "assistant",
		Content: []anthropicContentBlock{},
		Model:   model,
	}

	// 提取 output 数组
	outputs := gjson.GetBytes(completedData, "response.output")
	if !outputs.Exists() || !outputs.IsArray() {
		return resp
	}

	var content []anthropicContentBlock
	lastBlockIsToolUse := false

	outputs.ForEach(func(_, item gjson.Result) bool {
		itemType := item.Get("type").String()
		switch itemType {
		case "reasoning":
			// reasoning → thinking block（encrypted_content 作为 signature 下发，
			// 供客户端多轮回传重建推理上下文）
			summaryText := ""
			item.Get("summary").ForEach(func(_, s gjson.Result) bool {
				if s.Get("type").String() == "summary_text" {
					summaryText += s.Get("text").String()
				}
				return true
			})
			signature := item.Get("encrypted_content").String()
			if summaryText != "" || signature != "" {
				content = append(content, anthropicContentBlock{
					Type:      "thinking",
					Thinking:  summaryText,
					Signature: signature,
				})
				lastBlockIsToolUse = false
			}

		case "message":
			// message → text block(s)
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "output_text" {
					text := part.Get("text").String()
					if text != "" {
						content = append(content, anthropicContentBlock{
							Type: "text",
							Text: text,
						})
						lastBlockIsToolUse = false
					}
				}
				return true
			})

		case "function_call", "custom_tool_call":
			// function_call/custom_tool_call → tool_use block
			callID := fromCodexCallID(item.Get("call_id").String())
			if callID == "" {
				callID = fromCodexCallID(item.Get("id").String())
			}
			name := item.Get("name").String()
			args := item.Get("arguments").String()
			if itemType == "custom_tool_call" {
				args = string(wrappedCustomToolInput(item.Get("input").String()))
				id := item.Get("call_id").String()
				if id == "" {
					id = item.Get("id").String()
				}
				callID = anthropicCustomToolID(id, item.Get("namespace").String())
			}
			if cleaned := sanitizeToolInputJSON(name, args); cleaned != "" {
				args = cleaned
			} else {
				args = "{}"
			}
			content = append(content, anthropicContentBlock{
				Type:  "tool_use",
				ID:    callID,
				Name:  name,
				Input: json.RawMessage(args),
			})
			lastBlockIsToolUse = true
		}
		return true
	})

	resp.Content = content

	// 确定 stop_reason
	status := gjson.GetBytes(completedData, "response.status").String()
	switch status {
	case "incomplete":
		reason := gjson.GetBytes(completedData, "response.incomplete_details.reason").String()
		if reason == "max_output_tokens" {
			resp.StopReason = anthropicStopReason("max_tokens")
		} else {
			resp.StopReason = anthropicStopReason("end_turn")
		}
	default:
		if lastBlockIsToolUse {
			resp.StopReason = anthropicStopReason("tool_use")
		} else {
			resp.StopReason = anthropicStopReason("end_turn")
		}
	}

	// usage
	usage := gjson.GetBytes(completedData, "response.usage")
	if usage.Exists() {
		// 见流式分支说明：扣除缓存命中部分，避免缓存 token 被重复计入。
		cached := int(usage.Get("input_tokens_details.cached_tokens").Int())
		input := max(int(usage.Get("input_tokens").Int())-cached, 0)
		resp.Usage = anthropicUsage{
			InputTokens:          input,
			OutputTokens:         int(usage.Get("output_tokens").Int()),
			CacheReadInputTokens: cached,
			OutputTokensDetails:  anthropicThinkingTokensFromUsage(usage),
		}
	}

	return resp
}
