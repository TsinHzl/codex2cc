package proxy

import (
	"context"
	"net/http"

	"github.com/codex2cc/database"
	"github.com/gin-gonic/gin"
)

// 本文件收拢从 codex2api 抽离过程中被剔除功能所遗留的引用点。这些符号原属
// prompt filter / new-api 委派 / 内部 compact / telemetry 等已裁剪子系统，
// 此处退化为 no-op 或「不适用」返回，保证核心 Messages 链路行为不受影响。

// contextAPIKeyConcurrencyInherited 标记子请求继承父请求的并发配额。
// codex2cc 无内部派生请求链路，该标记不再被设置。
const contextAPIKeyConcurrencyInherited = "apiKeyConcurrencyInherited"

// contextDisableOverflowAutoCompact / internalReasonOverflowCompact 是内部
// compact 逻辑使用的上下文键与原因标记，供保留的请求链路引用。
const (
	contextDisableOverflowAutoCompact = "disableOverflowAutoCompact"
	internalReasonOverflowCompact     = "overflow_compact"
)

func (h *Handler) enforceRequiredNewAPIIdentityAtIngress(c *gin.Context) bool {
	_ = c
	return false
}

func (h *Handler) capturePromptRequestIngress(c *gin.Context, body []byte) {
	_ = c
	_ = body
}

func (h *Handler) inspectPromptFilterAnthropic(c *gin.Context, rawBody []byte, endpoint, model string) bool {
	_ = c
	_ = rawBody
	_ = endpoint
	_ = model
	return false
}

func (h *Handler) inspectPromptFilterOpenAI(c *gin.Context, model string) bool {
	_ = c
	_ = model
	return false
}

func (h *Handler) logPromptPolicyRetryUsage(c *gin.Context, input database.UsageLogInput, incidentID string) {
	_ = c
	_ = input
	_ = incidentID
}

func (h *Handler) executeInternalResponseWithAttribution(ctx context.Context, body []byte, attribution *internalResponseAttribution) (int, []byte) {
	return http.StatusNotImplemented, nil
}

type internalResponseAttribution struct{}

func internalResponseAttributionFromRequest(c *gin.Context, reason string) *internalResponseAttribution {
	return nil
}

func truncateForLog(body []byte, limit int) string {
	if limit <= 0 || len(body) <= limit {
		return string(body)
	}
	return string(body[:limit])
}

type codexTelemetryRequest struct {
	account       interface{}
	body          []byte
	sessionID     string
	proxyOverride string
	apiKey        string
	deviceCfg     interface{}
	headers       http.Header
}

type codexTelemetryAttempt struct{}

func beginCodexTelemetry(input codexTelemetryRequest) *codexTelemetryAttempt {
	return &codexTelemetryAttempt{}
}

func (a *codexTelemetryAttempt) observeResult(resp *http.Response, err error) {}

var _ = database.UpstreamChannelCodex
