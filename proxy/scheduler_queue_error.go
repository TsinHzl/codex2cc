package proxy

import (
	"errors"
	"net/http"

	"github.com/codex2cc/api"
	"github.com/codex2cc/auth"
	"github.com/gin-gonic/gin"
)

const schedulerQueueFullMessage = "Scheduler wait queue is full. Retry after 1 second."

func schedulerQueueFullAPIError() *api.APIError {
	return api.NewAPIError(api.ErrCodeServiceUnavailable, schedulerQueueFullMessage, api.ErrorTypeServer)
}

// Queue overload is a local retryable capacity error. It must not enter the
// quota scan or continuous-retry exclusion reset, or overwrite committed SSE.
func writeSchedulerQueueError(c *gin.Context, err error, protocol continuousRetryHTTPProtocol) bool {
	if !errors.Is(err, auth.ErrSchedulerQueueFull) {
		return false
	}
	if !claimContinuousRetryTerminal(c, protocol) || c.Request.Context().Err() != nil {
		return true
	}
	if !c.Writer.Written() {
		c.Header("Retry-After", "1")
	}
	switch protocol {
	case continuousRetryProtocolAnthropic:
		if !writeCommittedAnthropicRetryError(c, "overloaded_error", schedulerQueueFullMessage) {
			sendAnthropicError(c, http.StatusServiceUnavailable, "overloaded_error", schedulerQueueFullMessage)
		}
	case continuousRetryProtocolChat:
		if !writeCommittedChatRetryError(c, schedulerQueueFullMessage) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": schedulerQueueFullAPIError()})
		}
	default:
		if !writeCommittedResponsesRetryError(c, schedulerQueueFullMessage) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": schedulerQueueFullAPIError()})
		}
	}
	return true
}
