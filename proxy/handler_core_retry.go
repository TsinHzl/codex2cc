package proxy

// 本文件承接从 codex2api 抽离的「渠道无关」重试与冷却核心逻辑：
// 429/用量上限分类、账号冷却决策、会话亲和绑定、重试等待预算与用量落库。
// 原生上游渠道的分支已随渠道剔除整体删除。

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type usageLimitDetails struct {
	message         string
	planType        string
	resetsAt        int64
	resetsInSeconds int64
}

func capacityShedRetainsAffinity(outcome streamOutcome, retriesSoFar int) bool {
	return outcome.capacityShed && retriesSoFar < maxCapacityShedSameAccountRetries
}

func firstGJSONInt(body []byte, paths ...string) int64 {
	for _, path := range paths {
		result := gjson.GetBytes(body, path)
		if result.Exists() {
			return result.Int()
		}
	}
	return 0
}

// 上游给出的重置时间来自响应体，属不可信输入：过大的秒数在
// time.Duration 乘法中会溢出为负值，使冷却时长变成"已过期"而被立即清除，
// 账号会被反复选中并持续撞限流。这里统一给出来自上游的时间上限——
// 30 天的秒数上限，以及 time.Unix 可安全表示的最大 Unix 秒（9999-12-31）。
const (
	maxResetSeconds = int64(30 * 24 * 60 * 60)
	maxUnixSeconds  = int64(253402300799)
)

func parseRetryAfter(body []byte) time.Duration {
	if len(body) == 0 {
		return 2 * time.Minute
	}

	// 解析 error.resets_at (Unix timestamp)
	if resetsAt := gjson.GetBytes(body, "error.resets_at").Int(); resetsAt > 0 && resetsAt <= maxUnixSeconds {
		resetTime := time.Unix(resetsAt, 0)
		// resets_at 与 resets_in_seconds 统一收敛到「距当前最多 30 天」，
		// 防止上游回极大值把年限级冷却持久化进 DB。
		if maxReset := time.Now().Add(time.Duration(maxResetSeconds) * time.Second); resetTime.After(maxReset) {
			resetTime = maxReset
		}
		if resetTime.After(time.Now()) {
			d := time.Until(resetTime)
			if d > 0 {
				return d
			}
		}
	}

	// 解析 error.resets_in_seconds
	if secs := gjson.GetBytes(body, "error.resets_in_seconds").Int(); secs > 0 {
		if secs > maxResetSeconds {
			secs = maxResetSeconds
		}
		return time.Duration(secs) * time.Second
	}

	// 默认 2 分钟
	return 2 * time.Minute
}

func parseRetryAfterResetAt(body []byte, now time.Time) (time.Time, bool) {
	if len(body) == 0 {
		return time.Time{}, false
	}

	if resetsAt := firstGJSONInt(body, "error.resets_at", "response.error.resets_at", "response.status_details.error.resets_at"); resetsAt > 0 && resetsAt <= maxUnixSeconds {
		resetTime := time.Unix(resetsAt, 0)
		if maxReset := now.Add(time.Duration(maxResetSeconds) * time.Second); resetTime.After(maxReset) {
			resetTime = maxReset
		}
		if resetTime.After(now) {
			return resetTime, true
		}
	}

	if secs := firstGJSONInt(body, "error.resets_in_seconds", "response.error.resets_in_seconds", "response.status_details.error.resets_in_seconds"); secs > 0 {
		if secs > maxResetSeconds {
			secs = maxResetSeconds
		}
		return now.Add(time.Duration(secs) * time.Second), true
	}

	return time.Time{}, false
}

func parseUsageLimitResetAt(body []byte, now time.Time) (time.Time, bool) {
	if !IsUsageLimitReachedError(body) {
		return time.Time{}, false
	}
	return parseRetryAfterResetAt(body, now)
}

func classifyCodex429Window(resp *http.Response, now time.Time) (codexRateLimitWindow, time.Time, bool) {
	if resp == nil {
		return codexRateLimitWindowUnknown, time.Time{}, false
	}

	primary := parseCodexWindowUsage(
		resp.Header.Get("x-codex-primary-used-percent"),
		resp.Header.Get("x-codex-primary-window-minutes"),
		resp.Header.Get("x-codex-primary-reset-after-seconds"),
	)
	secondary := parseCodexWindowUsage(
		resp.Header.Get("x-codex-secondary-used-percent"),
		resp.Header.Get("x-codex-secondary-window-minutes"),
		resp.Header.Get("x-codex-secondary-reset-after-seconds"),
	)

	var exhausted []codexWindowUsage
	if primary.valid && primary.usedPct >= 100 {
		exhausted = append(exhausted, primary)
	}
	if secondary.valid && secondary.usedPct >= 100 {
		exhausted = append(exhausted, secondary)
	}
	if len(exhausted) == 0 {
		return codexRateLimitWindowUnknown, time.Time{}, false
	}

	chosen := exhausted[0]
	for _, candidate := range exhausted[1:] {
		if candidate.windowMin > chosen.windowMin {
			chosen = candidate
		}
	}

	var resetAt time.Time
	if chosen.resetSec > 0 {
		resetAt = now.Add(time.Duration(chosen.resetSec) * time.Second)
	}
	return codexWindowType(chosen.windowMin), resetAt, !resetAt.IsZero()
}

func responseHasCodex5hHeaders(resp *http.Response) bool {
	if resp == nil {
		return false
	}

	primary := parseCodexWindowUsage(
		resp.Header.Get("x-codex-primary-used-percent"),
		resp.Header.Get("x-codex-primary-window-minutes"),
		resp.Header.Get("x-codex-primary-reset-after-seconds"),
	)
	if primary.valid && codexWindowType(primary.windowMin) == codexRateLimitWindow5h {
		return true
	}

	secondary := parseCodexWindowUsage(
		resp.Header.Get("x-codex-secondary-used-percent"),
		resp.Header.Get("x-codex-secondary-window-minutes"),
		resp.Header.Get("x-codex-secondary-reset-after-seconds"),
	)
	return secondary.valid && codexWindowType(secondary.windowMin) == codexRateLimitWindow5h
}

func classifySpark429RateLimit(account *auth.Account, body []byte, resp *http.Response, now time.Time, model string) codex429Decision {
	decision := codex429Decision{
		Scope:  rateLimitScopeModel,
		Reason: "rate_limited_model",
		Model:  strings.TrimSpace(model),
	}
	if isCodexModelCapacityError(body) {
		decision.Reason = "model_capacity"
	}

	if IsUsageLimitReachedError(body) {
		decision.Reason = "spark_usage_limit"
		if resetAt, ok := parseUsageLimitResetAt(body, now); ok {
			decision.ResetAt = resetAt
			decision.Cooldown = resetAt.Sub(now)
			return decision
		}
	}

	windowType, resetAt, hasWindowReset := classifyCodex429Window(resp, now)
	switch windowType {
	case codexRateLimitWindow5h:
		if !hasWindowReset {
			resetAt = now.Add(5 * time.Hour)
		}
		decision.Reason = "spark_usage_limit"
		decision.ResetAt = resetAt
		decision.Cooldown = resetAt.Sub(now)
		return decision
	case codexRateLimitWindow7d:
		if !hasWindowReset {
			resetAt = now.Add(7 * 24 * time.Hour)
		}
		decision.Reason = "spark_usage_limit"
		decision.ResetAt = resetAt
		decision.Cooldown = resetAt.Sub(now)
		return decision
	}

	if decision.Reason == "spark_usage_limit" {
		decision.Cooldown = usageLimitFallbackCooldown(account, body)
		decision.ResetAt = now.Add(decision.Cooldown)
		return decision
	}
	if decision.Reason == "model_capacity" {
		decision.Cooldown = 5 * time.Minute
		return decision
	}
	// Spark 瞬时 throttle 与主模型共享账号预算；只冻 Spark 模型会被别名绕过。
	return transientAccountRateLimitDecision(body, resp, now)
}

func transientAccountRateLimitDecision(body []byte, resp *http.Response, now time.Time) codex429Decision {
	cooldown := auth.TransientRateLimitBackoffBase
	if retryAfter := transient429RetryAfter(body, resp, now); retryAfter > cooldown {
		cooldown = retryAfter
	}
	if cooldown > auth.TransientRateLimitBackoffMax {
		cooldown = auth.TransientRateLimitBackoffMax
	}
	return codex429Decision{
		Scope:    rateLimitScopeAccount,
		Reason:   "rate_limited",
		ResetAt:  now.Add(cooldown),
		Cooldown: cooldown,
	}
}

func usageLimitFallbackCooldown(account *auth.Account, body []byte) time.Duration {
	planType := ""
	if details, ok := parseUsageLimitDetails(body); ok {
		planType = details.planType
	}
	if planType == "" && account != nil {
		planType = account.GetPlanType()
	}
	switch auth.NormalizePlanType(planType) {
	case "free":
		return 7 * 24 * time.Hour
	default:
		return 5 * time.Hour
	}
}

func windowMinutesToCooldown(windowMinutes float64) time.Duration {
	switch {
	case windowMinutes >= 1440: // >= 1 天 → 7d 窗口
		return 7 * 24 * time.Hour
	case windowMinutes >= 60: // >= 1 小时 → 5h 窗口
		return 5 * time.Hour
	default:
		return 30 * time.Minute // 短窗口
	}
}

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v := 0.0
	fmt.Sscanf(s, "%f", &v)
	return v
}

func normalizedRetryAfter(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	digits := true
	for _, r := range value {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		return value
	}
	if _, err := http.ParseTime(value); err == nil {
		return value
	}
	return ""
}

func codexUnsupportedModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	candidates := []string{
		gjson.GetBytes(body, "error.message").String(),
		gjson.GetBytes(body, "detail").String(),
		gjson.GetBytes(body, "message").String(),
		string(body),
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if match := codexUnsupportedModelRe.FindStringSubmatch(candidate); len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
	}
	return ""
}

const (
	rateLimitScopeAccount = "account"
	rateLimitScopeModel   = "model"
)

const transportRetryPolicySticky = "sticky"

const (
	unlimitedRetryBackoffBase = 250 * time.Millisecond
	unlimitedRetryBackoffMax  = 30 * time.Second
)

const maxRetryAfterDelay = 5 * time.Minute

func (h *Handler) applyResponseFailedCooldown(account *auth.Account, payload []byte, resp *http.Response, model string) codex429Decision {
	if h == nil || account == nil || len(payload) == 0 {
		return codex429Decision{}
	}
	body := responseFailedErrorBody(payload)
	statusCode := classifyResponseFailedOutcome(payload).logStatusCode
	if statusCode == http.StatusTooManyRequests && !account.IsRelayStyle() {
		// 官方 Codex 的 response.failed/error 是已建立 HTTP/WS 流之后的
		// 语义错误。此时 resp 描述的是外层成功响应或 WS 握手，普通模型不能拿它的 x-codex-*
		// 快照推断本次 429 的账号窗口。Spark 有独立模型配额，保留 headers
		// 后仍须由 classifySpark429RateLimit 验证窗口确已耗尽。真正的
		// HTTP/WS dial 429 不走本 helper，仍会在 transport failure 路径中
		// 把原始 resp 交给 applyCooldownForModel。
		if !isProOnlyModel(model) {
			resp = nil
			// 明确的模型容量错误仍只应摘掉当前模型；普通 rate_limit_exceeded
			// 才回落到账号级短冷却。
			if !isCodexModelCapacityError(body) {
				model = ""
			}
		}
	}
	return h.applyCooldownForModel(account, statusCode, body, resp, model)
}

func (h *Handler) bindBufferedStickyRetryAffinity(ctx context.Context, affinityKey string, account *auth.Account, proxyURL string, stickyRetry bool, policy database.ContinuousRetryPolicy) bool {
	if h == nil || h.store == nil || account == nil || !stickyRetry || !continuousRetryBuffersAttempts(policy) {
		return true
	}
	return bindContinuousRetrySessionAffinity(ctx, h.store, affinityKey, account, proxyURL)
}

// logUsageForRequest 汇总请求上下文的归因元数据后落库。codex2cc 只保留
// Codex 链路的填充器：compact / ultra / 生图 / 内容策略等随功能裁剪一并删除。
func (h *Handler) logUsageForRequest(c *gin.Context, input *database.UsageLogInput) {
	populateAPIKeyMetaFromContext(c, input)
	populateClientIPFromRequest(c, input)
	populateUserAgentMetaFromRequest(c, input)
	populateWsAcquireFromRequest(c, input)
	populateUpstreamTrace(c, input)
	populateInternalUsageMetaFromContext(c, input)
	populateCompactUsageMetaFromRequest(c, input)
	input = database.SnapshotUsageLogBilling(input)
	h.logUsage(input)
}

func (h *Handler) nextAccountForSessionWithDispatchGuard(sessionID string, apiKeyID int64, exclude map[int64]bool, filter auth.AccountFilter, policy auth.DispatchPolicy) (*auth.Account, string, auth.SessionAffinityGuard) {
	if h == nil || h.store == nil {
		return nil, "", auth.SessionAffinityGuard{}
	}
	return h.store.NextForSessionWithDispatchGuard(sessionID, apiKeyID, exclude, filter, policy)
}

func (h *Handler) reportStreamOutcomeFailure(account *auth.Account, outcome streamOutcome, d time.Duration) {
	if h == nil || h.store == nil || account == nil {
		return
	}
	if outcome.capacityShed || outcome.requestScoped {
		return
	}
	h.store.ReportRequestFailure(account, outcome.failureKind, d)
}

func (h *Handler) resolveProxyForAttempt(account *auth.Account, stickyProxyURL string) string {
	if h != nil && h.store != nil {
		if proxyURL := strings.TrimSpace(stickyProxyURL); proxyURL != "" && !h.store.ManagedProxyUnavailable(proxyURL) {
			return proxyURL
		}
		return h.store.ResolveProxyForAccount(account)
	}
	return strings.TrimSpace(stickyProxyURL)
}

func (h *Handler) shouldStickyTransportRetry(err error, kind string, timedOut, shouldRetry bool, policy database.ContinuousRetryPolicy) bool {
	if !shouldRetry || timedOut || kind == "" || kind == upstreamErrorKindWsBusyAcquire || !h.stickyTransportRetryEnabled() {
		return false
	}
	if policy.CatchesAllUpstreamFailures() {
		return false
	}
	return !continuousRetryRequestErrorSelected(policy, err) && (err == nil || !policy.MatchesTransport(err.Error()))
}

func (h *Handler) unbindOrRetainAffinityForCapacityShedWithGuard(exclusions *retryAccountExclusions, affinityKey string, account *auth.Account, proxyURL string, guard auth.SessionAffinityGuard, outcome streamOutcome, retries map[int64]int, policy database.ContinuousRetryPolicy) {
	if h == nil || h.store == nil || account == nil || retries == nil {
		return
	}
	id := account.ID()
	// Catch-all promises a real account rotation for every upstream failure.
	// Keep the legacy same-account capacity backoff only for the normal,
	// selective policy mode.
	if !policy.CatchesAllUpstreamFailures() && capacityShedRetainsAffinity(outcome, retries[id]) {
		// Buffered attempts defer affinity until replay resolves. Bind here only
		// for real upstream capacity retries to preserve same-account backoff.
		// 缓冲 attempt 会把亲和绑定延后到回放完成；这里只为真实上游降载重试绑定，
		// 以保留同账号退避行为。
		if continuousRetryBuffersAttempts(policy) {
			h.store.BindSessionAffinityWithGuard(affinityKey, account, proxyURL, guard)
		}
		retries[id]++
		return
	}
	h.store.UnbindSessionAffinity(affinityKey, id)
	if outcome.capacityShed {
		exclusions.MarkSoft(id)
	}
}

func (h *Handler) waitBeforeRetryWithBudget(ctx context.Context, retryOrdinal, retryLimit int, responses ...*http.Response) bool {
	return h.waitBeforeRetryWithBudgetMode(ctx, retryOrdinal, retryLimit, true, responses...)
}

func (h *Handler) waitBeforeRetryWithFirstTokenTimeout(ctx context.Context, firstTokenTimeout bool, retryOrdinal, retryLimit int, responses ...*http.Response) bool {
	if firstTokenTimeout && retryLimit != -1 {
		return ctx == nil || ctx.Err() == nil
	}
	return h.waitBeforeRetryWithBudget(ctx, retryOrdinal, retryLimit, responses...)
}

func (h *Handler) withModelCooldownFilter(model string, filter auth.AccountFilter) auth.AccountFilter {
	if h == nil || h.store == nil {
		return filter
	}
	return h.store.WithModelCooldownFilter(model, filter)
}

func (h *Handler) effectiveMaxRateLimitRetries(account *auth.Account, fallback int) int {
	// 额外重试上限配置随渠道剔除，恒用调用方给定的 fallback。
	_ = account
	return fallback
}

func (h *Handler) stickyTransportRetryEnabled() bool {
	return h != nil && h.store != nil && h.store.GetTransportRetryPolicy() == transportRetryPolicySticky
}

func (h *Handler) waitBeforeRetryWithBudgetMode(ctx context.Context, retryOrdinal, retryLimit int, useRequestKeepalive bool, responses ...*http.Response) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	activateContinuousRetryDeadlineForLimit(ctx, retryLimit)
	if retryLimit == -1 {
		for _, resp := range responses {
			rememberContinuousRetryHTTPFailure(ctx, resp, nil)
		}
	}
	interval := time.Duration(0)
	if h != nil && h.store != nil {
		interval = time.Duration(h.store.GetRetryIntervalMS()) * time.Millisecond
	}
	// Retry-After is part of the continuous-retry contract only. Finite and
	// disabled retry budgets retain the historical local retry interval and do
	// not let an upstream response introduce an unexpected delay.
	if retryLimit == -1 {
		for _, resp := range responses {
			if resp == nil {
				continue
			}
			rawRetryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
			if rawRetryAfter == "" {
				continue
			}
			retryAfter := parseRetryAfterHeader(rawRetryAfter)
			if retryAfter > maxRetryAfterDelay {
				retryAfter = maxRetryAfterDelay
			}
			if retryAfter > interval {
				interval = retryAfter
			}
		}
	}
	if retryLimit == -1 {
		if backoff := unlimitedRetryBackoff(retryOrdinal, rand.Float64()); backoff > interval {
			interval = backoff
		}
	}
	if retryLimit == -1 && useRequestKeepalive {
		activateContinuousRetryKeepalive(ctx)
	}
	if interval <= 0 {
		return true
	}
	if retryLimit == -1 && useRequestKeepalive {
		return waitWithContinuousRetryKeepalive(ctx, interval)
	}
	return waitForRetryInterval(ctx, interval)
}

func (h *Handler) waitBeforeRetry(ctx context.Context, responses ...*http.Response) bool {
	retryLimit := 0
	if h != nil && h.store != nil {
		retryLimit = h.store.GetMaxRetries()
	}
	return h.waitBeforeRetryWithBudget(ctx, 1, retryLimit, responses...)
}

func (h *Handler) logUsage(input *database.UsageLogInput) {
	if h.db == nil || input == nil {
		return
	}
	input = database.SnapshotUsageLogBilling(input)
	// scope 维度预算（issue #439）在日志落库前先吃到这笔消耗，抵掉窗口聚合缓存的滞后。
	h.recordAPIKeyScopeUsage(input)
	// codex2cc 只有 Codex 渠道，渠道字段恒按 codex 固化。
	if input.Channel == "" {
		input.Channel = database.UpstreamChannelCodex
	}
	// 用量落库带超时，避免 DB 抖动时该调用无限期挂住请求收尾路径；
	// 失败必须留痕，否则漏账会完全静默。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.db.InsertUsageLog(ctx, input); err != nil {
		log.Printf("用量落库失败 (api_key_id=%d, model=%s): %v", input.APIKeyID, input.Model, err)
	}
}

func (h *Handler) applyCooldownForModel(account *auth.Account, statusCode int, body []byte, resp *http.Response, model string) codex429Decision {
	// codex2cc 只有 Codex OAuth 与 relay 账号，其他渠道的错误语义
	// 分支已随渠道剔除。
	if h == nil || h.store == nil || account == nil {
		return codex429Decision{}
	}
	if IsUsageLimitReachedError(body) {
		decision := Apply429Cooldown(h.store, account, body, resp, model)
		log.Printf("账号 %d 触发用量上限 (status=%d, plan=%s, reason=%s)，冷却到 %s", account.ID(), statusCode, account.GetPlanType(), decision.Reason, decision.ResetAt.Format(time.RFC3339))
		return decision
	}
	switch statusCode {
	case http.StatusBadRequest:
		// 账号套餐不支持该模型：按 (账号, 模型) 冷却，调度器随后会跳过该组合；
		// 其余 400 属请求内容问题，不动账号状态。
		if model != "" && isCodexModelUnsupportedError(body) {
			cooldown := h.store.MarkModelCooldown(account, model, 30*time.Minute, "model_not_supported")
			log.Printf("账号 %d (plan=%s) 不支持模型 %s，该模型冷却到 %s", account.ID(), account.GetPlanType(), model, cooldown.ResetAt.Format(time.RFC3339))
			return codex429Decision{
				Scope:    rateLimitScopeModel,
				Reason:   "model_not_supported",
				Model:    model,
				ResetAt:  cooldown.ResetAt,
				Cooldown: time.Until(cooldown.ResetAt),
			}
		}
		return codex429Decision{}
	case http.StatusTooManyRequests:
		decision := Apply429Cooldown(h.store, account, body, resp, model)
		if decision.Scope == rateLimitScopeModel {
			if decision.ResetAt.IsZero() {
				log.Printf("账号 %d 模型 %s 触发短时限流 (reason=%s)，按策略不持久化冷却", account.ID(), decision.Model, decision.Reason)
			} else {
				log.Printf("账号 %d 模型 %s 触发短时限流 (reason=%s)，冷却到 %s", account.ID(), decision.Model, decision.Reason, decision.ResetAt.Format(time.RFC3339))
			}
			return decision
		}
		log.Printf("账号 %d 被限速 (plan=%s, reason=%s)，冷却到 %s", account.ID(), account.GetPlanType(), decision.Reason, decision.ResetAt.Format(time.RFC3339))
		return decision
	case http.StatusUnauthorized:
		// 原子标志瞬间置位，阻止其他并发请求再选到该账号
		atomic.StoreInt32(&account.Disabled, 1)

		if isMissingScopeUnauthorized(body) {
			log.Printf("账号 %d 收到 missing_scope 401，保留在号池", account.ID())
			atomic.StoreInt32(&account.Disabled, 0)
			return codex429Decision{}
		}

		if h.store.GetAutoCleanUnauthorized() {
			// 开启自动清理时，401 立即从号池删除
			log.Printf("账号 %d 收到 401，立即清理", account.ID())
			if h.db != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = h.db.SetError(ctx, account.ID(), "deleted")
				cancel()
				h.db.InsertAccountEventAsync(account.ID(), "deleted", "auto_clean_401")
			}
			h.store.RemoveAccount(account.ID())
		} else {
			h.store.MarkCooldownWithError(account, 5*time.Minute, "unauthorized", upstreamAccountErrorMessage(statusCode, body))
			// MarkCooldownWithError 不会复位上面置位的 Disabled 标志。先写冷却
			// 再复位，消除「标志已清、冷却尚未生效」的并发选号窗口；冷却期由
			// Status/HealthTier 门控，复位仅撤除原子标志的即时阻断。
			atomic.StoreInt32(&account.Disabled, 0)
		}
	case http.StatusPaymentRequired, http.StatusForbidden:
		if statusCode == http.StatusForbidden && IsAgentRuntimeDeletedError(body) {
			atomic.StoreInt32(&account.Disabled, 1)
			errorMsg := upstreamAccountErrorMessage(statusCode, body)
			log.Printf("账号 %d 的 Agent runtime 已删除，标记为封禁", account.ID())
			h.store.MarkCooldownWithErrorExactDuration(account, 24*time.Hour, "unauthorized", errorMsg)
			return codex429Decision{}
		}
		if IsDeactivatedWorkspaceError(body) {
			log.Printf("账号 %d 工作区已停用，标记为错误", account.ID())
			if h.store != nil {
				h.store.MarkDeactivatedWorkspace(account, upstreamAccountErrorMessage(statusCode, body))
			}
			return codex429Decision{}
		}
		h.store.MarkCooldown(account, 30*time.Minute, "payment_required")
	}
	return codex429Decision{}
}

func (h *Handler) applyUpstreamChannelFilter(c *gin.Context, effectiveModel string, filter auth.AccountFilter) auth.AccountFilter {
	// codex2cc 只保留 Codex 渠道：非 Codex 账号（渠道类账号已整体剔除，
	// 判定恒 false）不再参与调度。
	_ = effectiveModel
	switch requestUpstreamChannel(c) {
	case database.UpstreamChannelCodex:
		return func(account *auth.Account) bool {
			if account == nil {
				return false
			}
			return filter == nil || filter(account)
		}
	}
	return filter
}

func Apply429Cooldown(store *auth.Store, account *auth.Account, body []byte, resp *http.Response, model string) codex429Decision {
	// OpenAI Responses/API relay 是直连上游，不是 OAuth 订阅账号。裸 429 通常只是
	// 瞬时负载抑制，默认不应写入 Redis/DB 并把整个模型摘掉 5~30 分钟。
	if account != nil && account.IsRelayStyle() {
		reason := "rate_limited_model"
		if isCodexModelCapacityError(body) {
			reason = "model_capacity"
		}
		decision := codex429Decision{
			Scope:  rateLimitScopeModel,
			Reason: reason,
			Model:  strings.TrimSpace(model),
		}
		if store == nil || decision.Model == "" {
			return decision
		}
		policy := store.ResolveModelCooldownPolicy(account)
		if policy.Mode == database.ModelCooldownModeOff || policy.Seconds <= 0 {
			return decision
		}
		backoff := policy.Mode == database.ModelCooldownModeAdaptive && policy.BackoffEnabled
		cooldown := store.MarkModelCooldownWithBackoff(
			account,
			decision.Model,
			time.Duration(policy.Seconds)*time.Second,
			decision.Reason,
			backoff,
		)
		decision.ResetAt = cooldown.ResetAt
		decision.Cooldown = time.Until(cooldown.ResetAt)
		return decision
	}
	decision := classify429RateLimit(account, body, resp, time.Now(), model)
	if store == nil || account == nil {
		return decision
	}
	// Spark has an independent quota window. Its usage_limit metadata must not
	// rewrite the account plan or the main 5h/7d snapshots before the model-level
	// decision below is applied.
	if details, ok := parseUsageLimitDetails(body); ok && !(decision.Scope == rateLimitScopeModel && isProOnlyModel(model)) {
		store.ApplyUsageLimitMetadata(account, details.planType, decision.ResetAt)
	}
	if decision.Scope == rateLimitScopeModel {
		if isProOnlyModel(model) && decision.Reason == "spark_usage_limit" {
			store.MarkSparkUsageExhausted(account, decision.ResetAt)
			return decision
		}
		policy := store.ResolveModelCooldownPolicy(account)
		if policy.Mode == database.ModelCooldownModeOff || policy.Seconds <= 0 {
			decision.ResetAt = time.Time{}
			decision.Cooldown = 0
			return decision
		}
		backoff := policy.Mode == database.ModelCooldownModeAdaptive && policy.BackoffEnabled
		cooldown := store.MarkModelCooldownWithBackoff(
			account,
			decision.Model,
			time.Duration(policy.Seconds)*time.Second,
			decision.Reason,
			backoff,
		)
		decision.ResetAt = cooldown.ResetAt
		decision.Cooldown = time.Until(cooldown.ResetAt)
		return decision
	}
	if account.IsPremium5hPlan() && decision.Scope == rateLimitScopeAccount && decision.Reason == "rate_limited_5h" {
		store.MarkResponsesPremium5hRateLimited(account, decision.ResetAt)
		return decision
	}
	if decision.Scope == rateLimitScopeAccount && decision.Reason == "rate_limited" {
		// Pass only an actual upstream hint. Reusing the synthetic 15s floor
		// here would slide the same cooldown forward on every in-flight 429.
		applied := store.MarkTransientRateLimited(account, transient429RetryAfter(body, resp, time.Now()))
		decision.Cooldown = applied
		if applied > 0 {
			decision.ResetAt = time.Now().Add(applied)
		}
		return decision
	}
	store.MarkResponsesRateLimited(account, decision.Cooldown)
	return decision
}

func upstreamAccountErrorMessage(statusCode int, body []byte) string {
	if IsDeactivatedWorkspaceError(body) {
		return fmt.Sprintf("上游返回 %d: deactivated_workspace", statusCode)
	}
	message := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(body, "detail.message").String())
	}
	// Do not persist arbitrary HTML/plain-text bodies. Structured JSON fields
	// above are bounded and sanitised; an unstructured body becomes status text.
	if len(message) > 300 {
		message = message[:300]
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return fmt.Sprintf("上游返回 %d: %s", statusCode, message)
}

func unlimitedRetryBackoff(retryOrdinal int, jitterUnit float64) time.Duration {
	if retryOrdinal < 1 {
		retryOrdinal = 1
	}
	if jitterUnit < 0 {
		jitterUnit = 0
	} else if jitterUnit > 1 {
		jitterUnit = 1
	}

	delay := unlimitedRetryBackoffBase
	for i := 1; i < retryOrdinal && delay < unlimitedRetryBackoffMax; i++ {
		if delay > unlimitedRetryBackoffMax/2 {
			delay = unlimitedRetryBackoffMax
			break
		}
		delay *= 2
	}
	jitter := time.Duration(float64(delay/2) * jitterUnit)
	if delay >= unlimitedRetryBackoffMax-jitter {
		return unlimitedRetryBackoffMax
	}
	return delay + jitter
}

func classify429RateLimit(account *auth.Account, body []byte, resp *http.Response, now time.Time, model string) codex429Decision {
	model = strings.TrimSpace(model)
	if isProOnlyModel(model) {
		return classifySpark429RateLimit(account, body, resp, now, model)
	}

	if IsUsageLimitReachedError(body) {
		if resetAt, ok := parseUsageLimitResetAt(body, now); ok {
			reason := "usage_limit"
			if account != nil && account.IsPremium5hPlan() && responseHasCodex5hHeaders(resp) {
				reason = "rate_limited_5h"
			}
			return codex429Decision{
				Scope:    rateLimitScopeAccount,
				Reason:   reason,
				ResetAt:  resetAt,
				Cooldown: resetAt.Sub(now),
			}
		}

		windowType, resetAt, hasWindowReset := classifyCodex429Window(resp, now)
		switch windowType {
		case codexRateLimitWindow5h:
			if !hasWindowReset {
				resetAt = now.Add(5 * time.Hour)
			}
			return codex429Decision{Scope: rateLimitScopeAccount, Reason: "rate_limited_5h", ResetAt: resetAt, Cooldown: resetAt.Sub(now)}
		case codexRateLimitWindow7d:
			if !hasWindowReset {
				resetAt = now.Add(7 * 24 * time.Hour)
			}
			return codex429Decision{Scope: rateLimitScopeAccount, Reason: "rate_limited_7d", ResetAt: resetAt, Cooldown: resetAt.Sub(now)}
		}

		cooldown := usageLimitFallbackCooldown(account, body)
		resetAt = now.Add(cooldown)
		return codex429Decision{Scope: rateLimitScopeAccount, Reason: "usage_limit", ResetAt: resetAt, Cooldown: cooldown}
	}

	windowType, resetAt, hasWindowReset := classifyCodex429Window(resp, now)
	switch windowType {
	case codexRateLimitWindow5h:
		if !hasWindowReset {
			resetAt = now.Add(5 * time.Hour)
		}
		return codex429Decision{Scope: rateLimitScopeAccount, Reason: "rate_limited_5h", ResetAt: resetAt, Cooldown: resetAt.Sub(now)}
	case codexRateLimitWindow7d:
		if !hasWindowReset {
			resetAt = now.Add(7 * 24 * time.Hour)
		}
		return codex429Decision{Scope: rateLimitScopeAccount, Reason: "rate_limited_7d", ResetAt: resetAt, Cooldown: resetAt.Sub(now)}
	}

	if isCodexModelCapacityError(body) && model != "" {
		return codex429Decision{
			Scope:    rateLimitScopeModel,
			Reason:   "model_capacity",
			Model:    model,
			Cooldown: 5 * time.Minute,
		}
	}

	// 裸 429 / rate_limit* 是账号级瞬时限流。只冻当前模型时，同一号换个别名
	// 会立刻再打上游。额度耗尽与 5h/7d=100% 已在上面分流。
	return transientAccountRateLimitDecision(body, resp, now)
}

func transient429RetryAfter(body []byte, resp *http.Response, now time.Time) time.Duration {
	if resp != nil {
		if retryAfter := parseRetryAfterHeader(resp.Header.Get("Retry-After")); retryAfter > 0 {
			return retryAfter
		}
	}
	if resetAt, ok := parseRetryAfterResetAt(body, now); ok {
		if remaining := resetAt.Sub(now); remaining > 0 {
			return remaining
		}
	}
	return 0
}

func parseUsageLimitDetails(body []byte) (usageLimitDetails, bool) {
	if len(body) == 0 {
		return usageLimitDetails{}, false
	}
	if !IsUsageLimitReachedError(body) {
		return usageLimitDetails{}, false
	}
	return usageLimitDetails{
		message:         firstGJSONString(body, "error.message", "response.error.message", "response.status_details.error.message"),
		planType:        firstGJSONString(body, "error.plan_type", "response.error.plan_type", "response.status_details.error.plan_type"),
		resetsAt:        firstGJSONInt(body, "error.resets_at", "response.error.resets_at", "response.status_details.error.resets_at"),
		resetsInSeconds: firstGJSONInt(body, "error.resets_in_seconds", "response.error.resets_in_seconds", "response.status_details.error.resets_in_seconds"),
	}, true
}

func isCodexModelCapacityError(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	candidates := []string{
		gjson.GetBytes(body, "error.message").String(),
		gjson.GetBytes(body, "message").String(),
		string(body),
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "selected model is at capacity") ||
			strings.Contains(lower, "model is at capacity. please try a different model") {
			return true
		}
	}
	return false
}

func populateAPIKeyMetaFromContext(c *gin.Context, input *database.UsageLogInput) {
	if c == nil || input == nil {
		return
	}
	if v, exists := c.Get(contextAPIKeyID); exists && v != nil {
		switch typed := v.(type) {
		case int64:
			input.APIKeyID = typed
		case int:
			input.APIKeyID = int64(typed)
		}
	}
	if v, exists := c.Get(contextAPIKeyName); exists && v != nil {
		if name, ok := v.(string); ok {
			input.APIKeyName = name
		}
	}
	if v, exists := c.Get(contextAPIKeyMasked); exists && v != nil {
		if masked, ok := v.(string); ok {
			input.APIKeyMasked = masked
		}
	}
}

// populateInternalUsageMetaFromContext 保留内部请求的归因字段：codex2cc 无
// 内部 compact 链路，仅透传上游已设置的 reason / parent 请求号。
func populateInternalUsageMetaFromContext(c *gin.Context, input *database.UsageLogInput) {
	if c == nil || input == nil {
		return
	}
	if value, exists := c.Get("internalReason"); exists {
		input.InternalReason, _ = value.(string)
	}
	if value, exists := c.Get("parentRequestID"); exists {
		input.ParentRequestID, _ = value.(string)
	}
}

func populateClientIPFromRequest(c *gin.Context, input *database.UsageLogInput) {
	if c == nil || input == nil || strings.TrimSpace(input.ClientIP) != "" {
		return
	}
	clientIP := strings.TrimSpace(c.ClientIP())
	if clientIP == "" && c.Request != nil {
		clientIP = strings.TrimSpace(c.Request.RemoteAddr)
		if host, _, err := net.SplitHostPort(clientIP); err == nil {
			clientIP = host
		}
	}
	if len(clientIP) > 64 {
		clientIP = clientIP[:64]
	}
	input.ClientIP = clientIP
}

func markCyberPolicyUsageKind(input *database.UsageLogInput) {
	if input == nil || input.UpstreamErrorKind == "cyber_policy" {
		return
	}
	msg := strings.ToLower(input.ErrorMessage)
	if strings.Contains(msg, "cyber_policy") || strings.Contains(msg, "cyber security risk") {
		input.UpstreamErrorKind = "cyber_policy"
	}
}

// populateCompactUsageMetaFromRequest 标记 compact 请求的用量。codex2cc 无
// 内部 compact 链路与压缩历史追踪，仅按入站端点是否为 compact 端点判定。
func populateCompactUsageMetaFromRequest(c *gin.Context, input *database.UsageLogInput) {
	if input == nil {
		return
	}
	if c != nil && c.Request != nil && c.Request.URL != nil {
		input.Compact = isCompactUsageEndpoint(c.Request.URL.Path)
	}
}

const maxCapacityShedSameAccountRetries = 2

func parseRetryAfterHeaderAt(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 || seconds > int64((1<<63-1)/int64(time.Second)) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func parseRetryAfterHeader(value string) time.Duration {
	return parseRetryAfterHeaderAt(value, time.Now())
}

func isCompactUsageEndpoint(endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if cut := strings.IndexAny(endpoint, "?#"); cut >= 0 {
		endpoint = endpoint[:cut]
	}
	endpoint = strings.TrimRight(endpoint, "/")
	return endpoint == "/v1/responses/compact"
}

func compute429Cooldown(account *auth.Account, body []byte, resp *http.Response) time.Duration {
	if account == nil {
		return 2 * time.Minute
	}
	// 1. 优先使用 Codex 响应体中的精确重置时间
	if resetDuration := parseRetryAfter(body); resetDuration > 2*time.Minute {
		// parseRetryAfter 默认返回 2min（无数据），超过 2min 说明解析到了真实的 resets_at/resets_in_seconds
		if resetDuration > 7*24*time.Hour {
			resetDuration = 7 * 24 * time.Hour // 最多 7 天
		}
		return resetDuration
	}

	// 2. 没有精确重置时间，根据套餐类型 + 用量窗口推断
	planType := auth.NormalizePlanType(account.GetPlanType())

	switch planType {
	case "free":
		// Free 只有 7d 窗口，429 = 额度耗尽，冷却 7 天
		return 7 * 24 * time.Hour

	case "team", "teamplus", "pro", "plus", "enterprise", "k12", "edu", "education":
		// Team/Pro/Plus 及教育版(k12/edu)有 5h + 7d 双窗口，需要判断是哪个窗口触发了限制
		return detectTeamCooldownWindow(resp)

	default:
		// 未知套餐，保守默认 5 小时
		return 5 * time.Hour
	}
}

func detectTeamCooldownWindow(resp *http.Response) time.Duration {
	if resp == nil {
		return 5 * time.Hour // 保守默认
	}

	// Codex 返回两组窗口头：primary 和 secondary
	// x-codex-primary-window-minutes / x-codex-primary-used-percent
	// x-codex-secondary-window-minutes / x-codex-secondary-used-percent
	// 用量 >= 100% 的窗口就是触发限制的窗口

	primaryUsed := parseFloat(resp.Header.Get("x-codex-primary-used-percent"))
	primaryWindowMin := parseFloat(resp.Header.Get("x-codex-primary-window-minutes"))
	secondaryUsed := parseFloat(resp.Header.Get("x-codex-secondary-used-percent"))
	secondaryWindowMin := parseFloat(resp.Header.Get("x-codex-secondary-window-minutes"))

	// 找到 used >= 100% 的窗口
	primaryExhausted := primaryUsed >= 100
	secondaryExhausted := secondaryUsed >= 100

	switch {
	case primaryExhausted && secondaryExhausted:
		// 两个窗口都满了，取较大窗口的冷却时间
		return windowMinutesToCooldown(max(primaryWindowMin, secondaryWindowMin))
	case primaryExhausted:
		return windowMinutesToCooldown(primaryWindowMin)
	case secondaryExhausted:
		return windowMinutesToCooldown(secondaryWindowMin)
	default:
		// 都没满但还是 429，可能是短时 burst 限制
		return 5 * time.Hour
	}
}

var codexUnsupportedModelRe = regexp.MustCompile(`(?i)the '([^']+)' model is not supported`)
