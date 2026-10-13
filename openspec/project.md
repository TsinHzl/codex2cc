# 项目上下文

## 项目定位

codex2cc：从 codex2api 仓库抽离「[CC] CLI → Codex/ChatGPT 上游」代理网关功能形成的独立项目。
下游暴露 Anthropic Messages 协议（`/v1/messages`，供 [CC] CLI 使用），上游为 Codex/ChatGPT OAuth 账号池 + relay 中转账号。不含 Grok / Antigravity / Gemini / 生图 / 提示词过滤渠道。

## 技术栈

- 后端：Go 1.26 + Gin v1.12，module path `github.com/codex2cc`
- 数据库：modernc.org/sqlite + pgx/v5 双驱动（`DATABASE_DRIVER` 切换 sqlite/postgres），无 CGO
- 缓存：go-redis/v9（TokenCache 跨实例：会话亲和/冷却/刷新锁），单机内存实现
- 网络出口：utls（Chrome TLS 指纹伪装）、gjson/sjson 请求体路径式读改写、gorilla/websocket
- 前端：React 19 + Vite 8 + TypeScript 5.9 + Tailwind 4（CSS-first，无 tailwind.config.js）+ shadcn 风格组件 + i18next，产物 `frontend/dist` 由 `//go:embed` 打进单个二进制

## 架构约定

- 三件套：proxy.Handler（转发调度）/ auth.Store（账号池调度：健康分+亲和+冷却）/ cache.TokenCache（跨实例状态）
- 依赖注入 + `AccountFilter` 闭包链做选号过滤；auth 包零依赖 proxy（避免 import cycle）
- 唯一循环点 proxy ↔ proxy/wsrelay，用 `proxy.WebsocketExecuteFunc` 函数字段在 main.go 运行时接线
- 错误统一走 api/ 包的 [OI]/Anthropic 风格信封，禁止裸 c.JSON
- 账号状态为纯函数推导（runtimeStatusLocked），不存状态字段
- SSE 全链路流式：drainable 上游 ctx、keepalive 注释行、首字超时看门狗

## 目录结构

```
codex2cc/
├─ main.go          # 启动装配（配置→DB→缓存→Store→后台任务→路由→优雅关闭）
├─ config/          # .env 物理层配置
├─ database/        # sqlite+postgres 双驱动、SystemSettings、计费、用量日志
├─ cache/           # TokenCache 接口 + memory/redis 双实现
├─ auth/            # 账号池核心（仅 Codex/ChatGPT + relay 渠道）
├─ proxy/           # 转发核心（仅 Messages↔Responses 翻译链路）
├─ api/             # 错误信封、中间件、声明式校验
├─ security/        # 请求体限制、日志脱敏（不含 promptfilter）
├─ internal/        # openaiidentity 等必要子集
├─ admin/           # 管理后台 REST API（裁剪后子集）
├─ frontend/        # React 管理台（裁剪后子集，样式完整保留）
└─ cmd/             # 辅助 CLI
```

## 开发约定

- 代码风格遵循源仓库 codex2api；仅改 import path 与裁剪相关代码，不做无关重构
- 提交格式：`<type>(<scope>): <desc>`
- 测试：迁移源仓库相关 _test.go，保证 `go test ./...` 通过
- 构建：`go build ./...` + `cd frontend && npm run build` 均须通过
