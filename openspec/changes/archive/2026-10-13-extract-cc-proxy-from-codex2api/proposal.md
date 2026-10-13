# 变更提案：extract-cc-proxy-from-codex2api

## 背景

codex2api 是一个四渠道（Codex/Claude/Grok/Antigravity）账号池网关，约 22 万行 Go + 6 万行前端，体量庞大。本需求只需要其中一条链路：**Claude Code CLI → `/v1/messages` → Anthropic Messages 翻译成 Codex Responses → ChatGPT/Codex 上游账号 → SSE 翻译回 Anthropic 事件**。该链路在 codex2api 中已有完整实现（`proxy/anthropic.go` 的 `TranslateAnthropicToCodexWithModels` + `anthropicStreamTranslator`、`proxy/handler_anthropic.go` 的 `Messages` handler、`proxy/codex_tool_names.go` 等），无需重写，属纯抽离裁剪。网页端保留 codex2api 原有样式（Tailwind 4 主题 token + Layout + shadcn ui 组件），仅裁剪与被删渠道相关的页面。

## 目标范围

**在范围内：**
- 在当前仓库 codex2cc（module `github.com/codex2cc`）重建可编译可运行的最小网关：
  - 下游：`POST /v1/messages`（Anthropic Messages，Claude Code CLI 主链路）+ `POST /v1/messages/count_tokens` + `GET /v1/models` + `/health`
  - 上游：Codex/ChatGPT OAuth 账号池 + relay 中转账号（含 HTTP 与 WebSocket 两种上游传输）
  - 账号池调度：健康分、会话亲和、冷却、重试排除（auth.Store + cache.TokenCache）
  - 管理后台 admin REST API 裁剪子集（账号管理、用量统计、API Key、系统设置、Claude 连通性测试）
  - 前端保留页面：Dashboard / Accounts（Codex 视图）/ Usage / APIKeys / Settings / Docs + Layout/AuthGate/ui 全套样式基础设施；删除 Grok、Antigravity、生图、提示词过滤、PayloadRules、Proxies、QualityTest、ModelPricing、运维看板等页面
  - sqlite/postgres 双驱动（裁剪无关表列）
  - Dockerfile + docker-compose 一键部署
  - 同步迁移相关 _test.go，保证 `go test ./...` 通过
- import path 全量改写：`github.com/codex2api` → `github.com/codex2cc`

**不在范围内：**
- Claude OAuth 上游账号（Claude Code 账号池）——已确认仅留 Codex/ChatGPT + relay 上游；`/v1/messages` 中原 `IsClaudeOAuth()` 原生透传分支一并移除
- Codex CLI 客户端路由（`/v1/responses`、`/v1/chat/completions`、`/backend-api/codex/*`、`/v1/alpha/search`、Realtime、生图/生视频、Gemini `/v1beta`）
- Grok / Antigravity / Gemini 渠道全部代码（proxy/auth/admin/database/frontend 各层）
- 提示词过滤引擎（security/promptfilter 13.6k 行及 prompt_* 域）、遥测（codex_telemetry*）、生图存储（internal/imagestore 等）
- 公开门户（key-usage / image-studio / account-portal 页面与后端）
- 任何架构重构、协议增强、样式改动——只裁剪、改 import、修补编译

### 保留 / 删除文件对照表

**Go 侧整体删除（文件级）**：
- proxy/：grok_*（15 文件）、antigravity_*（9）、handler_gemini.go、images*.go、image_*.go、grok_media.go、live*.go、liveattestation/、prompt_*.go、newapi_policy.go、codex_telemetry*（5）、codex_invite.go、codex_alpha_search.go、codex_cli_version_sync.go、codex_ua_catalog.go、claude_*（上游侧 7 文件：claude_api_key/claude_cli_version_sync/claude_first_token_timeout/claude_thinking_disabled/claude_thinking_signature/claude_usage_semantics/claude_upstream）、count_tokens.go（仅删 Anthropic 侧 CountTokens，ResponsesInputTokens 不存在）、handler_chat_stream_failure_test 等
- proxy/handler.go 不整体删除，重写为薄装配版（见方案 4）
- auth/：claude_*（13）、grok_*（10）、antigravity_*（5）
- admin/：grok_*（7）、antigravity_*（6）、image_*（含 admin_images.go、image_studio_external.go）、prompt_filter*、proxy_*、wham_*、claude_accounts.go/claude_export.go/claude_setup_token.go/claude_config.go/claude_test_diagnostics.go（Claude 上游账号管理）、公开门户路由（key-usage/image-studio/account-portal/self_service）
- database/：渠道专属文件按 grep 引用裁剪；既有列 DDL 一律保留
- 根目录：health_probe.go/main_test.go/main_migrate_only_test.go 随 main.go 重写重建

**proxy/ 保留清单（显式）**：handler_anthropic.go、anthropic.go、translator.go、executor.go、retry_exclusions.go、model_mapping.go、model_registry.go、model_capabilities.go、model_pricing.go（模型计费口径，若 admin 用量页依赖）、response_cache*.go（7 文件）、stream_flush_writer.go、first_token_timeout.go、ttft.go、downstream_keepalive.go、endpoint_keepalive.go、continuous_retry*.go（5）、errors.go、error_logger.go、egress.go、ratelimit.go、utls_transport.go、device_profile.go、useragent.go、client_policy.go、scoped_models.go、models_list_limit.go、overflow_compact.go、compact_via_responses.go、portable_compaction.go、compaction_provenance.go、usage_wham*.go（用量归因，若用量页依赖）、payload_observed.go、payload_rules.go（admin 保留项裁掉则一并删）、codex_empty_incomplete.go、codex_environment_context.go、codex_fingerprint.go、codex_session_headers.go、codex_tool_names.go、codex_tool_schema_unions.go、codex_turn_state.go、codex_turn_state_inject.go、codex_ws_transport_lane.go、codex_models_manifest.go（/v1/models 数据源）、wsrelay/（9 文件）、handler.go（薄装配重写）、routing_hint.go、engine_fingerprint_signal.go、content_session_seed.go、internal_response*.go（若 handler_anthropic 引用则保留）

**前端页面删除清单（显式）**：GrokAccounts.tsx、AntigravityAccounts.tsx、ImageStudio.tsx、ImageStudioPortal.tsx、PromptFilter.tsx、PayloadRules.tsx、Proxies.tsx、QualityTest.tsx、ModelPricing.tsx、Operations.tsx、OperationsErrors.tsx、RuntimeStatus.tsx、SchedulerBoard.tsx、AccountPortal.tsx、APIKeyUsagePortal.tsx、pages/docs/ 中 grok/antigravity/生图相关内容块、对应 *.css 与 components/image-studio/；保留 Dashboard、Accounts（Codex 视图壳）、Usage、APIKeys、Settings（去 grok/antigravity Tab）、Docs、ThemeSettings + Layout/AuthGate/ui/hooks/样式三件套。locales 按命名空间删除：antigravity、grok、images、imageStudioPortal、accountPortal、payloadRules、proxies、promptFilter、qualityTest、ops、opsErrors、runtime、scheduler、modelPricing 相关 key；i18n key 残留用「key 扫描脚本比对存活组件 t() 调用」验证，不依赖 typecheck。

**admin/ 保留清单（显式）**：handler.go（路由裁剪版）、claude 相关之外的账号管理文件、accounts.go、usage*、api key/keys、settings.go（去渠道段）、bootstrap、system、runtime-status、quality-test 预览删除；连通性测试文件裁剪至 Codex/relay 渠道。

## 技术方案（续）

1. **拷贝基础层（零改动或仅 import 改写）**：config/、cache/、api/、security/（根，去 promptfilter）、internal/openaiidentity。
2. **database/ 裁剪**：保留 sqlite.go/postgres.go 双驱动骨架 + accounts/api_keys/usage_logs/system_settings/account_groups 等核心表；删除 grok/antigravity/images/promptfilter 相关列定义、迁移与文件（claude_provider 迁移保留 schema 兼容但渠道数据为空）。需手术 postgres.go（8.7k 行）与 data_migrations.go。
3. **auth/ 裁剪**：保留 Store 调度核心 + codex_* 账号文件 + oauth 刷新 + 代理池；删除 claude_*/grok_*/antigravity_* 账号文件与 Store 内对应分支（store.go 12.1k 行需手术剔除 claude/grok/antigravity 分支）。
4. **proxy/ 裁剪（核心）**：
   - 保留：handler_anthropic.go（Messages 主 handler）、anthropic.go（双向翻译器）、translator.go、executor.go、retry_exclusions.go、model_mapping/registry/capabilities、stream/keepalive/ttft、response_cache*、continuous_retry*、wsrelay/、codex_ 专属文件中协议相关者（turn_state/session_headers/fingerprint/tool_names/tool_schema_unions/environment_context/empty_incomplete/request_compression/ws_transport_lane/models_manifest）
   - 删除：handler.go 中 Responses/ChatCompletions/Images/Gemini 路由与 Grok 内联块（handler.go 8.9k 行重写为薄装配：仅保留 Handler struct、NewHandler、RegisterRoutes（仅 /v1/messages 系）、鉴权中间件、Claude 渠道过滤器改造）
   - 删除：grok_*、antigravity_*、images、live*、prompt_*、newapi_policy、handler_gemini.go、codex_telemetry*、codex_invite、codex_alpha_search、codex_cli_version_sync、codex_ua_catalog、claude_*（上游侧）
   - `claudeChannelAccountFilter` 保留（/v1/messages 选号过滤器），改造为「OAuth Codex 账号 + relay 账号」过滤
5. **admin/ 裁剪**：保留账号管理（codex/relay）、用量、API Key、设置、bootstrap、连通性测试；删除 grok_*/antigravity_*/image_*/prompt_filter*/proxy_*/wham_* 与公开门户路由。
6. **frontend/ 裁剪**：复制全部，删页面与对应 api.ts/types.ts/locales 条目收缩；样式基础设施（index.css、主题三件套、ui/、Layout、AuthGate、hooks、Toast）原样保留；清掉引用被删页面的约 50 个源码文本型 .test.mjs 中的失效者。
7. **main.go 重写装配段**：仅注册存活路由、存活后台任务（token 刷新、冷却探测、用量落库）、proxy↔wsrelay 接线、SPA 壳仅 /admin。
8. **验证链**：`go build ./...` → `go vet ./...` → `go test ./...` → `cd frontend && npm i && npm run build` → Docker 构建。

## 预期影响

- 产物为独立可运行网关，单二进制内嵌管理台，样式与 codex2api 一致
- 源仓库 codex2api 不做任何修改
- 代码量预计缩减至约 6–8 万行 Go + 2–3 万行前端
- 行为变化（相对 codex2api）：仅 /v1/messages 链路可用；/v1/responses 等 Codex 客户端路由不再存在；Claude 上游账号类型不再存在

## 风险

| 风险 | 应对 |
|---|---|
| handler.go / store.go / postgres.go 三个巨型文件手术裁剪漏删/误删，编译错误连锁 | 以编译器为闸：每裁一层立即 `go build`，按依赖叶子→根顺序推进（database→auth→proxy→admin→main） |
| 前端单文件聚合体（api.ts/types.ts/Accounts.tsx/locales）裁剪引用断裂 | 先删页面后收缩聚合体，`npm run build` + typecheck 验证；i18n key 残留 typecheck 查不出，额外做 key 扫描比对 |
| main.go 装配段剥离：config 中渠道配置项、OAuth 刷新/claude/grok 定时器等后台任务需逐一甄别去留 | 以「存活链路是否调用」为唯一判据；config 字段保留不删（停用无害），后台任务仅保留 codex token 刷新/冷却探测/用量落库/CLI 版本同步 |
| admin/ 与被删 image-studio 页面连锁（admin_images.go 等） | admin 裁剪以 admin/handler.go RegisterRoutes 为锚点，删路由后按编译错误收敛 |
| /v1/models 数据来源（codex_models_manifest.go）裁剪归属 | 归属 T9：handler.go 薄装配版中保留 manifest 端点 |
| 测试文件依赖被删功能导致 go test 失败 | 测试随包同步裁剪，仅保留存活链路的测试；无法独立编译的测试删除并在 tasks 标注 |
| relay 账号语义与 Codex OAuth 分支耦合在 handler.go 内联代码中 | 保留两分支共用的翻译管线，仅删渠道特判 |
| database 列裁剪破坏老库兼容 | 不删已有列（DROP 成本高且风险大），仅停止写入废弃列；schema 增量列表保留 claude_config 等列定义以维持迁移幂等 |
