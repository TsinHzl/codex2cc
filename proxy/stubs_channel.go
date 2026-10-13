package proxy

import (
	"context"
	"net/http"
	"strings"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 本文件是渠道裁剪后的兼容桩集合。codex2cc 只保留「Claude Code → Codex 上游」
// 一条链路，原 claude 原生上游等传输实现已整体剔除。
// 这里保留被保留代码引用的类型与函数最小实现，使其行为退化为 no-op / 不支持，
// 而不是把已经剔除的渠道逻辑重新引回。

// ---------------------------------------------------------------------------
// relay 协议标识
// ---------------------------------------------------------------------------

// RelayProtocol 标识下游入站协议，供保留的 relay 转发路由/日志代码引用。
type RelayProtocol string

const (
	RelayProtocolChatCompletions RelayProtocol = "chat_completions"
	RelayProtocolMessages        RelayProtocol = "messages"
	RelayProtocolResponses       RelayProtocol = "responses"
)

// isMediaOnlyModel 判断模型是否只能走媒体端点(images),不接受文本请求。
// codex2cc 无媒体端点，仅保留生图模型族的识别用于提示文案。
func isMediaOnlyModel(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return false
	}
	return isImageOnlyModel(name)
}

// ---------------------------------------------------------------------------
// relay 上游传输（relay 账号池保留）
// ---------------------------------------------------------------------------

// relayUpstreamEndpointForProtocol 返回 relay 账号上该协议对应的上游端点。
// channel 为 RelayProtocolMessages 时沿用 Anthropic Messages 端点。
func relayUpstreamEndpointForProtocol(account *auth.Account, inbound RelayProtocol, model string) string {
	_ = account
	_ = model
	if inbound == RelayProtocolMessages {
		return "/v1/messages"
	}
	return "/v1/responses"
}

// ExecuteRelayStyleProtocolRequest 把入站协议体转发到 relay 上游。codex2cc 的
// relay 账号统一走 OpenAI Responses 通道，故直接委托给本地已有的执行器。
func ExecuteRelayStyleProtocolRequest(ctx context.Context, account *auth.Account, inbound RelayProtocol, inboundBody, responsesBody []byte, proxyOverride string, headers http.Header) (*http.Response, error) {
	_ = inbound
	_ = inboundBody
	return ExecuteOpenAIResponsesRequest(ctx, account, responsesBody, proxyOverride, headers)
}

// ---------------------------------------------------------------------------
// 图片能力剥离 / 意图识别
// ---------------------------------------------------------------------------

func normalizeImageToolModel(model string) (string, string) {
	return normalizeImageToolModelForPrompt(model, "")
}

func inferImageAspectFromPrompt(prompt string) string {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	if normalized == "" {
		return ""
	}

	containsAny := func(keywords ...string) bool {
		for _, keyword := range keywords {
			if strings.Contains(normalized, strings.ToLower(keyword)) {
				return true
			}
		}
		return false
	}

	if containsAny("方图", "方形", "正方形", "square", "1:1") {
		return "square"
	}
	if containsAny("竖版", "竖屏", "纵向", "竖向", "手机壁纸", "手机屏保", "手机海报", "portrait", "vertical", "phone wallpaper", "mobile wallpaper", "9:16") {
		return "portrait"
	}
	if containsAny("横版", "横屏", "横向", "宽屏", "桌面壁纸", "电脑壁纸", "电脑桌面", "landscape", "horizontal", "wide", "widescreen", "desktop wallpaper", "16:9") {
		return "landscape"
	}

	if containsAny("头像", "图标", "徽标", "贴纸", "表情包", "logo", "icon", "avatar", "sticker") {
		return "square"
	}
	if containsAny("海报", "poster", "封面", "cover") {
		return "portrait"
	}
	if containsAny("壁纸", "wallpaper", "电影感", "cinematic", "banner", "横幅") {
		return "landscape"
	}
	return ""
}

func normalizeImageIntentText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	return strings.Join(strings.Fields(text), " ")
}

// imageIntentFalsePositivePhrases 是否定语境短语：命中即判定为「并非生图请求」。
var imageIntentFalsePositivePhrases = []string{
	"生成图片的代码",
	"生成图片代码",
	"生成图片的脚本",
	"生成图片脚本",
	"生成图片的函数",
	"生成图片函数",
	"图片生成函数",
	"写一个生成图片",
}

// imageIntentPositivePhrases 是肯定语境短语：命中即判定为「生图请求」。
var imageIntentPositivePhrases = []string{
	"生图",
	"文生图",
	"图生图",
	"生成一张",
	"生成一幅",
	"生成图片",
	"生成照片",
	"生成海报",
}

func stripImageGenerationToolsFromArray(tools []gjson.Result) ([]interface{}, bool) {
	kept := make([]interface{}, 0, len(tools))
	removed := false
	for _, tool := range tools {
		if strings.TrimSpace(tool.Get("type").String()) == "image_generation" {
			removed = true
			continue
		}
		kept = append(kept, tool.Value())
	}
	return kept, removed
}

// stripResponsesInputImageTools 从 Responses 入站体的 input 数组中剔除
// additional_tools 里的图片能力声明；结构与原实现保持一致。
func stripResponsesInputImageTools(body []byte) []byte {
	if len(body) == 0 || !gjson.GetBytes(body, `input.#(type%"*additional_tools*")`).Exists() {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	changed := false
	rebuilt := make([]interface{}, 0, len(input.Array()))
	for _, item := range input.Array() {
		if strings.Contains(item.Get("type").String(), "additional_tools") {
			tools, removed := stripImageGenerationToolsFromArray(item.Get("tools").Array())
			if !removed {
				rebuilt = append(rebuilt, item.Value())
				continue
			}
			changed = true
			entry, ok := item.Value().(map[string]interface{})
			if !ok {
				rebuilt = append(rebuilt, item.Value())
				continue
			}
			entry["tools"] = tools
			rebuilt = append(rebuilt, entry)
			continue
		}
		rebuilt = append(rebuilt, item.Value())
	}
	if !changed {
		return body
	}
	out, err := sjson.SetBytes(body, "input", rebuilt)
	if err != nil {
		return body
	}
	return out
}

// parseGPTImage25Pricing 解析上游定价页中的 gpt-image 价目表。codex2cc 不再
// 抓取该页面，返回空表（保留函数以满足定价覆盖合并逻辑的调用形态）。
func parseGPTImage25Pricing(body []byte) map[string]database.ModelPricingOverride {
	_ = body
	return map[string]database.ModelPricingOverride{}
}

// ---------------------------------------------------------------------------
// live 模型别名 / new-api 决策明细
// ---------------------------------------------------------------------------

var liveModelAliases = []string{"gpt-live", "gpt-live-1", "gpt-live-1-mini"}

func liveModelExplicitlyDenied(id string, deny []string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return false
	}
	for _, item := range deny {
		if strings.ToLower(strings.TrimSpace(item)) == id {
			return true
		}
	}
	return false
}

// newAPIPolicyDecisionDetails 渲染 new-api 委派决策明细。codex2cc 无委派链路，
// 仅输出元数据本身供日志定位。
func newAPIPolicyDecisionDetails(metadata newAPIPolicyDecisionMetadata) gin.H {
	return gin.H{
		"decision_id":         metadata.DecisionID,
		"event_id":            metadata.EventID,
		"request_id":          metadata.RequestID,
		"reason_code":         metadata.ReasonCode,
		"conversation_locked": metadata.ConversationLocked,
	}
}
