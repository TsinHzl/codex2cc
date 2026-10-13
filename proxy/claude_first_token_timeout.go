package proxy

import (
	"time"

	"github.com/codex2cc/auth"
)

// claudeFirstTokenTimeoutFor 返回本次 attempt 应使用的首字超时。codex2cc 只保留
// Codex/relay 渠道，Claude OAuth 的专属首字超时随渠道整体剔除，统一跟随全局
// first_token_timeout_seconds（签名保留以兼容原调用点）。
func claudeFirstTokenTimeoutFor(_ *auth.Store, _ *auth.Account) time.Duration {
	return currentFirstTokenTimeout()
}
