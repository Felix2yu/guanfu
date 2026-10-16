# 「观复」运行时镜像：二进制与前端产物均由 CI 预编译后拼装。
#
# 编译期依赖（Go 工具链、node_modules）全部留在 CI，不进镜像；前端已通过
# go:embed 内嵌进二进制，镜像里不再有 web 目录。因此这里没有任何编译步骤 ——
# bin/guanfu 不存在时 COPY 直接红，而不是构建成功、运行到一半才发现前端缺文件。
#
# 本地要构建镜像，先跑 ./scripts/build.sh 产出 bin/guanfu。
#
# 容器内布局：
#   /usr/local/bin/guanfu   服务二进制（含内嵌前端）
#   /data                   唯一数据卷：SQLite 数据库文件

FROM alpine:3.24

# su-exec：按 PUID/PGID 降权运行（见 scripts/docker-entrypoint.sh）
# tzdata：观复的 local_date 归日全部基于本机时区，缺了会静默退化成 UTC
#         （Go 的 time.LoadLocation("Asia/Shanghai") 直接报 unknown time zone，
#          且只在容器里报，本机复现不了）
RUN apk add --no-cache su-exec ca-certificates tzdata \
    && addgroup -g 1000 guanfu \
    && adduser -D -H -u 1000 -G guanfu guanfu

COPY --chmod=755 bin/guanfu /usr/local/bin/guanfu
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

ENV TZ=Asia/Shanghai \
    GUANFU_LISTEN=:8788 \
    GUANFU_DB=/data/guanfu.db \
    GUANFU_TZ=Asia/Shanghai \
    GUANFU_STATIC_DIR= \
    PUID=1000 \
    PGID=1000

# 数据落在挂载卷里，容器重建不丢数据；预置属主避免首次启动前 /data 属 root
RUN mkdir -p /data && chown -R guanfu:guanfu /data
VOLUME ["/data"]

EXPOSE 8788

# 探活走 /api/v1/health：该端点免鉴权，配不配访问口令都能探
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:8788/api/v1/health >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["guanfu"]
