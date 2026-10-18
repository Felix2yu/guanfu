# 「观复」运行时镜像：二进制与前端产物均由 CI 预编译后拼装。
#
# 编译期依赖（Go 工具链、node_modules）全部留在 CI，不进镜像；前端已通过
# go:embed 内嵌进二进制，镜像里不再有 web 目录。因此这里没有任何编译步骤 ——
# bin/guanfu 不存在时 COPY 直接红，而不是构建成功、运行到一半才发现前端缺文件。
#
# 本地要构建镜像，先跑 ./scripts/build.sh 产出 bin/guanfu。
#
# 基镜 ghcr.io/felix2yu/base:alpine-3.24 提供 su-exec、ca-certificates、tzdata
# 与 PUID/PGID 降权入口（观复的 local_date 归日基于本机时区，tzdata 不可缺）。
#
# 容器内布局：
#   /usr/local/bin/guanfu   服务二进制（含内嵌前端）
#   /data                   唯一数据卷：SQLite 数据库文件

FROM ghcr.io/felix2yu/base:alpine-3.24

COPY --chmod=755 bin/guanfu /usr/local/bin/guanfu

ENV GUANFU_LISTEN=:8788 \
    GUANFU_DB=/data/guanfu.db \
    GUANFU_TZ=Asia/Shanghai \
    GUANFU_STATIC_DIR= \
    APP_DATA_DIR=/data

RUN mkdir -p /data
VOLUME ["/data"]

EXPOSE 8788

# 探活走 /api/v1/health：该端点免鉴权，配不配访问口令都能探
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:8788/api/v1/health >/dev/null || exit 1

CMD ["guanfu"]
