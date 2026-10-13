package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/codex2cc/api"
	"github.com/codex2cc/auth"
	"github.com/codex2cc/cache"
	"github.com/codex2cc/config"
	"github.com/codex2cc/database"
	"github.com/codex2cc/security"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

// Handler 是下游协议入口的装配体：持有账号池、数据库、运行时缓存与各类限流器。
// codex2cc 只暴露 Anthropic Messages 入口（Claude Code CLI → Codex 上游），
// 因此这里不再保留 responses/chat/images/videos/live/gemini 等渠道的处理句柄。
type Handler struct {
	store           *auth.Store
	configKeys      map[string]bool // 配置文件中的静态 key
	db              *database.DB
	cfg             *config.Config       // 全局配置
	deviceCfg       *DeviceProfileConfig // 设备指纹配置
	cache           cache.TokenCache     // Redis/Memory 运行态缓存
	apiKeyLookups   singleflight.Group
	authCache       *apiKeyAuthCache
	apiKeyGateMu    sync.Mutex
	apiKeyGate      *apiKeyConcurrencyLimiter
	scopeUsageMu    sync.Mutex
	scopeUsage      *apiKeyScopeUsageTracker
	scopeDeltaInit  sync.Once
	scopeDeltaSlots chan struct{}
	// continuousRetryReplayFactory injects bounded replay failures in tests
	// without changing process-wide limits or TMPDIR.
	// continuousRetryReplayFactory 在测试中注入有界回放失败，不修改进程级限制或 TMPDIR。
	continuousRetryReplayFactory func() *continuousRetryReplay
}

const (
	apiKeyCacheNamespace      = "api-key"
	apiKeyCountCacheNamespace = "api-key-count"
	apiKeyCacheTTL            = 5 * time.Minute
	apiKeyCountCacheTTL       = 30 * time.Second
)

type apiKeyRuntimeRecord struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type apiKeyCountRuntimeRecord struct {
	Count int `json:"count"`
}

// NewHandler 创建处理器。
func NewHandler(store *auth.Store, db *database.DB, cfg *config.Config, deviceCfg *DeviceProfileConfig) *Handler {
	restoreModelCapabilities(store, db)
	return &Handler{
		store:      store,
		configKeys: make(map[string]bool), // 不再使用硬编码，但保留结构以向后兼容逻辑
		db:         db,
		cfg:        cfg,
		deviceCfg:  deviceCfg,
		apiKeyGate: newAPIKeyConcurrencyLimiter(),
	}
}

// SetRuntimeCache wires Redis/Memory runtime cache for hot auth metadata.
func (h *Handler) SetRuntimeCache(tc cache.TokenCache) {
	if h == nil {
		return
	}
	h.cache = tc
	if h.authCache != nil {
		h.authCache.close()
		h.authCache = nil
	}
	if h.cfg != nil && h.cfg.APIKeyAuthCacheEnabled && h.db != nil {
		h.authCache = newAPIKeyAuthCache(h.db, tc)
	}
}

// RegisterRoutes 注册下游协议路由。codex2cc 只保留 Anthropic Messages 入口：
// POST /v1/messages、POST /v1/messages/count_tokens、POST /v1/responses/input_tokens、
// GET /v1/models，以及 base_url 已含 /v1 的无前缀兼容路由。
// /health 等运维路由由 main 装配层注册，不在此处。
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	auth := h.authMiddleware()

	v1 := r.Group("/v1")
	v1.Use(auth)
	v1.POST("/messages", h.Messages)
	v1.POST("/messages/count_tokens", h.CountTokens)
	v1.POST("/responses/input_tokens", h.ResponsesInputTokens)
	// Codex CLI / Codex App 从 /models?client_version=... 刷新模型选单，期望
	// manifest 格式；client_version 是 Codex 客户端的天然指纹，普通 OpenAI
	// 客户端不携带，其余请求保持 OpenAI 格式列表不变。
	v1.GET("/models", h.listModelsOrManifest)

	// 无前缀路由（兼容 base_url 已包含 /v1 的客户端）
	r.POST("/messages", auth, h.Messages)
	r.POST("/messages/count_tokens", auth, h.CountTokens)
	r.POST("/responses/input_tokens", auth, h.ResponsesInputTokens)
	r.GET("/models", auth, h.listModelsOrManifest)
}

// APIKeyAuthMiddleware exposes the standard /v1 API key authentication middleware
// for companion routes that live outside proxy.RegisterRoutes.
func (h *Handler) APIKeyAuthMiddleware() gin.HandlerFunc {
	return h.authMiddleware()
}

// APIKeyReadAuthMiddleware permits exhausted keys to retrieve their own stored
// results. All other authentication checks remain in force; writes stay blocked.
func (h *Handler) APIKeyReadAuthMiddleware() gin.HandlerFunc {
	return h.authMiddlewareWithQuotaRead(true)
}

// authMiddleware API Key 鉴权中间件（增强版，带安全日志）
//
// 安全策略（fail-closed）：
//   - 默认情况下，未配置任何 API Key 时直接拒绝请求（503），避免裸奔账号池。
//   - 仅当显式设置 CODEX_ALLOW_ANONYMOUS=true 时才在无密钥情况下放行（兼容内网/测试）。
func (h *Handler) authMiddleware() gin.HandlerFunc {
	return h.authMiddlewareWithQuotaRead(false)
}

func (h *Handler) authMiddlewareWithQuotaRead(allowQuotaRead bool) gin.HandlerFunc {
	allowAnonymous := h.cfg != nil && h.cfg.AllowAnonymousV1
	return func(c *gin.Context) {
		attachUserAgentAudit(c)
		attachWsAcquireAudit(c)
		attachUpstreamTrace(c, h.store)
		// 如果没有配置任何密钥
		hasKeys, presenceErr := h.hasAnyKeysWithError()
		if presenceErr != nil {
			api.SendError(c, api.ErrServiceUnavailable)
			c.Abort()
			return
		}
		if !hasKeys {
			if allowAnonymous {
				// 显式允许匿名访问（旧行为，仅在 CODEX_ALLOW_ANONYMOUS=true 时启用）
				c.Next()
				return
			}
			// fail-closed：未配置 API Key 即拒绝，避免账号池被未授权调用
			security.SecurityAuditLog("V1_BLOCKED_NO_KEYS", auditFields(c))
			api.SendError(c, api.NewAPIError(
				api.ErrCodeServiceUnavailable,
				"Service is not configured: no API key has been created yet. Please add at least one API key in the admin dashboard, or set CODEX_ALLOW_ANONYMOUS=true to disable this check.",
				api.ErrorTypeServer,
			))
			c.Abort()
			return
		}

		authHeader := downstreamAuthorizationHeader(c.Request)
		if authHeader == "" {
			api.SendError(c, api.ErrMissingAPIKey)
			c.Abort()
			return
		}

		// 清理输入
		authHeader = security.SanitizeInput(authHeader)

		key := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		apiKeyRow, ok, resolveErr := h.resolveAPIKeyContext(c.Request.Context(), key)
		if resolveErr != nil {
			// DB/基础设施暂时性故障：返回 503，不当成客户端 key 无效（issue #323）。
			// 不记 AUTH_FAILED 审计日志，避免污染凭证攻击告警。
			api.SendError(c, api.ErrServiceUnavailable)
			c.Abort()
			return
		}
		if !ok {
			security.SecurityAuditLog("AUTH_FAILED", auditFields(c)+" key="+security.MaskAPIKey(key))
			api.SendError(c, api.ErrInvalidAPIKey)
			c.Abort()
			return
		}
		if !apiKeyRow.Enabled {
			security.SecurityAuditLog("AUTH_FAILED_DISABLED_KEY", auditFields(c)+" key="+security.MaskAPIKey(key))
			api.SendError(c, api.NewAPIError(api.ErrCodeInvalidAuth, "API key is disabled", api.ErrorTypeAuthentication))
			c.Abort()
			return
		}
		if apiKeyRow.IsExpired(time.Now()) {
			security.SecurityAuditLog("AUTH_FAILED_EXPIRED_KEY", auditFields(c)+" key="+security.MaskAPIKey(key))
			api.SendError(c, api.NewAPIError(api.ErrCodeInvalidAuth, "API key has expired", api.ErrorTypeAuthentication))
			c.Abort()
			return
		}
		readOnly := allowQuotaRead && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead)
		if apiKeyRow.IsQuotaExhausted() && !readOnly {
			security.SecurityAuditLog("AUTH_FAILED_QUOTA_EXHAUSTED", auditFields(c)+" key="+security.MaskAPIKey(key))
			api.SendError(c, api.NewAPIError(api.ErrCodeRateLimitReached, "API key quota exhausted", api.ErrorTypeRateLimit))
			c.Abort()
			return
		}
		c.Set(contextAPIKeyID, apiKeyRow.ID)
		c.Set(contextAPIKeyName, strings.TrimSpace(apiKeyRow.Name))
		c.Set(contextAPIKeyMasked, security.MaskAPIKey(apiKeyRow.Key))
		c.Set(contextAPIKeyRow, apiKeyRow)
		h.attachAPIKeyModelRequestQuota(c, false)
		c.Set("apiKey", key)
		if h.enforceRequiredNewAPIIdentityAtIngress(c) {
			c.Abort()
			return
		}
		c.Next()
	}
}

// auditFields 统一审计日志的路径/IP 段，避免各分支重复拼接。
func auditFields(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return "path=" + c.Request.URL.Path + " ip=" + c.ClientIP()
}

func (h *Handler) setAPIKeyRuntimeCache(row *database.APIKeyRow) {
	if h == nil || h.cache == nil || row == nil || strings.TrimSpace(row.Key) == "" || row.ID <= 0 {
		return
	}
	if row.HasAccessConstraints() {
		return
	}
	record := apiKeyRuntimeRecord{
		ID:        row.ID,
		Name:      row.Name,
		CreatedAt: row.CreatedAt,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := h.cache.SetRuntime(ctx, apiKeyCacheNamespace, row.Key, payload, apiKeyCacheTTL); err != nil {
		log.Printf("写入 API Key Redis 缓存失败: id=%d err=%v", row.ID, err)
	}
}

func (h *Handler) syncAPIKeyAllowedGroups(row *database.APIKeyRow) {
	if h == nil || h.store == nil || row == nil || row.ID <= 0 {
		return
	}
	h.store.SetAPIKeyAllowedGroups(row.ID, row.AllowedGroupIDs)
	h.store.SetAPIKeyNoAffinityGroups(row.ID, row.Limits.NoAffinityGroupIDs)
	h.store.SetAPIKeyAllowedPlans(row.ID, row.Limits.PlanAllow)
	h.store.SetAPIKeyUpstreamChannel(row.ID, row.Limits.ResolveUpstreamChannel())
}

// hasAnyKeys 检查是否配置了任何密钥
func (h *Handler) hasAnyKeys() bool {
	configured, err := h.hasAnyKeysWithError()
	return err == nil && configured
}

func (h *Handler) hasAnyKeysWithError() (bool, error) {
	if len(h.configKeys) > 0 {
		return true, nil
	}
	if h.authCache != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for i := 0; i < 3; i++ {
			state, _, err := h.authCache.revision(ctx)
			if errors.Is(err, errAPIKeyAuthRetry) {
				continue
			}
			return state.KeyCount > 0, err
		}
		return false, errAPIKeyAuthRetry
	}
	if h.cache != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		raw, ok, err := h.cache.GetRuntime(ctx, apiKeyCountCacheNamespace, "all")
		cancel()
		if err != nil {
			log.Printf("读取 API Key 数量缓存失败: %v", err)
		} else if ok {
			var record apiKeyCountRuntimeRecord
			if err := json.Unmarshal(raw, &record); err == nil {
				return record.Count > 0, nil
			}
		}
	}
	if h.db == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	count, err := h.db.CountAPIKeys(ctx)
	if err != nil {
		log.Printf("统计 API Key 数量失败: %v", err)
		return false, err
	}
	if h.cache != nil {
		if payload, marshalErr := json.Marshal(apiKeyCountRuntimeRecord{Count: count}); marshalErr == nil {
			writeCtx, writeCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer writeCancel()
			if setErr := h.cache.SetRuntime(writeCtx, apiKeyCountCacheNamespace, "all", payload, apiKeyCountCacheTTL); setErr != nil {
				log.Printf("写入 API Key 数量缓存失败: %v", setErr)
			}
		}
	}
	return count > 0, nil
}

// resolveAPIKey 解析下游 API Key。返回值区分三种结果：
//   - (row, true, nil)：命中有效 key
//   - (nil, false, nil)：确认查无此 key（应答 401）
//   - (nil, false, err)：DB/基础设施暂时性故障（应答 503，而非误报 key 无效）
//
// 关键：绝不能把"数据库连接耗尽/超时"这类暂时性故障当成"客户端 key 无效"
// 返回 401，否则压测或 DB 抖动时客户端会误以为自己的凭证失效（issue #323）。
func (h *Handler) resolveAPIKeyUnshared(key string) (*database.APIKeyRow, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, false, nil
	}
	if h.configKeys[key] {
		return &database.APIKeyRow{
			ID:      0,
			Name:    "config",
			Key:     key,
			Enabled: true,
		}, true, nil
	}
	if row, ok := h.resolveAPIKeyFromRuntimeCache(key); ok {
		h.syncAPIKeyAllowedGroups(row)
		return row, true, nil
	}
	if h.db == nil {
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	row, err := h.db.GetAPIKeyByValue(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		// DB 故障（连接耗尽/超时/网络）：上报错误让调用方回 503，不当成 key 无效。
		log.Printf("查询 API Key 失败: %v", err)
		return nil, false, err
	}
	h.setAPIKeyRuntimeCache(row)
	h.syncAPIKeyAllowedGroups(row)
	return row, true, nil
}

func (h *Handler) resolveAPIKeyFromRuntimeCache(key string) (*database.APIKeyRow, bool) {
	if h == nil || h.cache == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	raw, ok, err := h.cache.GetRuntime(ctx, apiKeyCacheNamespace, key)
	if err != nil {
		log.Printf("读取 API Key Redis 缓存失败: %v", err)
		return nil, false
	}
	if !ok || len(raw) == 0 {
		return nil, false
	}
	var record apiKeyRuntimeRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		log.Printf("解析 API Key Redis 缓存失败: %v", err)
		return nil, false
	}
	if record.ID <= 0 {
		return nil, false
	}
	// 运行时缓存只收录无任何访问约束的 key（含 enabled=true），此处可安全回填。
	return &database.APIKeyRow{
		ID:        record.ID,
		Name:      record.Name,
		Key:       key,
		Enabled:   true,
		CreatedAt: record.CreatedAt,
	}, true
}

// ListModels 列出可用模型
// listModelsOrManifest 按客户端形态分发模型列表：带 client_version 查询参数的是
// Codex 客户端在刷新模型选单（期望 manifest 格式，解析失败会静默冻结在本地缓存）。
// 其余客户端返回 OpenAI 兼容列表。
func (h *Handler) listModelsOrManifest(c *gin.Context) {
	if strings.TrimSpace(c.Query("client_version")) != "" {
		h.CodexModelsManifestHandler(c)
		return
	}
	h.ListModels(c)
}

func (h *Handler) ListModels(c *gin.Context) {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	// Authenticated routes always carry the complete APIKeyRow. Keep the
	// historical global list only for direct/internal calls that bypass the
	// middleware (including older embedders); a real key always gets an
	// isolated, read-only account snapshot.
	if row := apiKeyRowFromContext(c); row != nil {
		api.SendList(c, "list", h.scopedModels(ctx, row))
		return
	}
	modelIDs := h.supportedModelIDs(ctx)
	models := make([]api.Model, 0, len(modelIDs))
	for _, id := range modelIDs {
		models = append(models, api.Model{
			ID:      id,
			Object:  "model",
			Created: modelCompatibilityCreatedUnix,
			OwnedBy: "openai",
		})
	}
	api.SendList(c, "list", models)
}

func (h *Handler) supportedModelIDs(ctx context.Context) []string {
	models := SupportedModelIDs(ctx, h.db)
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		seen[strings.ToLower(strings.TrimSpace(model))] = struct{}{}
	}
	if h != nil && h.store != nil {
		for _, account := range h.store.Accounts() {
			declared := account.OpenAIResponsesModels()
			for _, model := range declared {
				key := strings.ToLower(strings.TrimSpace(model))
				if key == "" {
					continue
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				models = append(models, model)
			}
			aliases := accountModelMappingAliases(account)
			for _, alias := range aliases {
				key := strings.ToLower(strings.TrimSpace(alias))
				if key == "" {
					continue
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				models = append(models, alias)
			}
		}
	}
	return models
}

// getMaxRetries 从 store 读取可配置的最大重试次数
func (h *Handler) getMaxRetries() int {
	if h == nil || h.store == nil {
		return 1
	}
	return h.store.GetMaxRetries()
}

func (h *Handler) getMaxRateLimitRetries() int {
	if h == nil || h.store == nil {
		return 1
	}
	return h.store.GetMaxRateLimitRetries()
}

func (h *Handler) shouldUseWebsocketForHTTP() bool {
	if h == nil {
		return false
	}
	// 运行时 DB 级开关 codex_force_websocket 优先：开启则强制走 WS
	// （与 ExecuteRequest 的 wantWebsocket 判定保持一致，也用于 usage 日志的 WS 标记）。
	if CurrentRuntimeSettings().CodexForceWebsocket {
		return true
	}
	// 管理后台的热更新值同样作为强制开关来源，避免运行时配置尚未同步时
	// UI 显示已开启但请求热路径仍按静态 env=http 判定。
	if h.store != nil && h.store.CodexForceWebsocket() {
		return true
	}
	if h.cfg == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(h.cfg.CodexUpstreamTransport)) {
	case "ws":
		return true
	case "http", "auto":
		return false
	default:
		return h.cfg.UseWebsocket
	}
}
