#!/bin/sh
# 「观复」容器入口脚本。
#
# 按 PUID / PGID 调整运行身份并修正 /data 数据目录属主，使容器既能以非 root 运行，
# 又能对绑定挂载进来的宿主目录拥有写权限 —— SQLite 以 WAL 模式打开时需要对 /data
# 本身可写，否则报 error 14: unable to open database file。

set -e

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

# 已是非 root 启动（如 docker run --user）：无力改属主，直接运行。
if [ "$(id -u)" != "0" ]; then
  exec "$@"
fi

# 调整运行身份的 uid/gid。busybox 不带 shadow 时 groupmod/usermod 不存在，
# 忽略即可 —— 真正决定文件权限的是下面的 chown 与 su-exec 用的数字 uid/gid，
# 与容器内是否存在同名用户无关。
if command -v groupmod >/dev/null 2>&1; then
  groupmod -o -g "$PGID" guanfu 2>/dev/null || true
fi
if command -v usermod >/dev/null 2>&1; then
  usermod -o -u "$PUID" -g "$PGID" guanfu 2>/dev/null || true
fi

# 数据目录及其内容归属到运行身份；历史属主（如 root）残留会挡住 SQLite 写入。
chown -R "${PUID}:${PGID}" /data 2>/dev/null || true

# HOME 指向 /data，保证任何相对路径写入都落在卷上。
export HOME=/data

# exec 替换 PID 1：否则信号收不到，docker stop 会等到 10 秒超时后 SIGKILL，
# SQLite 的 WAL 可能来不及 checkpoint。
exec su-exec "${PUID}:${PGID}" "$@"
