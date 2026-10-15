# 观复（guanfu）

个人数据聚合应用：在同一时间轴按天聚合多源个人数据——记账、到访地点、照片、健康指标、演出、日记等。

规划与数据源调研见 [docs/观复规划.md](docs/观复规划.md)（含 27 个仓库盘点、接入方案、缺口评估）。

## 技术栈

- **后端**：Go 1.27 单二进制，标准库 `net/http`，modernc.org/sqlite（纯 Go，无 CGO）
- **前端**：SvelteKit 5 + Tailwind v4，SPA 模式（adapter-static），产物 `go:embed` 内嵌
- **部署**：Docker 单容器，su-exec 按 `PUID/PGID` 降权，`/data` 卷持久化 SQLite

## 快速开始

### 本地构建运行

```bash
# 全量构建（前端 + 内嵌 + go build），产出 bin/guanfu
./scripts/build.sh

# 运行（凭据只走 env，不落文件）
HUOZHI_BASE_URL=https://huozhi.example.com \
HUOZHI_API_KEY=<key> \
./bin/guanfu
# 打开 http://127.0.0.1:8788
```

工具链可用 `GO_BIN` / `NODE_BIN` / `PNPM_BIN` 覆盖（沙箱 PATH 常缺 go）。

### Docker

```bash
docker build -t guanfu .
docker run -d --name guanfu \
  -p 8788:8788 \
  -e PUID=1000 -e PGID=100 \
  -e HUOZHI_BASE_URL=https://huozhi.example.com \
  -e HUOZHI_API_KEY=<key> \
  -v /path/to/data:/data \
  guanfu
```

### 前端单独开发（热更新）

```bash
cd web && pnpm install && pnpm dev
# vite 已配置 /api -> http://127.0.0.1:8788 代理，需先跑起 Go 端
```

## 配置（全部环境变量）

| 变量 | 默认 | 说明 |
|---|---|---|
| `GUANFU_LISTEN` | `:8788` | HTTP 监听地址 |
| `GUANFU_DB` | `./data/guanfu.db` | SQLite 文件路径 |
| `GUANFU_TZ` | `Asia/Shanghai` | 本地时区（local_date 归日） |
| `GUANFU_STATIC_DIR` | `./web/dist` | 磁盘前端目录（存在则优先于内嵌） |
| `GUANFU_SYNC_ENABLED` | `1` | 同步引擎总开关 |
| `HUOZHI_BASE_URL` / `HUOZHI_API_KEY` | — | 货殖记账（`X-API-Key` 认证） |
| `HUOZHI_INTERVAL` | `30m` | 同步间隔 |
| `HUOZHI_LOOKBACK_DAYS` | `90` | 首轮无游标时的回溯天数 |

其余源（mujian/diarum/zhichang/dawarich/bili-history/immich）按 `<NAME>_BASE_URL` / `<NAME>_API_KEY` / `<NAME>_ENABLED=1` 模式追加，适配器落地后自动进入调度（路线图见 docs/观复规划.md §7）。

## 架构

```
cmd/guanfu/         入口：配置 → store → 引擎 → API → 优雅停机
internal/
  config/           环境变量配置（凭据只进 env）
  model/            统一事件模型（events）+ 时区
  store/            SQLite：events（幂等 upsert）+ sources（游标/状态）
  sync/             调度引擎：每源独立 ticker + 手动触发 + 失败隔离
  sources/          数据源适配器（M1：huozhi）
  api/              REST API（/api/v1/events 等）
  web/              前端托管：磁盘目录优先，go:embed 内嵌兜底（all:dist）
web/                SvelteKit 5 SPA
```

### 统一事件模型

所有源落到 `events` 一张表，`(source, external_id)` 唯一约束保证同步幂等：

- `occurred_at`（UTC unix 秒）+ `local_date`（本地时区归日）双时间字段
- 坐标统一 WGS-84；金额统一整数分 + currency
- `media_json` 走观复代理端点（`/api/v1/media/...`），源站 token 不外泄
- 各源长尾字段进 `detail_json`

### 同步语义

- 每源独立 goroutine + ticker，单源失败只记 `sources.last_error`，不影响其他源
- cursor 持久化（huozhi 为成功同步日期），失败保留原 cursor，不会全量回溯
- 幂等：重复拉取同一 external_id 只做更新，`/api/v1/sync/{source}` 可手动触发

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/health` | 健康检查 |
| GET | `/api/v1/events?date=YYYY-MM-DD` | 事件列表（支持 from/to/source/type/limit/offset） |
| GET | `/api/v1/events/counts?date=` | 某日各源事件数 |
| GET | `/api/v1/range` | 库日期范围（日历边界） |
| GET | `/api/v1/sources` | 源同步状态（游标/错误/条数） |
| POST | `/api/v1/sync/{source}` | 手动同步（`all` = 全部） |

## 安全约定

- 所有 API key 只经环境变量传入；**不写入仓库、不写入文档、不进镜像层**
- 各源 key 均为全权凭证（huozhi/mujian/dawarich/immich 概莫如此），观复只发 GET 请求
- SQLite 文件位于 `/data` 卷，纳入既有备份体系（docker-db-auto-backup）

## 开发

```bash
/opt/homebrew/bin/go test ./internal/...   # 单测（适配器 fixture + store 幂等 + 前端托管契约）
/opt/homebrew/bin/go vet ./...
cd web && pnpm check                      # svelte-check 类型检查
```
