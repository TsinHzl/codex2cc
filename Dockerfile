# ---- 前端构建阶段 ----
FROM node:22-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ---- Go 构建阶段 ----
FROM golang:1.26-alpine AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
# modernc.org/sqlite 为纯 Go 驱动，无需 CGO；禁用后交叉构建不依赖 gcc
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /codex2cc .

# ---- 运行阶段 ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata sqlite-libs
WORKDIR /app
COPY --from=go-builder /codex2cc /app/codex2cc
ENV CODEX_BIND=0.0.0.0 \
    CODEX_PORT=8989 \
    DATABASE_DRIVER=sqlite \
    DATABASE_PATH=/app/data/codex2cc.db \
    CACHE_DRIVER=memory \
    TZ=Asia/Shanghai
VOLUME ["/app/data"]
EXPOSE 8989
ENTRYPOINT ["/app/codex2cc"]
