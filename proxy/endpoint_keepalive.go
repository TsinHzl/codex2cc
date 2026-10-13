package proxy

import (
	"context"
	"fmt"
	"io"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/database"
)

// upstreamResponseReadContext 保留普通请求取消后的有界 usage 补读窗口。
// 持续重试的私有尝试则随下游立即结束，不能因补读而延长重试生命周期。
func upstreamResponseReadContext(downstreamCtx, upstreamCtx context.Context, policy database.ContinuousRetryPolicy) context.Context {
	if continuousRetryBuffersAttempts(policy) || upstreamCtx == nil {
		return downstreamCtx
	}
	return upstreamCtx
}

// readAllLimitedWithContinuousRetryKeepalive 将读取上限与请求级保活组合，
// 用于非流式成功响应及原生协议 JSON 聚合，避免读体阶段长时间没有下游活动。
func readAllLimitedWithContinuousRetryKeepalive(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("response body limit must be non-negative")
	}
	body, err := readAllWithContinuousRetryKeepalive(ctx, io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return body, nil
}

// activateAnthropicMessagesKeepalive 在选号后启用 Messages 保活。
// relay 和 Codex 路径始终启用保活。
func (h *Handler) activateAnthropicMessagesKeepalive(ctx context.Context, account *auth.Account, stream bool) {
	_ = account
	if stream {
		setContinuousRetryKeepaliveActive(ctx, true)
		return
	}
	setContinuousRetryKeepaliveActive(ctx, true)
}

// setContinuousRetryKeepaliveActive 供重试换号时切换保活策略（relay 与 Codex
// 路径行为一致，保留开关以便未来差异化）。
func setContinuousRetryKeepaliveActive(ctx context.Context, active bool) {
	keepalive := continuousRetryKeepaliveForContext(ctx)
	if keepalive == nil {
		return
	}
	if controller, ok := keepalive.(interface{ SetEnabled(bool) }); ok {
		controller.SetEnabled(active)
	}
	if active {
		activateContinuousRetryKeepalive(ctx)
	}
}
