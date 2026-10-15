# 观复（guanfu）多阶段构建：Node 构建前端 → Go 单二进制 → Alpine 运行。
# 运行身份可配置：PUID/PGID（默认 100），/data 为 home 兼数据目录。
FROM node:22-alpine AS web

WORKDIR /build/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN corepack enable && pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build


FROM golang:1.27-alpine AS gobuild

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /build/web/build ./web/build
RUN rm -rf internal/web/dist && mkdir -p internal/web/dist && cp -r web/build/. internal/web/dist/ \
    && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/guanfu ./cmd/guanfu


FROM alpine:3.21

# su-exec：以 PUID/PGID 身份降权运行（与慎始/全家桶一致）
RUN apk add --no-cache su-exec ca-certificates tzdata

COPY --from=gobuild /out/guanfu /usr/local/bin/guanfu

ENV GUANFU_LISTEN=:8788 \
    GUANFU_DB=/data/guanfu.db \
    GUANFU_TZ=Asia/Shanghai \
    GUANFU_STATIC_DIR= \
    PUID=100 \
    PGID=100

VOLUME ["/data"]

COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 8788

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["guanfu"]
