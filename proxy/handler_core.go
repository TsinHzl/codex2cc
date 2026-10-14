package proxy

import (

	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"image"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/database"
	"github.com/codex2cc/security"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func upstreamErrorConsoleBody(body []byte) string {
	if len(bytes.TrimSpace(body)) > 0 && !gjson.ValidBytes(body) {
		return fmt.Sprintf("[non-JSON upstream body omitted; %d bytes]", len(body))
	}
	truncated := false
	if len(body) > consoleUpstreamErrorLogMaxBytes {
		body = body[:consoleUpstreamErrorLogMaxBytes]
		truncated = true
	}
	bodyStr := security.SafeTruncate(security.SanitizeLog(string(body)), consoleUpstreamErrorLogMaxBytes)
	if truncated {
		bodyStr += " ... [truncated]"
	}
	return bodyStr
}

// Handler API 路由处理器

func dispatchPolicyForModel(model string) auth.DispatchPolicy {
	if isProOnlyModel(model) {
		return auth.DispatchPolicySpark
	}
	return auth.DispatchPolicyStandard
}

type CodexUsageSyncResult struct {
	UsagePct7d               float64
	HasUsage7d               bool
	Usage7dRateLimited       bool
	UsagePct5h               float64
	Reset5hAt                time.Time
	HasUsage5h               bool
	Used5hHeaders            bool
	Persisted5hOnly          bool
	Premium5hRateLimited     bool
	UsageWindowLimitsIgnored bool
	// Cleared5h 表示本次同步因上游未返回 5h 窗口而清除了本地陈旧 5h 快照（issue #382）。
	Cleared5h bool
	// CreditsObserved 表示本次同步观察到了上游 credits 相关响应头。
	CreditsObserved bool
	// RateLimitReachedType 记录上游返回的 rate_limit_reached_type。
	RateLimitReachedType string
}

type codex429Decision struct {
	Scope    string
	Reason   string
	Model    string
	ResetAt  time.Time
	Cooldown time.Duration
}

func requestAPIKeyID(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	if value, exists := c.Get(contextAPIKeyID); exists && value != nil {
		switch typed := value.(type) {
		case int64:
			return typed
		case int:
			return int64(typed)
		}
	}
	return 0
}

func sessionAffinityKey(sessionID string, apiKeyID int64) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || apiKeyID <= 0 {
		return sessionID
	}
	return fmt.Sprintf("%s::api-key:%d", sessionID, apiKeyID)
}

const codexTurnStateHeader = "X-Codex-Turn-State"

// codexTurnContinuationToken follows the official Codex per-turn contract:
// HTTP sends the token as a header, while Responses WebSocket v2 sends it in
// response.create client_metadata after the first request in that turn.

func codexWSTurnContinuationToken(body []byte) string {
	return codexTurnContinuationToken(nil, body)
}

// applyAffinityGroupRouting keeps fingerprinted requests on the API key's original groups
// and routes requests without either a Codex engine fingerprint or the dedicated local
// affinity header to the configured split groups.
//
// 当 Key 没配「允许账号分组」（= 不限分组）时，带指纹的请求改为「除分流组以外的全部账号」：
// 否则分流组既服务无指纹请求、又照常接真 Codex 流量，隔离等于没做——而不限分组恰恰是
// 绝大多数 Key 的默认配置。

func applyAffinityGroupRouting(c *gin.Context, identity requestSessionIdentity, filter auth.AccountFilter) auth.AccountFilter {
	row := apiKeyRowFromContext(c)
	if row == nil || len(row.Limits.NoAffinityGroupIDs) == 0 {
		return filter
	}

	splitGroups := int64GroupSet(row.Limits.NoAffinityGroupIDs)
	if len(splitGroups) == 0 {
		return filter
	}

	if !identity.hasRequestFingerprint {
		return groupMembershipFilter(splitGroups, true, filter)
	}

	allowedGroups := int64GroupSet(row.AllowedGroupIDs)
	if len(allowedGroups) == 0 {
		// 不限分组：把分流组排除掉，其余（含未分组账号）照常可用。
		return groupMembershipFilter(splitGroups, false, filter)
	}
	return groupMembershipFilter(allowedGroups, true, filter)
}

func isProOnlyModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), proOnlySparkModel)
}

func isSparkPlanCandidate(planType string) bool {
	switch auth.NormalizePlanType(planType) {
	case "free", "api":
		return false
	default:
		return true
	}
}

func accountFilterForModel(model string) auth.AccountFilter {
	model = strings.TrimSpace(model)
	return func(account *auth.Account) bool {
		if account == nil {
			return false
		}
		if account.IsRelayStyle() {
			return false
		}
		if model != "" && account.IsModelRateLimited(model) {
			return false
		}
		if !account.SupportsCodexModel(model) {
			return false
		}
		if isProOnlyModel(model) {
			return isSparkPlanCandidate(account.GetPlanType())
		}
		return true
	}
}

// requestUpstreamChannel 返回当前请求下游 Key 的上游渠道限定（空=不限）。

func requestUpstreamChannel(c *gin.Context) string {
	row := apiKeyRowFromContext(c)
	if row == nil {
		return ""
	}
	return row.Limits.ResolveUpstreamChannel()
}

// applyUpstreamChannelFilter 按下游 Key 的上游渠道限定收窄既有过滤器。

// claudeChannelAccountFilter 是 /v1/messages 的账号准入过滤器。codex2cc 的上游
// 只有 Codex OAuth 与 relay（openai_responses 中转）两类账号：
//   - relay 账号按其 openai_responses 模型白名单/映射判定，并叠加模型级冷却门；
//   - Codex OAuth 账号按 Codex 模型白名单判定，同样叠加模型级冷却门。
func claudeChannelAccountFilter(model string) auth.AccountFilter {
	model = strings.TrimSpace(model)
	codexFilter := accountFilterForModel(model)
	return func(account *auth.Account) bool {
		if account == nil {
			return false
		}
		if account.IsRelayStyle() {
			routedModel := model
			if mappedModel, ok := resolveAccountModelMappingForCandidates(account, model); ok && mappedModel != "" {
				routedModel = mappedModel
			}
			supported := relayAccountSupportsModel(account, routedModel)
			rateLimited := routedModel != "" && account.IsModelRateLimited(routedModel)
			return supported && !rateLimited
		}
		return codexFilter(account)
	}
}

// excludeClaudeAccountsFilter fences the native-Messages-only Claude provider
// from OpenAI Responses and Chat Completions routes.

func accountFilterForResponsesModel(model string, allowCodexAccounts bool) auth.AccountFilter {
	return accountFilterForResponsesModelWithOriginal(model, model, allowCodexAccounts)
}

func relayAccountSupportsModel(account *auth.Account, model string) bool {
	if account == nil {
		return false
	}
	// Claude Code OAuth 账号服务 claude-* 模型；显式 Models 白名单优先收窄。
	// 该分支对所有非 claude 账号恒不进入，保持既有准入行为不变。
	if account.SupportsOpenAIResponsesModel(model) {
		return true
	}
	return false
}

func modelIDInList(model string, models []string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, candidate := range models {
		if strings.EqualFold(strings.TrimSpace(candidate), model) {
			return true
		}
	}
	return false
}

func effectiveRequestModel(body []byte, fallback string) string {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model != "" {
		return model
	}
	return strings.TrimSpace(fallback)
}

func noAvailableAccountMessage(model string) string {
	if isProOnlyModel(model) {
		return "无可用付费或未知套餐账号，gpt-5.3-codex-spark 已排除明确 free/api 账号"
	}
	return "无可用账号，请稍后重试"
}

func usageLogErrorMessage(statusCode int, body []byte) string {
	return usageLogErrorMessageImpl(statusCode, body, false)
}

// usageLogFailureMessage 记录网关自产的失败诊断（传输错误、断流原因、重试上下文等）。
// 与上游任意响应体不同，这些文本由网关代码拼装，脱敏截断后保留原文；否则断流类
// 错误在用量页只剩裸状态码，根因全靠翻容器日志（issue #524）。仍先尝试 JSON 提取，
// 因为部分诊断原样包含上游错误帧。

func usageLogFailureMessage(statusCode int, message string) string {
	return usageLogErrorMessageImpl(statusCode, []byte(message), true)
}

func usageLogErrorMessageImpl(statusCode int, body []byte, trustedText bool) string {
	if statusCode < 400 {
		return ""
	}

	candidates := []string{
		gjson.GetBytes(body, "error.message").String(),
		gjson.GetBytes(body, "response.error.message").String(),
		gjson.GetBytes(body, "response.status_details.error.message").String(),
		gjson.GetBytes(body, "message").String(),
	}
	message := ""
	for _, candidate := range candidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			message = candidate
			break
		}
	}
	if message == "" {
		// 部分上游错误体把说明放在顶层字符串 error 字段:
		// {"code":"invalid-argument","error":"..."}。仅在 error 是字符串时采用,
		// 避免把对象形态的整段 JSON 打进 message。
		if errField := gjson.GetBytes(body, "error"); errField.Type == gjson.String {
			message = strings.TrimSpace(errField.String())
		}
	}

	codeCandidates := []string{
		gjson.GetBytes(body, "error.code").String(),
		gjson.GetBytes(body, "response.error.code").String(),
		gjson.GetBytes(body, "response.status_details.error.code").String(),
		gjson.GetBytes(body, "detail.code").String(),
		gjson.GetBytes(body, "code").String(),
	}
	code := ""
	for _, candidate := range codeCandidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			code = candidate
			break
		}
	}

	typeCandidates := []string{
		gjson.GetBytes(body, "error.type").String(),
		gjson.GetBytes(body, "response.error.type").String(),
		gjson.GetBytes(body, "response.status_details.error.type").String(),
		gjson.GetBytes(body, "type").String(),
	}
	errType := ""
	for _, candidate := range typeCandidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" && candidate != "error" {
			errType = candidate
			break
		}
	}

	if message == "" {
		// HTML and plain-text provider pages routinely contain request IDs,
		// internal routing details or echoed credentials. They are not an API
		// contract, so persist only the transport status instead of the body —
		// unless the caller marked the text as gateway-generated (trustedText).
		if !trustedText {
			return fmt.Sprintf("HTTP %d", statusCode)
		}
		raw := strings.TrimSpace(string(body))
		if raw == "" {
			return fmt.Sprintf("HTTP %d", statusCode)
		}
		message = raw
	}

	parts := make([]string, 0, 3)
	if code != "" {
		parts = append(parts, code)
	}
	if errType != "" && errType != code {
		parts = append(parts, errType)
	}
	parts = append(parts, message)
	return security.SafeTruncate(security.SanitizeLog(strings.Join(parts, " · ")), 600)
}

func noAvailableAnthropicAccountMessage(model string) string {
	if isProOnlyModel(model) {
		return "No available paid or unknown-plan account for gpt-5.3-codex-spark"
	}
	return "No available accounts, please retry later"
}

// NewHandler 创建处理器

func rawRequestBodyFromContext(c *gin.Context) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	v, exists := c.Get("raw_body")
	if !exists || v == nil {
		return nil, false
	}
	switch body := v.(type) {
	case []byte:
		if len(body) == 0 {
			return nil, false
		}
		return body, true
	case string:
		if body == "" {
			return nil, false
		}
		return []byte(body), true
	default:
		return nil, false
	}
}

func readRawRequestBody(c *gin.Context) ([]byte, error) {
	if body, ok := rawRequestBodyFromContext(c); ok {
		return body, nil
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	setRawRequestBody(c, body)
	return body, nil
}

func ingressRequestBody(c *gin.Context, fallback []byte) []byte {
	if c != nil {
		if value, exists := c.Get(ingressRequestBodyContextKey); exists {
			if body, ok := value.([]byte); ok {
				return body
			}
		}
	}
	return fallback
}

func requestBodyCompactionMeta(body []byte) requestCompactionMeta {
	meta := requestCompactionMeta{}
	input := gjson.GetBytes(body, "input")
	inspect := func(item gjson.Result) {
		switch {
		case gjsonResultIsCompactionTrigger(item):
			meta.ProtocolTriggered = true
		case gjsonResultIsCompactionHistory(item):
			meta.HasHistory = true
		}
	}
	if input.Exists() {
		if input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				inspect(item)
				return true
			})
		} else {
			inspect(input)
		}
	}

	meta.UsageTriggered = false
	clientMetadata := gjson.GetBytes(body, "client_metadata")
	if clientMetadata.IsObject() {
		turnMetadata := clientMetadata.Get("x-codex-turn-metadata")
		if turnMetadata.Type == gjson.String && turnMetadataIndicatesCompaction(turnMetadata.String()) {
			meta.UsageTriggered = true
		}
	}
	return meta
}

func turnMetadataIndicatesCompaction(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return false
	}
	root := gjson.Parse(raw)
	if !root.IsObject() {
		return false
	}
	requestKind := root.Get("request_kind")
	return requestKind.Type == gjson.String &&
		strings.EqualFold(strings.TrimSpace(requestKind.String()), "compaction")
}

// requestBodyHasCompactionTrigger reports only the protocol-level control used
// for routing. Metadata-only local compaction turns must remain on /responses.

func responseCachePreparationFailure(prepared responsesBodyPreparation) (status int, reason string, unavailable bool) {
	if prepared.PreviousResponseID == "" || prepared.Bypassed || prepared.CacheLookup.Kind == responseCacheLookupHit {
		return 0, "", false
	}
	switch prepared.CacheLookup.Kind {
	case responseCacheLookupKnownEvicted:
		return http.StatusConflict, "local_context_evicted", true
	case responseCacheLookupKnownOversize:
		return http.StatusConflict, "local_context_oversize", true
	case responseCacheLookupReconstructionTooLarge:
		return http.StatusConflict, "reconstruction_too_large", true
	case responseCacheLookupBackendCorrupt:
		return http.StatusConflict, "backend_value_corrupt", true
	case responseCacheLookupBackendError:
		if prepared.RequiresLocalContext {
			return http.StatusServiceUnavailable, "backend_unavailable", true
		}
	case responseCacheLookupMiss, responseCacheLookupExpired:
		if prepared.RequiresLocalContext {
			return http.StatusConflict, "missing_required_call_context", true
		}
	}
	return 0, "", false
}

func extractReasoningEffort(body []byte) string {
	// Responses API: reasoning.effort
	if effort := gjson.GetBytes(body, "reasoning.effort").String(); effort != "" {
		return effort
	}
	// Chat Completions API: reasoning_effort
	if effort := gjson.GetBytes(body, "reasoning_effort").String(); effort != "" {
		return effort
	}
	return ""
}

// responsesPhaseTimingHeader /v1/responses 请求准备阶段分段耗时的响应头。
// 首个 attempt 开始前写入(SSE 首字节尚未发出),下游网关可据此把
// "网关侧首字慢 vs codex2cc first_token_ms 快"的差值归因到具体阶段(issue #405)。

func extractServiceTier(body []byte) string {
	if tier := gjson.GetBytes(body, "service_tier").String(); tier != "" {
		return tier
	}
	return gjson.GetBytes(body, "serviceTier").String()
}

const upstreamErrorKindMessageTooBig = "message_too_big"

// upstreamErrorKindWsBusyAcquire 是 wsrelay busy session acquire 超时（issue #413）：
// 同会话的前一个请求长时间占用 WS 连接导致的排队超时，属会话占用而非账号故障。

func shouldPenalizeTransportKind(kind string) bool {
	return kind != "" && kind != upstreamErrorKindWsBusyAcquire
}

func websocketMessageTooBigSource(message string) string {
	if strings.Contains(strings.ToLower(message), "read limit exceeded") {
		return "local_read_limit"
	}
	return "peer_close"
}

func isWebsocketMessageTooBigOutcome(outcome streamOutcome) bool {
	return outcome.failureKind == upstreamErrorKindMessageTooBig
}

func shouldFallbackWebsocketMessageTooBigToHTTP(outcome streamOutcome, useWebsocket bool, wroteAnyBody bool, ctxErr, writeErr error) bool {
	if !useWebsocket || !isWebsocketMessageTooBigOutcome(outcome) {
		return false
	}
	if wroteAnyBody || ctxErr != nil || writeErr != nil {
		return false
	}
	return true
}

func classifyTransportFailure(err error) string {
	if err == nil {
		return ""
	}

	// Structured proxy errors already carry their retry semantics. Treating
	// every non-nil error as a transport failure turns deterministic adapter
	// errors (for example an invalid Responses feature) into account failures,
	// retries them against the whole pool, and can leave the caller with a
	// misleading "no available account" 503. Only unwrap errors that actually
	// describe an upstream transport failure; ordinary structured 4xx errors
	// are not transport incidents.
	var apiErr *Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case ErrorCodeUpstreamTimeout:
			return "timeout"
		case ErrorCodeUpstreamStreamBreak:
			return "transport"
		}
		// Executors wrap http.Client.Do failures as ErrUpstream(0, ..., cause).
		// Those stay outside the legacy Retryable status set, but the cause is
		// still a transport incident and must keep sticky/same-account retry.
		if apiErr.Code == ErrorCodeUpstreamError && apiErr.HTTPStatus == 0 && apiErr.Cause != nil {
			return classifyTransportFailure(apiErr.Cause)
		}
		if !apiErr.Retryable || (apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500) {
			return ""
		}
		if apiErr.Cause != nil {
			return classifyTransportFailure(apiErr.Cause)
		}
		return ""
	}

	if isWebsocketMessageTooBigError(err) {
		return upstreamErrorKindMessageTooBig
	}
	if isWsBusyAcquireTimeoutError(err) {
		return upstreamErrorKindWsBusyAcquire
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline exceeded") {
		return "timeout"
	}
	return "transport"
}

func classifyHTTPFailure(statusCode int) string {
	switch {
	case statusCode == http.StatusUnauthorized:
		return "unauthorized"
	case statusCode == http.StatusTooManyRequests:
		return "" // 429 由 applyCooldown 单独处理
	case statusCode >= 500:
		return "server"
	case statusCode >= 400:
		return "client"
	default:
		return ""
	}
}

type streamOutcome struct {
	logStatusCode  int
	failureKind    string
	failureMessage string
	failurePayload []byte // 原始 response.failed/error 帧，仅用于策略匹配，不向日志直接回显
	penalize       bool
	// capacityShed 标记这是一次上游容量降载（server_is_overloaded / slow_down）。
	// penalize 仍为 true 以复用首包前帧缓冲/透明重试机制，但账号连击/健康度不因此
	// 受罚，且透明重试优先在同账号退避几次而非立即换号（见 reportStreamOutcomeFailure）。
	capacityShed bool
	// verifyAccountAuth 标记这是一次 WS 上游读流失败（如 close 1008 policy violation）。
	// WS 通道下 token 失效表现为上游主动关闭而非 401，需异步跑一次探针确认账号鉴权状态，
	// 命中 401 才按 unauthorized 冷却，避免失效账号不被封、反复被调度。
	verifyAccountAuth bool
	// requestScoped 标记故障由本次请求内容触发（如零输出的 response.incomplete）：
	// penalize 保持 true 以复用首包前透明重试/换号，但不计入账号健康度。
	requestScoped bool
	// terminalLocal marks proxy replay/storage failures; they never affect
	// upstream retry or account health and must exit as protocol terminal errors.
	// terminalLocal 标记代理自身的回放/存储失败；它们不参与上游重试或账号健康度，
	// 并且必须以协议终态错误结束。
	terminalLocal bool
}

const continuousRetryLocalFailureMessage = "Internal proxy replay failure"

// isContinuousRetryLocalFailure recognizes only replay-owned failures, keeping
// sentinel identity stable while filesystem details remain private.
// isContinuousRetryLocalFailure 只识别回放层自身失败；sentinel 身份保持稳定，
// 文件系统细节不会进入公开协议错误。

func isContinuousRetryLocalFailure(err error) bool {
	if err == nil {
		return false
	}
	for _, sentinel := range []error{
		errContinuousRetryReplayClosed,
		errContinuousRetryReplayLimitExceeded,
		errContinuousRetryReplayStorage,
		errContinuousRetryWSReplayInvalid,
		errContinuousRetryWSMessageTooLarge,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

func overlayContinuousRetryLocalFailure(outcome streamOutcome, errs ...error) streamOutcome {
	if outcome.terminalLocal || strings.EqualFold(strings.TrimSpace(outcome.failureKind), "continuous_retry_timeout") {
		return outcome
	}
	for _, err := range errs {
		if !isContinuousRetryLocalFailure(err) {
			continue
		}
		return streamOutcome{
			logStatusCode:  http.StatusInternalServerError,
			failureKind:    "local",
			failureMessage: continuousRetryLocalFailureMessage,
			penalize:       false,
			terminalLocal:  true,
		}
	}
	return outcome
}

// isWebsocketUpstreamClose 判断读流错误是否来自 WS 上游异常关闭/读失败。
// wsrelay 的读错误统一以 "websocket read error:" 前缀包裹（见 wsrelay/executor.go）。

func classifyStreamOutcome(ctxErr, readErr, writeErr error, gotTerminal bool) streamOutcome {
	if errors.Is(ctxErr, errContinuousRetryDeadlineExceeded) {
		return streamOutcome{
			logStatusCode:  http.StatusGatewayTimeout,
			failureKind:    "continuous_retry_timeout",
			failureMessage: continuousRetryTimeoutMessage,
			penalize:       false,
		}
	}
	if local := overlayContinuousRetryLocalFailure(streamOutcome{}, readErr, writeErr); local.terminalLocal {
		return local
	}
	if gotTerminal {
		return streamOutcome{logStatusCode: http.StatusOK}
	}

	if ctxErr != nil || writeErr != nil {
		msg := "下游客户端提前断开"
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded):
			msg = "下游请求上下文超时"
		case writeErr != nil:
			msg = fmt.Sprintf("写回下游失败: %v", writeErr)
		case ctxErr != nil:
			msg = fmt.Sprintf("下游请求提前取消: %v", ctxErr)
		}
		return streamOutcome{
			logStatusCode:  logStatusClientClosed,
			failureMessage: msg,
		}
	}

	if readErr != nil {
		kind := classifyTransportFailure(readErr)
		if kind == "" {
			kind = "transport"
		}
		messageTooBig := kind == upstreamErrorKindMessageTooBig
		return streamOutcome{
			logStatusCode:     logStatusUpstreamStreamBreak,
			failureKind:       kind,
			failureMessage:    fmt.Sprintf("上游流读取失败: %v", readErr),
			penalize:          !messageTooBig,
			verifyAccountAuth: !messageTooBig && isWebsocketUpstreamClose(readErr),
		}
	}

	return streamOutcome{
		logStatusCode:  logStatusUpstreamStreamBreak,
		failureKind:    "transport",
		failureMessage: "上游流提前结束，未收到终止事件",
		penalize:       true,
	}
}

func continuousRetryBufferedAttemptCommitted(policy database.ContinuousRetryPolicy, outcome streamOutcome) bool {
	return continuousRetryBuffersAttempts(policy) && !outcome.terminalLocal && outcome.logStatusCode == http.StatusOK
}

// isPreviousResponseNotFoundBody 判定上游错误体是否为「previous_response_id
// 对该用户不可用」。下游 Responses WebSocket 通道已在 codex2cc 中剔除，
// 该判定仅保留供确定性 400 归类复用。
func isPreviousResponseNotFoundBody(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	// {"error":{...}} / {"response":{"error":{...}}} 归一化后取 error.*；
	// 裸 error 帧（{"type":"error","code":...,"message":...}）再看顶层字段。
	body := responseFailedErrorBody(payload)
	for _, path := range []string{"error.code", "code"} {
		if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, path).String()), "previous_response_not_found") {
			return true
		}
	}
	for _, path := range []string{"error.message", "message"} {
		message := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, path).String()))
		if message == "previous_response_id is not available for this user" ||
			strings.Contains(message, "previous response with id") {
			return true
		}
	}
	return false
}

func responseFailedErrorBody(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	if gjson.GetBytes(payload, "error").Exists() {
		return payload
	}
	for _, path := range []string{
		"response.error",
		"response.status_details.error",
	} {
		result := gjson.GetBytes(payload, path)
		raw := strings.TrimSpace(result.Raw)
		if raw == "" || raw == "null" {
			continue
		}
		return []byte(`{"error":` + raw + `}`)
	}
	return payload
}

// responseFailedRetryable 判断一个 response.failed 终止事件是否属于"换号重试有意义"的上游故障
// （额度耗尽/限流/5xx/401）。用于在首包前透明换号，避免把可恢复的失败帧直接下发给
// WebSocket 客户端而触发反复 Reconnecting。非可重试故障（如 invalid_request）仍照常透传。

func isRetryableUpstreamErrorFrame(eventType string, payload []byte, policies ...database.ContinuousRetryPolicy) bool {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		eventType = normalizedUpstreamSSEEventType("", payload)
	}
	if eventType != "error" {
		return false
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return false
	}
	// A continuous policy may deliberately select deterministic upstream error
	// frames (for example an account-specific 403/404). Keep those frames
	// buffered before the first token as well, otherwise the leading `error`
	// event commits the downstream response before the following
	// `response.failed` event can be retried transparently.
	outcome := classifyResponseFailedOutcome(payload)
	return continuousRetryStreamFailureSelected(outcome, payload, eventType, policies...)
}

// resolvePreContentRetryErrorCandidate promotes a standalone upstream error
// only when the stream ends before it produced a real response. Providers
// commonly emit error -> response.failed, but some relays close immediately
// after the error frame. Keeping it as a candidate avoids leaking a selected
// error before a later response.completed while still allowing the EOF case to
// enter the normal account-rotation retry path.

func resolvePreContentRetryErrorCandidate(terminalFailurePayload, candidate []byte, contentSeen, wroteAnyBody, gotTerminal bool, _ error, ctxErr, writeErr error) ([]byte, bool) {
	if len(terminalFailurePayload) > 0 || len(candidate) == 0 || contentSeen || wroteAnyBody || gotTerminal || ctxErr != nil || writeErr != nil {
		return terminalFailurePayload, false
	}
	return append([]byte(nil), candidate...), true
}

// capacityShedRetryableClientCode 是把上游容量降载错误透传给下游时改写使用的
// 错误码。Codex CLI 按闭集分类：server_is_overloaded / slow_down 被判致命
// （提示 "Selected model is at capacity. Please try a different model." 并终止
// 会话），而 server_error 等闭集之外的错误码会进入客户端内置的退避重试。

func sanitizeCapacityShedEventForClient(eventType string, payload []byte) []byte {
	switch strings.TrimSpace(eventType) {
	case "error", "response.failed":
	default:
		return payload
	}
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	updated := payload
	for _, path := range []string{"error.code", "response.error.code", "response.status_details.error.code"} {
		switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(updated, path).String())) {
		case "server_is_overloaded", "slow_down":
		default:
			continue
		}
		next, err := sjson.SetBytes(updated, path, capacityShedRetryableClientCode)
		if err != nil {
			return payload
		}
		updated = next
	}
	return updated
}

func applyResponseFailedDecisionKind(outcome streamOutcome, payload []byte, decision codex429Decision) streamOutcome {
	body := responseFailedErrorBody(payload)
	if isPermanentQuotaFailure(body) {
		outcome.failureKind = "usage_limit"
		return outcome
	}
	if decision.Reason != "" {
		outcome.failureKind = upstreamErrorKind(outcome.logStatusCode, body, decision)
	}
	return outcome
}

func responseFailedStatusCodeWithEvidence(payload []byte) (int, bool) {
	for _, path := range []string{
		"response.status_code",
		"response.error.status_code",
		"response.status_details.error.status_code",
		"status_code",
		"error.status_code",
	} {
		code := int(gjson.GetBytes(payload, path).Int())
		if code >= 400 && code <= 599 {
			return code, true
		}
	}

	codeOrType := strings.ToLower(strings.Join([]string{
		gjson.GetBytes(payload, "response.error.code").String(),
		gjson.GetBytes(payload, "response.error.type").String(),
		gjson.GetBytes(payload, "response.status_details.error.code").String(),
		gjson.GetBytes(payload, "response.status_details.error.type").String(),
		gjson.GetBytes(payload, "error.code").String(),
		gjson.GetBytes(payload, "error.type").String(),
		gjson.GetBytes(payload, "code").String(),
		gjson.GetBytes(payload, "type").String(),
	}, " "))
	switch {
	case strings.Contains(codeOrType, "usage_limit"):
		return http.StatusTooManyRequests, true
	case strings.Contains(codeOrType, "rate_limit"):
		return http.StatusTooManyRequests, true
	case strings.Contains(codeOrType, "unauthorized") || strings.Contains(codeOrType, "authentication") || strings.Contains(codeOrType, "invalid_api_key") || strings.Contains(codeOrType, "invalid_token"):
		return http.StatusUnauthorized, true
	case strings.Contains(codeOrType, "payment"):
		return http.StatusPaymentRequired, true
	case strings.Contains(codeOrType, "forbidden") || strings.Contains(codeOrType, "permission"):
		return http.StatusForbidden, true
	case strings.Contains(codeOrType, "previous_response_not_found") || isPreviousResponseNotFoundBody(payload):
		return http.StatusBadRequest, true
	// 确定性客户端错误：输入超上下文窗口/字段超长/模型不存在等，换号重试
	// 也必然失败。归为 400，避免落入 default 500 触发透明重试并惩罚账号
	// 健康度 (issue #310)。
	//
	// code/type 缺失时还要看 message：中转上游常把超窗回成
	// {"code":null,"type":"upstream_error","message":"Your input exceeds the
	// context window..."}，只匹配 code/type 会落进 default 500，于是网关把整个
	// 号池挨个试一遍（换号必然一样失败）并给每个健康账号记一笔故障。
	// 复用超窗压缩那套只查固定 error 字段的判定，避免全文扫命中回显的请求内容。
	case strings.Contains(codeOrType, "context_length") ||
		strings.Contains(codeOrType, "context_window") ||
		strings.Contains(codeOrType, "above_max_length") ||
		strings.Contains(codeOrType, "model_not_found") ||
		strings.Contains(codeOrType, "unsupported") ||
		isContextLengthExceededBody(responseFailedErrorBody(payload)):
		return http.StatusBadRequest, true
	case strings.Contains(codeOrType, "invalid") || strings.Contains(codeOrType, "bad_request"):
		return http.StatusBadRequest, true
	case strings.Contains(codeOrType, "server_error") ||
		strings.Contains(codeOrType, "internal_server") ||
		strings.Contains(codeOrType, "server_is_overloaded") ||
		strings.Contains(codeOrType, "service_unavailable") ||
		strings.Contains(codeOrType, "temporarily_unavailable") ||
		strings.Contains(codeOrType, "bad_gateway") ||
		strings.Contains(codeOrType, "gateway_timeout"):
		return http.StatusInternalServerError, true
	default:
		return 0, false
	}
}

func streamOutcomeUsesRateLimitBudget(outcome streamOutcome) bool {
	failureKind := strings.ToLower(strings.TrimSpace(outcome.failureKind))
	return outcome.logStatusCode == http.StatusTooManyRequests ||
		strings.Contains(failureKind, "rate_limit") ||
		strings.Contains(failureKind, "usage_limit")
}

func retryLimitForStreamOutcome(outcome streamOutcome, generalLimit, rateLimit int, policies ...database.ContinuousRetryPolicy) int {
	generalLimit, rateLimit = continuousRetryLimitsForStream(outcome, outcome.failurePayload, "", generalLimit, rateLimit, policies...)
	if streamOutcomeUsesRateLimitBudget(outcome) {
		return rateLimit
	}
	return generalLimit
}

func retryStateForStreamOutcome(outcome streamOutcome, generalRetries, rateLimitRetries, maxGeneralRetries, maxRateLimitRetries int, policies ...database.ContinuousRetryPolicy) (int, int) {
	return retryStateForStreamEvent(outcome, "", generalRetries, rateLimitRetries, maxGeneralRetries, maxRateLimitRetries, policies...)
}

func retryStateForStreamEvent(outcome streamOutcome, eventType string, generalRetries, rateLimitRetries, maxGeneralRetries, maxRateLimitRetries int, policies ...database.ContinuousRetryPolicy) (int, int) {
	maxGeneralRetries, maxRateLimitRetries = continuousRetryLimitsForStream(outcome, outcome.failurePayload, eventType, maxGeneralRetries, maxRateLimitRetries, policies...)
	if streamOutcomeUsesRateLimitBudget(outcome) {
		return rateLimitRetries, maxRateLimitRetries
	}
	return generalRetries, maxGeneralRetries
}

func shouldTransparentRetryStreamWithBudgets(outcome streamOutcome, generalRetries, rateLimitRetries *int, maxGeneralRetries, maxRateLimitRetries int, wroteAnyBody bool, ctxErr, writeErr error, policies ...database.ContinuousRetryPolicy) bool {
	return shouldTransparentRetryStreamEventWithBudgets(outcome, "", generalRetries, rateLimitRetries, maxGeneralRetries, maxRateLimitRetries, wroteAnyBody, ctxErr, writeErr, policies...)
}

func shouldTransparentRetryStreamEventWithBudgets(outcome streamOutcome, eventType string, generalRetries, rateLimitRetries *int, maxGeneralRetries, maxRateLimitRetries int, wroteAnyBody bool, ctxErr, writeErr error, policies ...database.ContinuousRetryPolicy) bool {
	// Native relay passthrough returns an already-classified client-closed
	// outcome instead of a separate writeErr. Never turn failed downstream
	// delivery into another upstream request, even in catch-all mode.
	if outcome.terminalLocal || wroteAnyBody || ctxErr != nil || writeErr != nil || outcome.logStatusCode == logStatusClientClosed {
		return false
	}
	if !continuousRetryStreamFailureSelected(outcome, outcome.failurePayload, eventType, policies...) {
		return false
	}
	maxGeneralRetries, maxRateLimitRetries = continuousRetryLimitsForStream(outcome, outcome.failurePayload, eventType, maxGeneralRetries, maxRateLimitRetries, policies...)
	retries := generalRetries
	limit := maxGeneralRetries
	if streamOutcomeUsesRateLimitBudget(outcome) {
		retries = rateLimitRetries
		limit = maxRateLimitRetries
	}
	if retries == nil || !retryBudgetAvailable(*retries, limit) {
		return false
	}
	*retries++
	return true
}

func shouldSuppressRetryableResponseFailedBeforeFirstTokenWithBudgets(eventType string, terminalFailurePayload []byte, ttftRecorded, wroteAnyBody bool, generalRetries, rateLimitRetries, maxGeneralRetries, maxRateLimitRetries int, ctxErr, writeErr error, policies ...database.ContinuousRetryPolicy) bool {
	if eventType != "response.failed" {
		return false
	}
	if ttftRecorded || wroteAnyBody || ctxErr != nil || writeErr != nil {
		return false
	}
	outcome := classifyResponseFailedOutcome(terminalFailurePayload)
	maxGeneralRetries, maxRateLimitRetries = continuousRetryLimitsForStream(outcome, terminalFailurePayload, eventType, maxGeneralRetries, maxRateLimitRetries, policies...)
	if !continuousRetryStreamFailureSelected(outcome, terminalFailurePayload, eventType, policies...) {
		return false
	}
	if streamOutcomeUsesRateLimitBudget(outcome) {
		return retryBudgetAvailable(rateLimitRetries, maxRateLimitRetries)
	}
	return retryBudgetAvailable(generalRetries, maxGeneralRetries)
}

// shouldReturnHTTPErrorForResponseFailed 判断:流式请求在首 token 之前收到
// response.failed(且尚未向下游写任何内容、客户端也未断开)时,应当中止 SSE 转发,
// 交由循环外按真实 HTTP 错误码返回,而不是把失败包装成 200 + [DONE]。
//
// 背景:pending 尚未 flush 时下游 HTTP 200 header 还没发出(见 stream_flush_writer.go),
// 此时若把 response.failed 写进流并补 [DONE],把本服务当上游的计费型中转层
// 会把它当成一次正常完成、按其本地预估的 input token 计费,
// 造成"上游拒绝(0 输出)却按 input 收费"。#310 已让 context_length_exceeded 等确定性
// 客户端错误不再换号重试,但流式下游返回仍是 200 + [DONE],本函数补上这一半。
//
// 注意:命中后除了中止转发,循环后的收尾 flush 也必须跳过(见 wroteAnyBody 守卫),
// 否则空 buffer 的 flusher.Flush 仍会提前提交 200 header,让循环外的 c.JSON(4xx) 失效。

func shouldReturnHTTPErrorForResponseFailed(eventType string, ttftRecorded, wroteAnyBody, clientGone bool) bool {
	return eventType == "response.failed" && !ttftRecorded && !wroteAnyBody && !clientGone
}

func extractResponseOutputItemDone(data []byte, seen map[string]struct{}) (json.RawMessage, bool) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return nil, false
	}
	if gjson.GetBytes(data, "type").String() != "response.output_item.done" {
		return nil, false
	}
	item := gjson.GetBytes(data, "item")
	if !item.Exists() || !item.IsObject() {
		return nil, false
	}
	key := responseOutputItemDoneKey(item)
	if key != "" && seen != nil {
		if _, ok := seen[key]; ok {
			return nil, false
		}
		seen[key] = struct{}{}
	}
	raw := []byte(item.Raw)
	if item.Get("type").String() == "image_generation_call" {
		var output map[string]any
		if err := json.Unmarshal(raw, &output); err == nil && addImageStatsToMap(output) {
			if annotated, err := json.Marshal(output); err == nil {
				raw = annotated
			}
		}
	}
	return json.RawMessage(raw), true
}

type responseOutputCollector struct {
	indexed   map[int]responseOutputItemRecord
	unindexed []responseOutputItemRecord
	seen      map[string]struct{}
	bytes     int64
	limit     int64
	sequence  int
	overflow  bool
}

func newResponseOutputCollector() *responseOutputCollector {
	limit := GetResponseCacheAppliedConfig().ReconstructMaxBytes
	if limit <= 0 {
		limit = responseCacheMaxBytes
	}
	return &responseOutputCollector{
		indexed: make(map[int]responseOutputItemRecord),
		seen:    make(map[string]struct{}),
		limit:   limit,
	}
}

func restoreMissingResponseOutputs(responseJSON []byte, outputItems []json.RawMessage) []byte {
	if len(responseJSON) == 0 || len(outputItems) == 0 {
		return responseJSON
	}
	terminalOutput := gjson.GetBytes(responseJSON, "output")
	terminalCount := int64(-1)
	if terminalOutput.IsArray() {
		terminalCount = terminalOutput.Get("#").Int()
	}
	if terminalCount >= int64(len(outputItems)) {
		return responseJSON
	}
	outputs := make([]json.RawMessage, 0, len(outputItems))
	for _, rawItem := range outputItems {
		if len(rawItem) == 0 || !gjson.ValidBytes(rawItem) {
			continue
		}
		outputs = append(outputs, rawItem)
	}
	if len(outputs) == 0 {
		return responseJSON
	}
	if terminalCount >= int64(len(outputs)) {
		return responseJSON
	}
	if firstNonSpace(responseJSON) != '{' || !gjson.ValidBytes(responseJSON) {
		return responseJSON
	}
	encoded, err := json.Marshal(outputs)
	if err != nil {
		return responseJSON
	}
	// Patch only output. Large usage/attribution trees stay opaque, preserving
	// unknown fields and exact JSON numbers while avoiding a full map round trip.
	restored, err := sjson.SetRawBytes(responseJSON, "output", encoded)
	if err != nil {
		return responseJSON
	}
	return restored
}

func isResponsesSuccessTerminalEvent(eventType string) bool {
	return eventType == "response.completed" || eventType == "response.incomplete"
}

// isResponsesTerminalEvent 覆盖 Responses 的全部终态（正常/截断/失败），
// 供 SSE 读取循环判定"读到这里就可以收工"。

func isResponsesTerminalEvent(eventType string) bool {
	return isResponsesSuccessTerminalEvent(eventType) || eventType == "response.failed"
}

// responsesIncompleteFinishReason 把 Responses 的截断原因映射成 Chat 的
// finish_reason；非截断终态返回空串表示"沿用推导值"。

func responsesIncompleteFinishReason(eventType, reason string) string {
	if eventType != "response.incomplete" {
		return ""
	}
	if reason == "content_filter" {
		return "content_filter"
	}
	return "length"
}

// isRetryableStatus 检查是否可重试的上游状态码。
// 403 也视为可重试：Codex 上游 403 全是账号侧问题（payment_required /
// deactivated_workspace / codex_access_restricted 等 OAuth/套餐/工作区维度），
// 非请求内容问题，换到号池里其他健康账号即可继续（issue #396）。
// 402 同理：deactivated_workspace（team 空间被封）等计费维度拒绝是纯账号侧
// 问题，applyCooldownForModel 已把该账号标错隔离，换号重试即可成功。

func shouldRetryHTTPStatus(statusCode int, body []byte, generalRetries *int, rateLimitRetries *int, maxGeneralRetries, maxRateLimitRetries int, policies ...database.ContinuousRetryPolicy) bool {
	policy := continuousRetryPolicyForCall(policies)
	if isExplicitUpstreamCyberPolicy(body) {
		return false
	}
	if isExplicitUpstreamSafetyPolicy(body) && !policy.CatchesAllUpstreamFailures() {
		return false
	}
	policySelected := continuousRetryHTTPSelected(policy, statusCode, body)
	maxGeneralRetries, maxRateLimitRetries = continuousRetryLimitsForHTTP(statusCode, body, maxGeneralRetries, maxRateLimitRetries, policy)
	if statusCode == http.StatusTooManyRequests {
		if rateLimitRetries == nil || !retryBudgetAvailable(*rateLimitRetries, maxRateLimitRetries) {
			return false
		}
		*rateLimitRetries++
		return true
	}
	// 400 一般是请求内容问题不重试；唯独"账号不支持该模型"是账号权益问题，
	// 该账号已被模型冷却排除，换号重试可成功（issue #408）。
	modelUnsupported := statusCode == http.StatusBadRequest && isCodexModelUnsupportedError(body)
	if !isRetryableStatus(statusCode) && !modelUnsupported && !policySelected {
		return false
	}
	if generalRetries == nil || !retryBudgetAvailable(*generalRetries, maxGeneralRetries) {
		return false
	}
	*generalRetries++
	return true
}

func shouldRetryRequestError(err error, generalRetries *int, maxGeneralRetries int, policies ...database.ContinuousRetryPolicy) bool {
	policy := continuousRetryPolicyForCall(policies)
	if isExplicitUpstreamCyberPolicyError(err) {
		return false
	}
	if _, body, ok := continuousRetryHTTPErrorDetails(err); ok && isExplicitUpstreamSafetyPolicy(body) && !policy.CatchesAllUpstreamFailures() {
		return false
	}
	selected := continuousRetryRequestErrorSelected(policy, err)
	maxGeneralRetries = continuousRetryLimitForRequestError(err, maxGeneralRetries, policy)
	if err == nil || generalRetries == nil || !retryBudgetAvailable(*generalRetries, maxGeneralRetries) {
		return false
	}
	if isRetryableRequestError(err) || selected {
		*generalRetries++
		return true
	}
	return false
}

// isRetryableRequestError gives an explicit non-retryable structured error
// precedence over the generic transport classifier. Executors use
// ErrUpstream(0, ..., cause) for failures from http.Client.Do; those remain
// retryable even though a status-less Error cannot set Retryable by status.

func isRetryableRequestErrorForContext(ctx context.Context, err error, policies ...database.ContinuousRetryPolicy) bool {
	if apiKeyModelRequestError(err) != nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	policy := continuousRetryPolicyForCall(policies)
	if isExplicitUpstreamCyberPolicyError(err) {
		return false
	}
	if _, body, ok := continuousRetryHTTPErrorDetails(err); ok && isExplicitUpstreamSafetyPolicy(body) && !policy.CatchesAllUpstreamFailures() {
		return false
	}
	if isRetryableRequestError(err) {
		return true
	}
	return continuousRetryRequestErrorSelected(policy, err)
}

func retryStateForHTTPStatusWithBody(statusCode int, body []byte, generalRetries, rateLimitRetries, generalLimit, rateLimit int, policies ...database.ContinuousRetryPolicy) (int, int) {
	generalLimit, rateLimit = continuousRetryLimitsForHTTP(statusCode, body, generalLimit, rateLimit, policies...)
	if statusCode == http.StatusTooManyRequests {
		return rateLimitRetries, retryLimitForHTTPStatus(statusCode, generalLimit, rateLimit)
	}
	return generalRetries, retryLimitForHTTPStatus(statusCode, generalLimit, rateLimit)
}

func retryAttemptProgress(attempt, limit int) string {
	if limit == -1 {
		return fmt.Sprintf("%d/unlimited", attempt+1)
	}
	return fmt.Sprintf("%d/%d", attempt+1, limit+1)
}

func upstreamErrorKind(statusCode int, body []byte, decision codex429Decision) string {
	if IsUsageLimitReachedError(body) {
		if decision.Reason != "" {
			return decision.Reason
		}
		return "usage_limit"
	}
	// Relays sometimes expose permanent billing/quota markers only in the
	// structured message field. A model-scoped cooldown decision must not turn
	// those account-level failures back into a transient rate-limit cycle.
	if isPermanentQuotaFailure(body) {
		return "usage_limit"
	}
	switch statusCode {
	case http.StatusTooManyRequests:
		if decision.Reason != "" {
			return decision.Reason
		}
		return "rate_limited"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusPaymentRequired:
		if IsDeactivatedWorkspaceError(body) {
			return "deactivated_workspace"
		}
		if decision.Reason != "" {
			return decision.Reason
		}
		return "payment_required_unknown"
	case http.StatusForbidden:
		if IsAgentRuntimeDeletedError(body) {
			return "agent_runtime_deleted"
		}
		if IsDeactivatedWorkspaceError(body) {
			return "deactivated_workspace"
		}
		return "forbidden"
	case http.StatusUpgradeRequired:
		return "version_required"
	case http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout:
		return "server"
	default:
		if statusCode >= 400 {
			return "client"
		}
		return ""
	}
}

func IsUsageLimitReachedError(body []byte) bool {
	return strings.EqualFold(firstGJSONString(body, "error.type", "response.error.type", "response.status_details.error.type"), "usage_limit_reached")
}

func firstGJSONString(body []byte, paths ...string) string {
	for _, path := range paths {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
			return value
		}
	}
	return ""
}

func isMissingScopeUnauthorized(body []byte) bool {
	if len(body) == 0 {
		return false
	}

	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	if code != "missing_scope" {
		return false
	}

	msg := strings.ToLower(gjson.GetBytes(body, "error.message").String())
	if strings.Contains(msg, "api.responses.write") {
		return true
	}

	return strings.Contains(msg, "scope")
}

func SyncCodexUsageState(store *auth.Store, account *auth.Account, resp *http.Response) CodexUsageSyncResult {
	result := CodexUsageSyncResult{}
	if account == nil || resp == nil {
		return result
	}
	observedAt := time.Now()
	if store != nil {
		planHeader := resp.Header.Get("x-codex-plan-type")
		store.UpdateAccountPlanType(account, planHeader)
		// 权威付费 plan_type 与「订阅已过期」互斥，借每次响应校正陈旧到期时间。(issue #360)
		if planHeader != "" {
			store.ClearStaleSubscriptionExpiresAt(account)
		}
	}

	observation := parseCodexUsageHeaderObservation(resp)
	rateLimitReachedType := strings.TrimSpace(resp.Header.Get("x-codex-rate-limit-reached-type"))
	result.RateLimitReachedType = rateLimitReachedType
	if hasCredits, unlimited, ok := parseCodexCreditsFlagHeaders(resp); ok {
		result.CreditsObserved = true
		var balPtr *string
		if trimmed := strings.TrimSpace(resp.Header.Get("x-codex-credits-balance")); trimmed != "" {
			balPtr = &trimmed
		}
		var overagePtr *bool
		if raw := strings.TrimSpace(resp.Header.Get("x-codex-credits-overage-reached")); raw != "" {
			if ov, err := strconv.ParseBool(raw); err == nil {
				overagePtr = &ov
			}
		}
		if store != nil {
			store.PersistSparseCreditObservation(account, hasCredits, unlimited, balPtr, overagePtr, rateLimitReachedType)
		} else {
			account.ApplySparseCreditsHeaders(hasCredits, unlimited, balPtr, overagePtr, rateLimitReachedType)
		}
	} else if rateLimitReachedType != "" || observation.authoritative {
		// reached-type 是逐响应字段：带用量头的响应没有它就是「未触达」，要把旧的
		// workspace hard-stop 清掉，否则工作区充值后积分顶替永远回不来。
		if store != nil {
			store.PersistRateLimitReachedType(account, rateLimitReachedType)
		} else {
			account.SetRateLimitReachedType(rateLimitReachedType)
		}
	}

	result.UsageWindowLimitsIgnored = account.SkipsUsageWindowLimits()

	result.Used5hHeaders = observation.w5h.valid
	usageApplied := false
	if observation.authoritative {
		usageApplied = account.ApplyUsageObservation(observedAt, func() {
			parsed := applyCodexUsageHeaderObservation(store, account, observation, observedAt)
			result.UsagePct7d = parsed.usagePct7d
			result.HasUsage7d = parsed.hasUsage7d
			result.Cleared5h = parsed.cleared5h
			if store == nil {
				return
			}
			if result.HasUsage7d {
				store.PersistUsageSnapshot(account, result.UsagePct7d)
				if result.UsagePct7d >= 100 {
					result.Usage7dRateLimited = store.MarkUsage7dRateLimited(account)
				}
			} else if result.Used5hHeaders {
				store.PersistUsageSnapshot5hOnly(account)
				result.Persisted5hOnly = true
			}
		})
	}

	result.UsagePct5h, result.Reset5hAt, result.HasUsage5h = account.GetUsageSnapshot5h()
	if usageApplied && store != nil && result.HasUsage5h {
		// 被动 /responses 头刷新了 5h 窗口重置时刻：武装「到点即探」，窗口翻新即刷新进度条。
		store.WakeBoundaryProbe(result.Reset5hAt)
	}
	if usageApplied && result.Used5hHeaders && account.IsPremium5hPlan() && result.HasUsage5h && result.UsagePct5h >= 100 && !account.SkipsUsageWindowLimits() {
		if store != nil {
			result.Premium5hRateLimited = store.MarkPremium5hRateLimitedAt(account, result.Reset5hAt, observedAt)
		} else {
			result.Premium5hRateLimited = true
		}
	}

	return result
}

func downstreamAuthorizationHeader(req *http.Request) string {
	if req == nil {
		return ""
	}
	if value := req.Header.Get("Authorization"); value != "" {
		return value
	}
	for _, name := range []string{"x-api-key", "anthropic-auth-token", "x-goog-api-key"} {
		if value := strings.TrimSpace(req.Header.Get(name)); value != "" {
			return "Bearer " + value
		}
	}
	return ""
}

const (
	contextAPIKeyID     = "apiKeyID"
	contextAPIKeyName   = "apiKeyName"
	contextAPIKeyMasked = "apiKeyMasked"
	contextAPIKeyRow    = "apiKeyRow"
	// contextScopeBudgetGate 存放本次请求的 scope 预算闸门（issue #439），
	// 由 enforceAPIKeyLimits 计算一次，供账号过滤链与「无可用账号」分支复用。
	contextScopeBudgetGate = "apiKeyScopeBudgetGate"
)


const (
	logStatusClientClosed        = 499
	logStatusUpstreamStreamBreak = 598
	// AccessLogStatusContextKey 允许流处理器在 HTTP 200 header 已提交后，
	// 把最终的内部结果（如客户端断开的 499）提供给访问日志中间件。
	AccessLogStatusContextKey = "x-access-log-status"
)

// upstreamStreamBreakMessage 是断流反馈给下游的稳定可读消息；机器识别用
// 稳定错误码 ErrorCodeUpstreamStreamBreak，下游网关/客户端可据此自动重试。

const proOnlySparkModel = "gpt-5.3-codex-spark"

func containsAnyPhrase(text string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
const consoleUpstreamErrorLogMaxBytes = 4 * 1024


func codexTurnContinuationToken(headers http.Header, body []byte) string {
	if headers != nil {
		if token := strings.TrimSpace(headers.Get(codexTurnStateHeader)); token != "" {
			return token
		}
	}
	return strings.TrimSpace(gjson.GetBytes(body, "client_metadata.x-codex-turn-state").String())
}

// codexWSTurnContinuationToken reads per-frame metadata only. Upgrade headers
// are connection-scoped and cannot prove that every response.create belongs to
// the same active turn.

func int64GroupSet(ids []int64) map[int64]struct{} {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			set[id] = struct{}{}
		}
	}
	return set
}

// groupMembershipFilter 在 filter 之上叠加分组门：want=true 要求账号命中 groups，
// want=false 要求账号不在 groups 里。

func groupMembershipFilter(groups map[int64]struct{}, want bool, filter auth.AccountFilter) auth.AccountFilter {
	return func(account *auth.Account) bool {
		if account == nil || account.InAnyGroup(groups) != want {
			return false
		}
		return filter == nil || filter(account)
	}
}


func accountFilterForResponsesModelWithOriginal(originalModel string, effectiveModel string, allowCodexAccounts bool) auth.AccountFilter {
	return accountFilterForResponsesModelCandidates([]string{originalModel, effectiveModel}, effectiveModel, allowCodexAccounts)
}


const ingressRequestBodyContextKey = "ingress_raw_body"


func setRawRequestBody(c *gin.Context, body []byte) {
	if c != nil {
		c.Set("raw_body", body)
	}
}


type requestCompactionMeta struct {
	// ProtocolTriggered is the wire-level compaction_trigger control. Only this
	// value may affect routing, account pinning, or protocol-specific timeouts.
	ProtocolTriggered bool
	// UsageTriggered is the observability signal persisted in usage_logs.compact.
	UsageTriggered bool
	// HasHistory marks direct durable compaction/context_compaction input items.
	HasHistory bool
}


func cacheRequestCompactionMeta(c *gin.Context, meta requestCompactionMeta) {
	if c != nil {
		c.Set(requestCompactionMetaContextKey, meta)
	}
}


func gjsonResultIsCompactionTrigger(result gjson.Result) bool {
	return result.IsObject() && strings.EqualFold(strings.TrimSpace(result.Get("type").String()), "compaction_trigger")
}


func gjsonResultIsCompactionHistory(result gjson.Result) bool {
	if !result.IsObject() {
		return false
	}
	itemType := strings.TrimSpace(result.Get("type").String())
	return strings.EqualFold(itemType, "compaction") || strings.EqualFold(itemType, "context_compaction")
}

// extractReasoningEffort 从请求体提取推理强度
// 支持 reasoning.effort（Responses API）和 reasoning_effort（Chat Completions API）

const upstreamErrorKindWsBusyAcquire = "ws_busy_acquire"

// isWsBusyAcquireTimeoutError 按错误文案识别 busy acquire 超时。wsrelay 依赖 proxy，
// proxy 无法反向导入其哨兵类型，跨包只能靠稳定的错误消息片段匹配。

func isWsBusyAcquireTimeoutError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "waiting for busy session")
}

// shouldPenalizeTransportKind 返回该传输失败是否应计入账号健康惩罚。
// busy acquire 超时的账号本身没有故障，惩罚会让 fast scheduler 错误降权（issue #413）。

func isWebsocketMessageTooBigError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "message too big") ||
		strings.Contains(msg, "close 1009") ||
		strings.Contains(msg, "read limit exceeded")
}


func isWebsocketUpstreamClose(err error) bool {
	return err != nil && strings.Contains(err.Error(), "websocket read error")
}


func classifyResponseFailedOutcome(payload []byte) streamOutcome {
	statusCode := responseFailedStatusCode(payload)
	errorBody := responseFailedErrorBody(payload)
	permanentQuota := isPermanentQuotaFailure(errorBody)
	safetyPolicy := isExplicitUpstreamSafetyPolicy(payload)
	cyberPolicy := isExplicitUpstreamCyberPolicy(payload)
	if permanentQuota {
		// Relays do not consistently include status_code on quota failures. Keep
		// them on the independent 429 budget and permanently exclude the account
		// from this request instead of admitting it to continuous 5xx cycles.
		statusCode = http.StatusTooManyRequests
	}
	if safetyPolicy {
		// Provider policy refusals are deterministic for this request. Some relays
		// report them with a synthetic 500, which must not inherit the legacy
		// finite 5xx retry path or penalize otherwise healthy accounts.
		statusCode = http.StatusBadRequest
	}
	message := usageLogErrorMessage(statusCode, payload)
	if cyberPolicy {
		// Upstream response.failed events often omit status_code, whose generic
		// fallback is 500. CYB is a deterministic request-policy rejection: expose
		// it as 400 and never rotate accounts/retry the same user request.
		statusCode = http.StatusBadRequest
		message = upstreamCyberPolicyUserMessage
	}
	if strings.TrimSpace(message) == "" || message == fmt.Sprintf("HTTP %d", statusCode) {
		message = "上游返回 response.failed"
	}
	kind := upstreamErrorKind(statusCode, errorBody, codex429Decision{})
	if permanentQuota {
		kind = "usage_limit"
	}
	if cyberPolicy {
		kind = "cyber_policy"
	} else if safetyPolicy {
		kind = "safety_policy"
	}
	if kind == "" {
		if statusCode >= 500 {
			kind = "server"
		} else {
			kind = "client"
		}
	}
	// 零输出的 response.incomplete 由网关改写成 response.failed（见
	// codex_empty_incomplete.go）：按 502 走透明重试，但不惩罚账号。
	emptyIncomplete := isEmptyIncompleteFailurePayload(payload)
	if emptyIncomplete {
		kind = codexEmptyIncompleteFailureKind
	}
	// 400 中"账号不支持该模型"属账号权益问题，冷却后换号重试有意义，视同可重试故障。
	modelUnsupported := statusCode == http.StatusBadRequest && isCodexModelUnsupportedError(errorBody)
	return streamOutcome{
		logStatusCode:  statusCode,
		failureKind:    kind,
		failureMessage: message,
		failurePayload: append([]byte(nil), payload...),
		penalize:       !safetyPolicy && (statusCode == http.StatusUnauthorized || statusCode == http.StatusTooManyRequests || statusCode >= 500 || modelUnsupported),
		capacityShed:   !capacityShedHandlingDisabled() && isCapacityShedPayload(payload),
		requestScoped:  emptyIncomplete,
	}
}

// reportStreamOutcomeFailure 上报流内 response.failed 类故障到账号健康度，但容量降载
// 例外：它是按模型/身份容量分桶的请求级瞬时信号，换号不改变被降载因素，计入连击只会
// 在高峰期把整池账号逐个调度降权。跳过上报即"软信号保留、硬惩罚移除"。

const capacityShedRetryableClientCode = "server_error"

// maxCapacityShedSameAccountRetries 是容量降载时在同一账号上退避重试的次数上限。
// 降载是按客户端身份/模型容量分桶的请求级信号，换号并不改变被降载的因素，先在
// 同账号退避重试若干次，耗尽后再换号（且全程不计入账号连击/健康度）。

func responseOutputItemDoneKey(item gjson.Result) string {
	if key := strings.TrimSpace(item.Get("id").String()); key != "" {
		// gjson strings borrow the parsed item's storage. Keep only the short
		// identity, not a second full output JSON through this map key.
		if len(key) <= 256 {
			return "id:" + key
		}
		sum := sha256.Sum256([]byte(key))
		return fmt.Sprintf("id-sha256:%x", sum)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(item.Raw)))
	return fmt.Sprintf("raw-sha256:%x", sum)
}


type responseOutputItemRecord struct {
	outputIndex int
	hasIndex    bool
	sequence    int
	raw         json.RawMessage
}

// responseOutputCollector rebuilds terminal response.output from the canonical
// response.output_item.done lifecycle events. Relays occasionally return an
// empty or partially populated terminal output array even though every item was
// already delivered. The collector is request-scoped and bounded by the same
// logical reconstruction budget used for response-context replay.

func isRetryableStatus(code int) bool {
	return code == http.StatusServiceUnavailable ||
		code == http.StatusUnauthorized ||
		code == http.StatusInternalServerError ||
		code == http.StatusPaymentRequired ||
		code == http.StatusForbidden ||
		code == http.StatusUpgradeRequired
}


func isRetryableRequestError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var structured *Error
	if errors.As(err, &structured) {
		if structured.Retryable {
			return true
		}
		return structured.Code == ErrorCodeUpstreamError && structured.HTTPStatus == 0 && structured.Cause != nil
	}
	return classifyTransportFailure(err) != ""
}


func retryBudgetAvailable(used, limit int) bool {
	return limit == -1 || (limit > 0 && used < limit)
}


func retryLimitForHTTPStatus(statusCode, generalLimit, rateLimit int) int {
	if statusCode == http.StatusTooManyRequests {
		return rateLimit
	}
	return generalLimit
}


func IsDeactivatedWorkspaceError(body []byte) bool {
	for _, path := range []string{"detail.code", "error.code", "code"} {
		code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, path).String()))
		if code == "deactivated_workspace" {
			return true
		}
	}
	message := strings.ToLower(firstGJSONString(body, "detail.message", "error.message", "message"))
	return strings.Contains(message, "deactivated_workspace")
}

// IsAgentRuntimeDeletedError 判断响应体是否表示 Agent runtime 已删除。

func IsAgentRuntimeDeletedError(body []byte) bool {
	code := strings.ToLower(firstGJSONString(body, "error.code", "detail.code", "code"))
	if code != "biscuit_baker_service_agent_error_status" {
		return false
	}
	message := strings.ToLower(firstGJSONString(body, "error.message", "detail.message", "message"))
	return strings.Contains(message, "agent runtime has been deleted")
}


func isCodexModelUnsupportedError(body []byte) bool {
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
		if strings.Contains(lower, "model is not supported when using codex") {
			return true
		}
		if strings.Contains(lower, "unknown provider for model") {
			return true
		}
	}
	return false
}

// codexUnsupportedModelRe 匹配上游 "The 'gpt-5.4-mini' model is not supported when
// using Codex with a ChatGPT account." 里被拒绝的模型名。
func accountFilterForResponsesModelCandidates(modelCandidates []string, effectiveModel string, allowCodexAccounts bool) auth.AccountFilter {
	return accountFilterForResponsesModelResolver(effectiveModel, allowCodexAccounts, func(account *auth.Account) (string, bool) {
		return resolveAccountModelMappingForCandidates(account, modelCandidates...)
	})
}

const requestCompactionMetaContextKey = "request_compaction_meta"


func capacityShedHandlingDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEX_DISABLE_CAPACITY_SHED_HANDLING"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// isCapacityShedPayload 判断 response.failed / error 帧是否为上游容量降载
// （server_is_overloaded / slow_down）。复用 sanitizeCapacityShedEventForClient
// 的三条 code path，供分类与失败上报两侧共用。

func isCapacityShedPayload(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	for _, path := range []string{"error.code", "response.error.code", "response.status_details.error.code"} {
		switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, path).String())) {
		case "server_is_overloaded", "slow_down":
			return true
		}
	}
	return false
}

// sanitizeCapacityShedEventForClient 把即将写给下游的 error / response.failed
// 事件中的容量降载错误码改写为客户端可重试的错误码。走到写出这一步说明网关侧
// 换号重试已不可用（流中途已有输出）或已用尽；保留原始降载码只会让 Codex CLI
// 就地终止会话。错误消息原样保留；监控、计费与账号冷却均基于改写前的原始
// payload（terminalFailurePayload 在写出前捕获），不受影响。rate_limit_exceeded
// 等其他错误码一律不动（客户端依赖原码解析重试延时）。

func responseFailedStatusCode(payload []byte) int {
	if status, evidenced := responseFailedStatusCodeWithEvidence(payload); evidenced {
		return status
	}
	// Keep the historical status used for logs/client errors. Selective HTTP
	// matching uses the evidenced helper above and never consumes this fallback.
	return http.StatusInternalServerError
}


func apiKeyFromWebSocketSubprotocol(header string) string {
	for _, item := range strings.Split(header, ",") {
		item = strings.TrimSpace(item)
		const prefix = "openai-insecure-api-key."
		if strings.HasPrefix(item, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(item, prefix))
		}
	}
	return ""
}

// ==================== /v1/responses ====================

// getMaxRetries 从 store 读取可配置的最大重试次数

func parseCodexCreditsFlagHeaders(resp *http.Response) (hasCredits, unlimited, ok bool) {
	rawHas := strings.TrimSpace(resp.Header.Get("x-codex-credits-has-credits"))
	rawUnlimited := strings.TrimSpace(resp.Header.Get("x-codex-credits-unlimited"))
	if rawHas == "" || rawUnlimited == "" {
		return false, false, false
	}
	hasCredits, err := strconv.ParseBool(rawHas)
	if err != nil {
		return false, false, false
	}
	unlimited, err = strconv.ParseBool(rawUnlimited)
	if err != nil {
		return false, false, false
	}
	return hasCredits, unlimited, true
}


func parseCodexUsageHeaderObservation(resp *http.Response) codexUsageHeaderObservation {
	out := codexUsageHeaderObservation{}
	if resp == nil {
		return out
	}

	// 解析 primary 和 secondary 窗口
	primaryUsedStr := resp.Header.Get("x-codex-primary-used-percent")
	primaryWindowStr := resp.Header.Get("x-codex-primary-window-minutes")
	primaryResetStr := resp.Header.Get("x-codex-primary-reset-after-seconds")
	secondaryUsedStr := resp.Header.Get("x-codex-secondary-used-percent")
	secondaryWindowStr := resp.Header.Get("x-codex-secondary-window-minutes")
	secondaryResetStr := resp.Header.Get("x-codex-secondary-reset-after-seconds")

	primary := parseCodexWindowUsage(primaryUsedStr, primaryWindowStr, primaryResetStr)
	secondary := parseCodexWindowUsage(secondaryUsedStr, secondaryWindowStr, secondaryResetStr)

	for _, window := range []codexWindowUsage{primary, secondary} {
		if !window.valid {
			continue
		}
		switch codexWindowType(window.windowMin) {
		case codexRateLimitWindow5h:
			if !out.w5h.valid {
				out.w5h = window
			}
		case codexRateLimitWindow7d:
			if !out.w7d.valid {
				out.w7d = window
			}
		}
	}
	out.authoritative = out.w5h.valid || out.w7d.valid
	return out
}


func applyCodexUsageHeaderObservation(store *auth.Store, account *auth.Account, observation codexUsageHeaderObservation, observedAt time.Time) codexUsageHeaderParseResult {
	out := codexUsageHeaderParseResult{}
	if observation.w5h.valid {
		resetAt := observedAt.Add(time.Duration(observation.w5h.resetSec) * time.Second)
		account.SetUsageSnapshot5hAt(observation.w5h.usedPct, resetAt, observedAt)
	} else if observation.authoritative && store != nil {
		out.cleared5h = store.ClearAbsentUsageSnapshot5hAt(account, observedAt)
	}

	if observation.w7d.valid {
		resetAt := observedAt.Add(time.Duration(observation.w7d.resetSec) * time.Second)
		account.SetReset7dAt(resetAt)
		account.SetWindow7dSeconds(int64(observation.w7d.windowMin * 60))
		account.SetUsagePercent7d(observation.w7d.usedPct)
		out.usagePct7d = observation.w7d.usedPct
		out.hasUsage7d = true
	}
	return out
}

// ParseCodexUsageHeaders 从响应头提取并更新账号用量信息
func addImageStatsToMap(item map[string]any) bool {
	if item == nil {
		return false
	}
	result := firstNonEmptyAnyString(item["result"])
	if result == "" {
		return false
	}
	stats, ok := imageStatsFromBase64(result)
	if !ok {
		return false
	}
	changed := false
	if stats.ByteSize > 0 {
		if _, exists := item["bytes"]; !exists {
			item["bytes"] = stats.ByteSize
			changed = true
		}
	}
	if stats.Width > 0 {
		if _, exists := item["width"]; !exists {
			item["width"] = stats.Width
			changed = true
		}
	}
	if stats.Height > 0 {
		if _, exists := item["height"]; !exists {
			item["height"] = stats.Height
			changed = true
		}
	}
	return changed
}

type codexRateLimitWindow string

const (
	codexRateLimitWindowUnknown codexRateLimitWindow = ""
	codexRateLimitWindowShort   codexRateLimitWindow = "short"
	codexRateLimitWindow5h      codexRateLimitWindow = "5h"
	codexRateLimitWindow7d      codexRateLimitWindow = "7d"
)
func accountFilterForResponsesModelResolver(effectiveModel string, allowCodexAccounts bool, resolveMapping func(*auth.Account) (string, bool)) auth.AccountFilter {
	effectiveModel = strings.TrimSpace(effectiveModel)
	codexFilter := accountFilterForModel(effectiveModel)
	return func(account *auth.Account) bool {
		if account == nil {
			return false
		}
		if account.IsRelayStyle() {
			routedModel := effectiveModel
			if mappedModel, ok := resolveMapping(account); ok && mappedModel != "" {
				routedModel = mappedModel
			}
			supported := relayAccountSupportsModel(account, routedModel)
			rateLimited := routedModel != "" && account.IsModelRateLimited(routedModel)
			return supported && !rateLimited
		}
		if !allowCodexAccounts {
			return false
		}
		return codexFilter(account)
	}
}

// relayAccountSupportsModel 判断 relay 风格账号能否服务指定模型。
// relay 中转必须显式声明 models 白名单，与 /v1/models 的默认集注册
// （supportedModelIDs）保持一致，声明了白名单的账号以白名单为准。

func codexWindowType(windowMinutes float64) codexRateLimitWindow {
	switch {
	case windowMinutes >= 1440:
		return codexRateLimitWindow7d
	case windowMinutes >= 60:
		return codexRateLimitWindow5h
	case windowMinutes > 0:
		return codexRateLimitWindowShort
	default:
		return codexRateLimitWindowUnknown
	}
}


type codexWindowUsage struct {
	usedPct   float64
	resetSec  float64
	windowMin float64
	valid     bool
}


func parseCodexWindowUsage(usedStr, windowStr, resetStr string) codexWindowUsage {
	if strings.TrimSpace(usedStr) == "" || strings.TrimSpace(windowStr) == "" {
		return codexWindowUsage{}
	}
	var usedPct, windowMin, resetSec float64
	if _, err := fmt.Sscanf(usedStr, "%f", &usedPct); err != nil {
		return codexWindowUsage{}
	}
	if _, err := fmt.Sscanf(windowStr, "%f", &windowMin); err != nil || windowMin <= 0 {
		return codexWindowUsage{}
	}
	if strings.TrimSpace(resetStr) != "" {
		if _, err := fmt.Sscanf(resetStr, "%f", &resetSec); err != nil {
			resetSec = 0
		}
	}
	return codexWindowUsage{
		usedPct:   usedPct,
		windowMin: windowMin,
		resetSec:  resetSec,
		valid:     true,
	}
}


type codexUsageHeaderParseResult struct {
	usagePct7d float64
	hasUsage7d bool
	cleared5h  bool
}


type codexUsageHeaderObservation struct {
	w5h           codexWindowUsage
	w7d           codexWindowUsage
	authoritative bool
}

// parseCodexUsageHeaders 从 Codex 响应头解析 5h/7d 用量百分比

func imageStatsFromBase64(raw string) (imageOutputStats, bool) {
	data, ok := decodeImageBase64(raw)
	if !ok {
		return imageOutputStats{}, false
	}
	stats := imageOutputStats{ByteSize: len(data)}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		stats.Width = cfg.Width
		stats.Height = cfg.Height
		return stats, true
	}
	if width, height, ok := decodeWebPDimensions(data); ok {
		stats.Width = width
		stats.Height = height
	}
	return stats, true
}
