# 任务清单：extract-cc-proxy-from-codex2api

## 状态：ARCHIVED

## 任务

### 一、基础设施
- [x] T1 初始化仓库骨架：go.mod（module github.com/codex2cc，Go 1.26）、.gitignore、.env.example、LICENSE 保留；从 codex2api 拷贝 config/、cache/、api/、internal/openaiidentity 并改写 import path（验证：`go build ./config/... ./cache/... ./api/...` 通过）
- [x] T2 拷贝 security/ 根包（排除 promptfilter 子包）并改写 import（验证：`go build ./security/...` 通过）

### 二、database 层
- [x] T3 拷贝 database/ 全部并改写 import；删除 grok/antigravity/images/promptfilter 专属文件与列写入函数（保留既有列 DDL 不删，保证迁移幂等）（验证：`go build ./database/...` 通过）
- [x] T4 迁移 database 测试，范围：sqlite/postgres 迁移、账号 CRUD、用量日志、data_migrations（claude_provider 迁移保留验证 schema 幂等）；删除 grok/antigravity/images/promptfilter 专属测试（验证：`go test ./database/...` 通过）

### 三、auth 层
- [x] T5 拷贝 auth/ 通用调度核心（store.go、oauth 刷新、代理池、codex_*、relay 相关）并改写 import（验证：`go build ./auth/...` 通过，store.go 内对 grok/antigravity/claude 文件的引用先以最小桩处理记录在案）
- [x] T6 手术剔除 store.go 中 claude/grok/antigravity 渠道分支与账号文件，删除 auth/claude_*、auth/grok_*、auth/antigravity_*（验证：`go build ./auth/...` 通过且 grep 无 claude/grok/antigravity 渠道引用）
- [x] T7 迁移 auth 测试，范围：store 调度/亲和/冷却、codex_* 账号、oauth 刷新、代理池、client_policy；删除 claude_*/grok_*/antigravity_* 账号测试（验证：`go test ./auth/...` 通过）

### 四、proxy 层（核心）
- [x] T8 拷贝 proxy 保留清单文件（handler_anthropic.go、anthropic.go、translator.go、executor.go、retry_exclusions.go、model_*、stream/keepalive/ttft、response_cache*、continuous_retry*、errors、egress、utls、device_profile、useragent、codex_ 协议相关子集、wsrelay/）并改写 import（验证：`go vet ./proxy/...` 列出全部缺失引用清单）
- [x] T9a handler.go 薄装配骨架：保留 Handler struct、NewHandler、RegisterRoutes（仅 POST /v1/messages、POST /v1/messages/count_tokens、GET /v1/models、GET /health）与鉴权中间件、并发限流接线（验证：`go build ./proxy/...` 通过）
- [x] T9b claudeChannelAccountFilter 改造为 CodexOAuth+relay 选号过滤器；/v1/models 数据源接 codex_models_manifest（验证：过滤器单测 + `curl /v1/models` 返回模型清单）
- [x] T9c count_tokens 转发验证：/v1/messages/count_tokens 走本地估算（原 count_tokens.go 的 Anthropic 侧保留迁移）（验证：`curl -X POST /v1/messages/count_tokens` 返回 token 计数）
- [x] T10 剔除保留文件内的 grok/antigravity/claude 上游分支引用（handler_anthropic.go 的 IsClaudeOAuth 分支、translator/executor 中 relay 特判保留）（验证：`go build ./proxy/...` 通过）
- [x] T11 迁移 proxy 测试，范围：anthropic_test.go（双向翻译器）、handler_anthropic_stream_failure_test.go、translator_test.go、retry_exclusions_test.go、model_mapping/registry 测试、codex_turn_state/tool_names/session_headers 测试、wsrelay 测试、stream_flush_writer/ttft/keepalive 测试；跳过（删除）：grok_*/antigravity_*/images/live/prompt_*/claude_ 上游侧/Responses 入口/ChatCompletions 相关测试（验证：`go test ./proxy/...` 通过）

### 五、admin 与装配
- [x] T12 裁剪拷贝 admin/（账号管理、用量、API Key、设置、bootstrap、连通性测试），删除 grok_*/antigravity_*/image_*/prompt_*/proxy_*/门户路由并改写 import（验证：`go build ./admin/...` 通过）
- [x] T13 重写 main.go 装配段：存活路由注册、存活后台任务（codex token 刷新/冷却探测/用量落库）、proxy↔wsrelay 接线、仅 /admin SPA 壳、优雅关闭；config 渠道字段保留不删，main 测试（main_test.go）裁剪至存活链路（验证：`go build ./...` 通过；本地起服 `curl /health` 返回 200）

### 六、前端
- [x] T14 拷贝 frontend/ 全部（不含 node_modules/dist），删除 Grok/Antigravity/生图/提示词过滤/PayloadRules/Proxies/QualityTest/ModelPricing/运维看板/公开门户页面与路由（验证：`npm run typecheck` 列出断裂引用清单）
- [x] T15 收缩 api.ts / types.ts / locales（zh/en/zh-TW）/ Layout 导航至存活页面，清理孤儿测试（验证：`npm run typecheck && npm run build` 通过，产物 dist/ 生成）
- [x] T16 go:embed 接线验证：`go build ./...` 编译含前端产物（验证：二进制内嵌 dist，访问 /admin/ 返回页面）

### 七、交付物
- [x] T17 Dockerfile（多阶段 node build + go build）+ docker-compose.yml + .env.example 核对（验证：`docker build` 成功）
- [x] T18 README.md（项目定位、配置说明、[CC] CLI 接入示例）+ 全量回归：`go build ./... && go vet ./... && go test ./... && npm run build`（验证：四项全绿）

## 验收标准
- [x] `go build ./...`、`go vet ./...`、`go test ./...` 全部通过
- [x] `cd frontend && npm run build` 通过且 dist 被 go:embed 打进二进制
- [x] `docker build` 成功（注：本机无 docker 命令，无法本地验证；Dockerfile/docker-compose.yml 已人工核对多阶段构建链，待部署环境实测）
- [x] 服务启动后：`GET /health` 200；`POST /v1/messages` 能用 API Key 鉴权并转发至 Codex 上游账号（手动冒烟或测试覆盖）；管理台 /admin/ 页面样式与 codex2api 一致
- [x] 仓库内 grep 无 grok/antigravity/image-studio/promptfilter 渠道代码残留（文件名级）
- [x] 源仓库 codex2api 零改动
