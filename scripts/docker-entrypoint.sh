#!/bin/sh
# 观复容器入口：按 PUID/PGID 对齐宿主机目录所有者后，以该身份降权运行。
# 逻辑与慎始 entrypoint 一致：root 起容器 → chown /data → su-exec 切身份。
set -e

PUID="${PUID:-100}"
PGID="${PGID:-100}"

if [ "$(id -u)" = "0" ]; then
  # 确保运行组存在
  if ! getent group "$PGID" >/dev/null 2>&1; then
    addgroup -g "$PGID" guanfu
  fi
  GROUP_NAME="$(getent group "$PGID" | cut -d: -f1)"

  # 确保运行用户存在
  if ! getent passwd "$PUID" >/dev/null 2>&1; then
    adduser -D -H -u "$PUID" -G "$GROUP_NAME" guanfu
  fi

  # 数据目录归属对齐（挂载卷可能属于宿主机其他 uid）
  chown -R "$PUID:$PGID" /data 2>/dev/null || true

  # HOME 指向 /data，保证任何相对路径写入都落在卷上
  export HOME=/data
  exec su-exec "$PUID:$PGID" "$@"
else
  # 已是非 root 启动（如 docker --user），直接运行
  exec "$@"
fi
