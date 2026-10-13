package proxy

import (
	"strings"
	"time"

	"github.com/codex2cc/api"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// 本文件是从 codex2api 抽离时的最小桩集合：promptfilter 包与 NewAPI 下游对接
// 不在 codex2cc 范围内（CC 直连场景不存在 NewAPI 签名身份与提示词过滤管线），
// 但 responses_ws.go / handler_anthropic.go 的调用链保留，因此这里提供
// 「策略禁用」语义的直通实现，保持原调用点行为不变。

// promptFilterConfig 是 promptfilter.Config 的最小替代，仅保留调用点用到的字段。
type promptFilterConfig struct {
	Enabled  bool
	Advanced struct {
		Output struct {
			Enabled     bool
			BufferBytes int
		}
		NewAPI struct {
			Enabled bool
		}
	}
}

func (h *Handler) promptFilterConfigForRequest(c *gin.Context) promptFilterConfig {
	_ = c
	return promptFilterConfig{}
}

// wsPromptOutputBuffer 无过滤缓冲：唯一语义是直通。
type wsPromptOutputBuffer struct{}

func newWSPromptOutputBuffer(cfg promptFilterConfig) *wsPromptOutputBuffer {
	_ = cfg
	return nil
}

// Push 恒直通。调用点可能持有 nil 接收者，也可能持有非 nil 接收者
// （newWSPromptOutputBuffer 的返回值），两条路径都必须把消息原样交还，
// 否则消息会被静默丢弃。
func (b *wsPromptOutputBuffer) Push(message []byte) ([][]byte, error) {
	return [][]byte{message}, nil
}

func (b *wsPromptOutputBuffer) Flush() ([][]byte, error) {
	return nil, nil
}

// NewAPI 签名身份校验链：CC 场景恒不拒绝、绑定恒有效。
func (h *Handler) rejectRequiredNewAPIIdentity(c *gin.Context, cfg promptFilterConfig, body []byte) bool {
	_ = c
	_ = cfg
	_ = body
	return false
}

func (h *Handler) refreshNewAPIWebSocketBinding(c *gin.Context, now time.Time) *api.APIError {
	_ = c
	_ = now
	return nil
}

// WebSocket 帧级安全上下文：CC 场景无帧级状态可重置/释放。
func resetPromptRequestSecurityFrame(c *gin.Context) {
	_ = c
}

func releasePromptRequestFrameBody(c *gin.Context) {
	_ = c
}

func resetPromptPolicyRequestCorrelationID(c *gin.Context) string {
	_ = c
	return ""
}

const promptGuardPolicyEventIDContextKey = "prompt_filter_policy_event_id"

// 上游 cyber_policy 处罚链：仅保留错误文案映射，不做证据落库与 NewAPI 决策。
const upstreamCyberPolicyUserMessage = "此内容因可能存在网络安全风险而被标记，本次已记录。请重新表述请求；再次触发可能会停用账号。如果确认是误判，请联系管理员。"

type upstreamCyberPolicyAttempt struct {
	Transport    string
	StatusCode   int
	AccountID    int64
	AttemptIndex int
}

func upstreamPromptPolicyTransport(stream, viaWebsocket bool) string {
	if viaWebsocket {
		return "websocket"
	}
	if stream {
		return "sse"
	}
	return "http"
}

// upstreamCyberPolicyCode 与上游实现保持一致：扫描错误体中的 cyber_policy 标记。
func upstreamCyberPolicyCode(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	for _, path := range []string{"codex_error_info", "error.codex_error_info", "error.code", "error.type", "code", "type"} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); strings.EqualFold(value, "cyber_policy") {
			return "cyber_policy"
		}
	}
	return ""
}

func isExplicitUpstreamCyberPolicy(body []byte) bool {
	return upstreamCyberPolicyCode(responseFailedErrorBody(body)) != ""
}

func upstreamCyberPolicyResponseMessage(c *gin.Context) string {
	_ = c
	return upstreamCyberPolicyUserMessage
}

// logUpstreamCyberPolicy 不再落证据库：恒返回空 incidentID 且未记录。
func (h *Handler) logUpstreamCyberPolicy(c *gin.Context, endpoint string, model string, body []byte, attempts ...upstreamCyberPolicyAttempt) (string, bool) {
	_ = c
	_ = endpoint
	_ = model
	_ = body
	_ = attempts
	return "", false
}

func acceptedPromptPolicyIncidentID(incidentID string, accepted bool) string {
	if !accepted {
		return ""
	}
	return incidentID
}

// newAPIPolicyDecisionMetadata 最小结构：CC 场景恒无委派决策。
type newAPIPolicyDecisionMetadata struct {
	DecisionID         string
	EventID            string
	RequestID          string
	ReasonCode         string
	ConversationLocked bool
}

const newAPIUpstreamCyberDecisionContextKey = "prompt_filter_newapi_upstream_cyber_decision"

func clearNewAPIUpstreamCyberPolicyDecision(c *gin.Context) {
	if c != nil {
		c.Set(newAPIUpstreamCyberDecisionContextKey, nil)
	}
}

func newAPIUpstreamCyberPolicyDecision(c *gin.Context) (newAPIPolicyDecisionMetadata, bool) {
	_ = c
	return newAPIPolicyDecisionMetadata{}, false
}

func newAPIPolicyDecisionAPIError(metadata newAPIPolicyDecisionMetadata) *api.APIError {
	_ = metadata
	return api.NewAPIError(api.ErrorCode("request_policy_violation"), "请求违反安全策略，本次请求已被拒绝", api.ErrorTypeInvalidRequest)
}

func normalizeNewAPIPolicyWebSocketEventID(eventID string) string {
	return strings.TrimSpace(eventID)
}
