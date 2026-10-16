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

全部配置项与各源凭据的占位示例见 `.env.example`（`.env` 已被 gitignore 排除）。

工具链可用 `GO_BIN` / `NODE_BIN` / `PNPM_BIN` 覆盖（沙箱 PATH 常缺 go）。

### Docker

镜像由 GitHub Actions 在推送到 `main` 后构建并发布到 GHCR（`ghcr.io/felix2yu/guanfu`），
amd64 / arm64 已合成多架构 manifest，服务器直接拉取即可：

```bash
cp .env.example .env      # 填各数据源 BASE_URL / KEY

# 本仓库与 GHCR 包都是私有的，服务器上需先用具备 read:packages 的 PAT 登录一次
echo "$GHCR_PAT" | docker login ghcr.io -u Felix2yu --password-stdin

docker compose pull && docker compose up -d
# 打开 http://<服务器地址>:8788
```

镜像是**装配式**的：Dockerfile 只拼装 CI 预编译的二进制（前端已经 `go:embed` 内嵌进去），
镜像内没有 Go / Node 工具链。因此本地自行构建镜像前必须先产出二进制：

```bash
./scripts/build.sh        # 产出 bin/guanfu
docker build -t guanfu .
docker run -d --name guanfu \
  -p 8788:8788 \
  -e HUOZHI_BASE_URL=https://huozhi.example.com \
  -e HUOZHI_API_KEY=<key> \
  -v guanfu-data:/data \
  guanfu
```

`/data` 是唯一持久化目录（SQLite，WAL 模式），已纳入既有备份体系（docker-db-auto-backup）。
绑定挂载宿主目录（如 `-v ./data:/data`）时把 `PUID` / `PGID` 设成该目录属主 uid/gid，
入口脚本会自动 `chown /data` 再降权运行；不设置时默认 `1000:1000`。

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
| `GUANFU_MCP_ENABLED` | `1` | 只读 MCP server（挂在 `/mcp`）总开关 |
| `GUANFU_MCP_TOKEN` | — | 非空时 `/mcp` 要求 `Authorization: Bearer <token>`；stdio 不需要 |
| `HUOZHI_BASE_URL` / `HUOZHI_API_KEY` | — | 货殖记账（`X-API-Key` 认证） |
| `HUOZHI_INTERVAL` | `30m` | 同步间隔 |
| `HUOZHI_LOOKBACK_DAYS` | `90` | 首轮无游标时的回溯天数 |
| `MUJIAN_BASE_URL` / `MUJIAN_API_KEY` | — | 幕间演出（token 可选，未配置则放通） |
| `ZHICHANG_BASE_URL` | — | 知常健康（读取端零鉴权，**无需 key**） |
| `ZHICHANG_METRICS` | 8 项核心指标 | 逗号分隔的指标名列表 |
| `DAWARICH_BASE_URL` / `DAWARICH_API_KEY` | — | 到访地点（Bearer，全权凭证只发 GET） |
| `BILI_HISTORY_BASE_URL` | — | 拾帧集观看历史（REST 零鉴权，**无需 key**） |
| `DIARUM_BASE_URL` / `DIARUM_API_KEY` | — | 吾身日记（Bearer JWT，或 `用户名:密码` 自动登录换 JWT） |
| `IMMICH_BASE_URL` / `IMMICH_API_KEY` | — | Immich 照片（`x-api-key`，只读 key 按天聚合为 photo 事件） |
| `QIANSI_BASE_URL` / `QIANSI_API_KEY` | — | 牵丝人情往来（往来事件/礼金/纪念日；未配 token 的实例放通） |
| `SHENSHI_BASE_URL` / `SHENSHI_API_KEY` | — | 慎始任务（任务完成/复盘/习惯打卡；未配 token 的实例放通） |
| `LIUXIA_BASE_URL` | — | 流霞霞光预报（REST 零鉴权，**无需 key**） |
| `QINGYE_BASE_URL` | — | 青野集园艺（植物日记 + 养护记录；REST 零鉴权，**无需 key**） |

各源**配了 BaseURL 即自动启用**（`<NAME>_ENABLED=0` 可显式关闭）；默认回溯天数：mujian 365 / diarum 365 / zhichang 30 / dawarich 30 / bili-history 90 / immich 365（可用 `<NAME>_LOOKBACK_DAYS` 覆盖）。

## 架构

```
cmd/guanfu/         入口：配置 → store → 引擎 → API → 优雅停机
internal/
  config/           环境变量配置（凭据只进 env）
  model/            统一事件模型（events）+ 时区 + GCJ-02→WGS-84
  store/            SQLite：events（幂等 upsert）+ sources（游标/状态）
  sync/             调度引擎：每源独立 ticker + 手动触发 + 失败隔离
  sources/          数据源适配器（huozhi/mujian/diarum/immich/zhichang/dawarich/bili-history/qiansi/shenshi/liuxia/qingye）
  api/              REST API（/api/v1/events 等）+ 媒体代理
  mcp/              只读 MCP server：5 个查询/聚合工具，HTTP(/mcp) 与 stdio 两种传输
  web/              前端托管：磁盘目录优先，go:embed 内嵌兜底（all:dist）
web/                SvelteKit 5 SPA（时间轴 + 日历热力图）
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
| GET | `/api/v1/events/counts?from=&to=` | 范围内每日事件总数（日历热力图数据源） |
| GET | `/api/v1/range` | 库日期范围（日历边界） |
| GET | `/api/v1/sources` | 源同步状态（游标/错误/条数） |
| GET | `/api/v1/export?from&to&format=json` | 全量或区间导出（流式 JSON，防锁定；页面右上「导出」按钮即此端点） |
| POST | `/api/v1/sync/{source}` | 手动同步（`all` = 全部） |
| GET | `/api/v1/media/mujian/{key}` | 演出票根/封面（回源 mujian `/uploads/`） |
| GET | `/api/v1/media/bili-history?u=` | 视频封面（回源 bili-history images/proxy） |
| GET | `/api/v1/media/immich/{assetID}?size=` | 照片缩略图/原图（`thumbnail`/`preview`/`fullsize`/`original`） |
| GET | `/api/v1/media/qingye/{file}` | 园艺日记配图（回源 qingye `/uploads/<file>`，上游无缩略图端点，只流转原图） |
| POST | `/mcp` | 只读 MCP server（Streamable HTTP，见下节） |

## MCP（只读）

让 AI 客户端直接问「上周看了几场演出」「这个月花了多少钱」。工具全部只读，
只有查询与聚合，没有同步触发、写入或凭据类工具。

| 工具 | 参数 | 返回 |
|---|---|---|
| `guanfu_range` | — | 库覆盖日期、总条数、各源条数 |
| `guanfu_timeline` | `date`（必填）、`limit` | 当日收支合计 + 全天事件 + 时刻流明细 |
| `guanfu_stats` | `from`/`to`（必填）、`group_by`、`source`、`event_type` | 区间按 source/event_type/day/month 的条数与收支合计 |
| `guanfu_events` | `from`/`to`（必填）、`source`、`event_type`、`q`、`limit`、`offset` | 关键词与条件检索的明细 + 同口径 total_count |
| `guanfu_sources` | — | 各源启用/间隔/游标/上次同步距今/错误 |

约定（写进返回值，避免模型自行脑补）：

- 日期只认 `YYYY-MM-DD`，`2026-10-1`、RFC3339、unix 秒一律拒绝并回示例；
- 金额单位一律**分**（`amount_cents`），返回体自带 `amount_unit`；
- 收支口径 `totals_rule`：`detail.type == "income"` 记收入，其余带金额事件记支出
  （huozhi 的转账/报销因此也进支出，与网页时间轴顶栏同口径）；
- 币种分桶返回，不同币种绝不加进同一个数；
- 明细带 `returned`/`truncated`，`limit` 截断时不会让模型把部分当全部；
- 返回体不含 `detail_json` 与媒体 URL（长尾字段动辄几 KB，图片须走观复代理）。

两种传输：

```bash
# 1) Streamable HTTP：随主服务启动，默认开启。
#    有状态会话：先 initialize 拿 Mcp-Session-Id，后续请求都要带上它。
SID=$(curl -sD- -o/dev/null -X POST http://127.0.0.1:8788/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  | grep -i '^mcp-session-id' | tr -d '\r' | awk '{print $2}')

curl -sX POST http://127.0.0.1:8788/mcp \
  -H "Mcp-Session-Id: $SID" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

curl -sX POST http://127.0.0.1:8788/mcp \
  -H "Mcp-Session-Id: $SID" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":
       {"name":"guanfu_stats","arguments":{"from":"2026-10-01","to":"2026-10-31","group_by":"event_type"}}}'

# 2) stdio：供 Claude Desktop 一类本地客户端直连，不起 HTTP、不启同步引擎
guanfu mcp
```

配了 `GUANFU_MCP_TOKEN` 时 HTTP 侧要求 `Authorization: Bearer <token>`（定长时间比较，
缺前缀即拒）。此时会关掉 mcp-go 的 DNS rebinding 保护——否则「反代到 127.0.0.1 监听 +
公网 Host」这种正常拓扑会被它 403，而门禁已经由 token 承担。未配 token 时按内网只读放行，
与既有 `/api/v1/*` 的暴露面同级（观复自身没有任何登录态，见下节安全约定）。

## 导出（防锁定）

`GET /api/v1/export` 把库里的事件原样吐出来，页面右上「导出」按钮就是它（范围取库的实际边界）。

- `from` / `to` 可省略，省略即全库；给了就必须是**有效日期**，`2026-1-5`、`2026-13-45`
  这种形态像日期但不存在的值一律 400 —— 放过它们只会产出空文件，调用方会误读成
  "那段时间确实没数据"。
- `format` 目前只有 `json`，别的值直接拒收（不静默按 json 发）。
- 逐条流式编码，内存与库大小无关；`ListEvents` 那条 1000 条上限是时间轴的护栏，
  不是导出的天花板（实测 1050 条一次导全）。
- `meta` 里带 `generated_at` / `timezone` / 实际范围 / `count` / 金额单位，
  便于核对"导全了吗、按哪个时区归的日"。
- `media[].id` 保留各源侧引用（uploads 键、Immich 资产 UUID、B 站封面外链），
  观复自己的代理路径会剥掉——那些 URL 只在观复跑着时才有意义。

## 安全约定

- 所有 API key 只经环境变量传入；**不写入仓库、不写入文档、不进镜像层**
- 各源 key 均为全权凭证（huozhi/mujian/dawarich/immich 概莫如此），观复只发 GET 请求
- 媒体代理做路径穿越防护（mujian key 字符集 + `..` 拒绝）与 SSRF 防护（bili 封面仅限 `*.hdslb.com` + https）
- MCP 只有 5 个只读查询工具，且返回体刻意剥掉 `detail_json` 与媒体路径；要收紧就在 `GUANFU_MCP_TOKEN` 配一个值，或 `GUANFU_MCP_ENABLED=0` 整条关掉
- SQLite 文件位于 `/data` 卷，纳入既有备份体系（docker-db-auto-backup）

## 开发

```bash
/opt/homebrew/bin/go test ./internal/...   # 单测（适配器 fixture + store 幂等 + 前端托管契约）
/opt/homebrew/bin/go vet ./...
cd web && pnpm check                      # svelte-check 类型检查
```

工具链版本由仓库自己的声明决定：仓库根 `.nvmrc`（Node 26）、`web/package.json` 的
`packageManager`（pnpm 11.22.0）、`web/pnpm-lock.yaml`。CI 用 `--frozen-lockfile` 安装，
**不设兜底版本**——这三者缺任何一项 CI 直接失败。

`internal/web/dist/` 下只提交 `.gitkeep` 占位，真实前端产物由 `./scripts/build.sh`
或 CI 生成后 `go:embed` 进二进制；未构建时服务只提供 API（启动日志会写明）。

CI 走组织共享的可复用 workflow（`Felix2yu/.github`）：`test`（go vet + go test + 覆盖率
上报 + 前端 check/build）、`image`（预编译二进制 → 装配镜像 → 双架构 manifest 推 GHCR）、
`release`（发布 Release 时出 5 平台二进制）。版本号不在任何文件里，由 git tag 经
`-ldflags -X main.version` 注入（本地构建为 `dev`，`guanfu -version` 可查）。
