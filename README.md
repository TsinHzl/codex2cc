# Codex2CC

[CC] CLI → Codex/ChatGPT 上游代理网关（从 codex2api 抽离）。

## 项目定位

Codex2CC 将 Anthropic Messages 协议（`/v1/messages`）的请求转发到 Codex/ChatGPT 上游，供 Claude Code 等 [CC] CLI 直接使用。支持三类上游渠道：

- **codex** — Codex/ChatGPT OAuth 账号
- **relay** — 外部中转上游账号池（`upstream_type = "relay"`）
- **claude** — Claude API 直连

内嵌 React 19 + Vite + Tailwind 4 管理后台（go:embed，单二进制交付），访问 `/admin/`。

## 技术栈

- 后端：Go 1.26 + Gin；sqlite/postgres 双驱动；缓存 memory/redis 双驱动
- 前端：React 19 + Vite 7 + Tailwind 4，构建产物由 Go 二进制内嵌
- 下游协议：Anthropic Messages（`/v1/messages`）

## 快速开始

### 1. 配置

```bash
cp .env.example .env
# 按需修改数据库/缓存/端口配置
```

核心环境变量（完整清单见 `.env.example`）：

| 变量 | 说明 | 默认值 |
|---|---|---|
| `CODEX_PORT` | 监听端口 | `8989` |
| `CODEX_BIND` | 监听地址 | `0.0.0.0` |
| `ADMIN_SECRET` | 管理后台密钥（留空则首启引导初始化） | 空 |
| `DATABASE_DRIVER` | `postgres` / `sqlite` | `postgres` |
| `DATABASE_HOST` / `DATABASE_PATH` | PG 主机 / SQLite 文件路径 | — |
| `CACHE_DRIVER` | `memory` / `redis` | `redis` |
| `REDIS_ADDR` | Redis 地址（`CACHE_DRIVER=redis` 时必填） | — |
| `CODEX_CRED_ENCRYPTION_KEY` | 上游凭据 AES 加密密钥（可选） | 空（不加密） |

### 2. 本地运行

```bash
go build -o codex2cc .
./codex2cc
```

### 3. Docker 部署

服务器零构建：镜像由 GitHub Actions 自动构建并推送至 GHCR（`linux/amd64` + `linux/arm64`），详见 [docs/部署指南_服务端.md](docs/部署指南_服务端.md)。

```bash
docker compose pull
docker compose up -d
```

默认使用 SQLite + 内存缓存，数据持久化在 `codex2cc-data` 卷；端口绑定 `127.0.0.1`，公网访问请前置反代加 TLS。

本地从源码构建镜像时，将 `docker-compose.yml` 中 `image:` 换回 `build: .` 后执行 `docker compose up -d --build`。

> 注：`image:` 默认指向 `ghcr.io/tsinhzl/codex2cc`。fork 仓库需覆盖变量：`IMAGE_OWNER=<你的GitHub用户名小写> docker compose up -d`，或自行修改 compose 文件。

### 4. 管理后台

首次访问 `http://<host>:8989/admin/`，按引导设置管理员密钥并添加上游账号。

## [CC] CLI 接入示例

Claude Code 通过环境变量指向本网关：

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8989
export ANTHROPIC_AUTH_TOKEN=<管理后台生成的 API Key>
claude
```

或在 `~/.claude/settings.json` 中配置 `env` 字段。API Key 在管理后台「API Keys」页面创建。

## 开发

```bash
# 后端验证链
go build ./... && go vet ./... && go test ./... -count=1

# 前端（frontend/ 目录下）
npm install
npm run build     # 产物输出到 frontend/dist/，需重新 go build 内嵌
npm run test      # node --test
```

前端改动后需重新构建 dist 并重新编译 Go 二进制（go:embed 编译期固化）。

## 致谢

本项目基于 [james-6-23/codex2api](https://github.com/james-6-23/codex2api) 抽离而来，感谢原作者 [james-6-23](https://github.com/james-6-23) 的开源贡献。
