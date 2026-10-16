// Package mcp 实现观复的只读 MCP server（规划 §6 P2-8）。
//
// 定位：让 AI 客户端直接问「上周看了几场演出 / 这个月花了多少钱」，
// 因此只提供查询与聚合，绝不暴露同步触发、写入或凭据。
//
// 两种传输，共用同一份工具注册表（Tools）：
//   - Streamable HTTP：挂在主服务 /mcp，与全家桶其他仓库同路径；
//   - stdio：`guanfu mcp` 子命令，供 Claude Desktop 一类本地客户端直连。
package mcp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/Felix2yu/guanfu/internal/model"
	"github.com/Felix2yu/guanfu/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// maxEvents 单次返回的事件条数上限（默认值与硬上限）。
// 上限存在的理由：一天的到访/观看记录可能上百条，全量塞进上下文会挤掉对话本身。
const (
	defaultEvents = 50
	maxEvents     = 200
)

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Tools 返回全部只读工具。HTTP、stdio 与测试都从这里取，避免三处注册漂移。
func Tools(st *store.Store) []server.ServerTool {
	dateDesc := "日期，必须写成 YYYY-MM-DD（示例 2026-10-16）；其他格式一律拒绝"
	fromDesc := "起始日（含），YYYY-MM-DD，如 2026-10-01"
	toDesc := "结束日（含），YYYY-MM-DD，如 2026-10-16"

	rangeTool := mcp.NewTool("guanfu_range",
		mcp.WithDescription("查看观库里有哪些数据：日期覆盖范围、总条数、各源条数。做任何时间相关的回答前先用它确认数据是否存在。"),
	)

	timelineTool := mcp.NewTool("guanfu_timeline",
		mcp.WithDescription("某一天的完整时间轴：当日收支合计、按源条数、事件明细（时刻/标题/摘要/金额/地点）。回答「今天/昨天干了什么」用它。"),
		mcp.WithString("date", mcp.Required(), mcp.Description(dateDesc)),
		mcp.WithNumber("limit", mcp.Description(fmt.Sprintf("最多返回多少条事件，默认 %d，上限 %d", defaultEvents, maxEvents))),
	)

	statsTool := mcp.NewTool("guanfu_stats",
		mcp.WithDescription("区间聚合：按 source / event_type / day / month 分组的条数与收支合计（单位分）。"+
			"回答「上周看了几场演出」「这个月花了多少钱」「今年哪个月最忙」用它，不要逐天调 timeline。"),
		mcp.WithString("from", mcp.Required(), mcp.Description(fromDesc)),
		mcp.WithString("to", mcp.Required(), mcp.Description(toDesc)),
		mcp.WithString("group_by", mcp.Enum(store.StatGroupBys()...),
			mcp.Description("聚合维度，默认 event_type；month 用于跨月对比，day 用于逐日曲线")),
		mcp.WithString("source", mcp.Description("只看某个源，如 huozhi/mujian/diarum；留空为全部")),
		mcp.WithString("event_type", mcp.Description("只看某种事件，如 transaction/performance/video_watch；留空为全部")),
	)

	eventsTool := mcp.NewTool("guanfu_events",
		mcp.WithDescription("区间内事件明细检索，可按源、类型、标题摘要关键词过滤。"+
			"先用 stats 定位区间，再用它取具体条目；结果按发生时刻升序，超出 limit 时 returned=false。"),
		mcp.WithString("from", mcp.Required(), mcp.Description(fromDesc)),
		mcp.WithString("to", mcp.Required(), mcp.Description(toDesc)),
		mcp.WithString("source", mcp.Description("源名，如 huozhi / bili-history")),
		mcp.WithString("event_type", mcp.Description("事件类型，如 transaction / performance")),
		mcp.WithString("q", mcp.Description("标题与摘要的子串关键词，按字面量匹配（% 与 _ 不是通配符）")),
		mcp.WithNumber("limit", mcp.Description(fmt.Sprintf("最多返回多少条，默认 %d，上限 %d", defaultEvents, maxEvents))),
		mcp.WithNumber("offset", mcp.Description("翻页偏移量，配合返回的 total_count 使用")),
	)

	sourcesTool := mcp.NewTool("guanfu_sources",
		mcp.WithDescription("各数据源的同步健康：是否启用、间隔、游标、上次成功时间、上次错误。回答「某源多久没同步了」或数据缺失排查用它。"),
	)

	return []server.ServerTool{
		{Tool: rangeTool, Handler: handleRange(st)},
		{Tool: timelineTool, Handler: handleTimeline(st)},
		{Tool: statsTool, Handler: handleStats(st)},
		{Tool: eventsTool, Handler: handleEvents(st)},
		{Tool: sourcesTool, Handler: handleSources(st)},
	}
}

// NewServer 组装 MCP server。version 与二进制版本号保持一致（guanfu -version）。
func NewServer(st *store.Store, version string) *server.MCPServer {
	s := server.NewMCPServer("guanfu", version,
		server.WithRecovery(),
		server.WithToolCapabilities(false),
		server.WithInstructions("观复是把个人记账、演出、日记、健康、位置、照片等多源数据"+
			"按天聚合到一条时间轴的应用。所有工具均为只读；金额单位一律是「分」，"+
			"日期口径是本地时区归日（local_date），时间是 24 小时制本地时刻。"+
			"需要汇总数字时用 guanfu_stats，不要自己逐条累加。"),
	)
	s.AddTools(Tools(st)...)
	return s
}

// HTTPHandler 用 Streamable HTTP 传输暴露 MCP（路径由调用方决定，观复主服务用 /mcp）。
// token 非空时要求 Authorization: Bearer <token>，否则视为内网只读服务直接放行。
func HTTPHandler(s *server.MCPServer, token string) http.Handler {
	var opts []server.StreamableHTTPOption
	if token != "" {
		// DNS rebinding 保护会把「环回监听 + 非环回 Host」判成 403，
		// 反向代理到 127.0.0.1:8788 的正常访问会被误杀；此时门禁已由 Bearer token 承担。
		opts = append(opts, server.WithDisableLocalhostProtection(true))
	}
	mcpHandler := server.NewStreamableHTTPServer(s, opts...)
	if token == "" {
		return mcpHandler
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="guanfu-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
}

// ServeStdio 以 stdio 传输运行（`guanfu mcp`）。stdin 关闭即返回。
func ServeStdio(ctx context.Context, s *server.MCPServer) error {
	errCh := make(chan error, 1)
	go func() { errCh <- server.ServeStdio(s) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------- 工具实现 ----------

// compactEvent 面向 LLM 的精简事件视图。
// 刻意不带 detail_json 与媒体：长尾字段动辄几 KB，且图片须经观复代理才有意义。
type compactEvent struct {
	ID            int64  `json:"id"`
	Date          string `json:"local_date"`
	Time          string `json:"local_time,omitempty"`
	TimePrecision string `json:"time_precision"`
	Source        string `json:"source"`
	EventType     string `json:"event_type"`
	Title         string `json:"title"`
	Summary       string `json:"summary,omitempty"`
	AmountCents   int64  `json:"amount_cents,omitempty"`
	Currency      string `json:"currency,omitempty"`
	LocationName  string `json:"location_name,omitempty"`
	SourceURL     string `json:"source_url,omitempty"`
	MediaCount    int    `json:"media_count,omitempty"`
}

func toCompact(e *model.Event) compactEvent {
	return compactEvent{
		ID:            e.ID,
		Date:          e.LocalDate,
		Time:          e.LocalTime,
		TimePrecision: e.TimePrecision,
		Source:        e.Source,
		EventType:     e.EventType,
		Title:         e.Title,
		Summary:       e.Summary,
		AmountCents:   e.AmountCents,
		Currency:      e.Currency,
		LocationName:  e.LocationName,
		SourceURL:     e.SourceURL,
		MediaCount:    len(e.Media),
	}
}

func handleRange(st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		min, max, err := st.DateRange()
		if err != nil {
			return toolError(err), nil
		}
		out := map[string]any{"min_date": min, "max_date": max, "total_count": 0, "by_source": []store.StatRow{}}
		if min == "" {
			out["note"] = "库里还没有任何事件，先确认源已配置并跑过同步（guanfu_sources 可看健康状态）"
			return jsonResult(out)
		}
		stats, err := st.Stats(store.StatFilter{From: min, To: max, GroupBy: "source"})
		if err != nil {
			return toolError(err), nil
		}
		out["total_count"] = stats.TotalCount
		out["by_source"] = stats.Rows
		return jsonResult(out)
	}
}

func handleTimeline(st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		date, err := argDate(req, "date")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := argLimit(req)
		// 汇总走 SQL（全量精确），明细走 limit：两者口径不同，所以分开给。
		stats, err := st.Stats(store.StatFilter{From: date, To: date, GroupBy: "day"})
		if err != nil {
			return toolError(err), nil
		}
		events, err := st.ListEvents(store.EventQuery{Date: date, Limit: limit})
		if err != nil {
			return toolError(err), nil
		}
		list := make([]compactEvent, 0, len(events))
		for _, e := range events {
			e.FillLocalTime()
			list = append(list, toCompact(e))
		}
		out := map[string]any{
			"date":        date,
			"weekday":     weekday(date),
			"total_count": stats.TotalCount,
			"totals":      stats.Totals,
			"returned":    len(list),
			"truncated":   stats.TotalCount > len(list),
			"day_summary": dayEvents(list),
			"events":      list,
			"amount_unit": "分（cents）",
			"totals_rule": totalsRule,
		}
		if stats.TotalCount == 0 {
			out["note"] = "这一天没有数据（不等于这一天什么都没发生，只表示观复库里没记录）"
		}
		return jsonResult(out)
	}
}

func handleStats(st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		from, err := argDate(req, "from")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		to, err := argDate(req, "to")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		stats, err := st.Stats(store.StatFilter{
			From:      from,
			To:        to,
			GroupBy:   req.GetString("group_by", ""),
			Source:    req.GetString("source", ""),
			EventType: req.GetString("event_type", ""),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		stats.TotalsRule = totalsRule
		stats.AmountUnit = "cents"
		if stats.TotalCount == 0 {
			stats.Note = "区间内没有匹配事件；空区间与零记录无法区分，必要时放宽日期或去掉 source/event_type 再查"
		}
		return jsonResult(stats)
	}
}

func handleEvents(st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		from, err := argDate(req, "from")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		to, err := argDate(req, "to")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		source := req.GetString("source", "")
		eventType := req.GetString("event_type", "")
		query := req.GetString("q", "")
		limit := argLimit(req)
		offset := req.GetInt("offset", 0)
		if offset < 0 {
			offset = 0
		}

		// total 必须与明细同口径（含关键词），否则分页数字和事件列表会互相打脸。
		total, err := st.Stats(store.StatFilter{
			From: from, To: to, GroupBy: "day", Source: source, EventType: eventType, Query: query,
		})
		if err != nil {
			return toolError(err), nil
		}
		events, err := st.ListEvents(store.EventQuery{
			From: from, To: to, Source: source, EventType: eventType,
			Query: query, Limit: limit, Offset: offset,
		})
		if err != nil {
			return toolError(err), nil
		}
		list := make([]compactEvent, 0, len(events))
		for _, e := range events {
			e.FillLocalTime()
			list = append(list, toCompact(e))
		}
		return jsonResult(map[string]any{
			"from":        from,
			"to":          to,
			"total_count": total.TotalCount,
			"offset":      offset,
			"returned":    len(list),
			"truncated":   offset+len(list) < total.TotalCount,
			"events":      list,
			"amount_unit": "分（cents）",
			"totals_rule": totalsRule,
		})
	}
}

func handleSources(st *store.Store) server.ToolHandlerFunc {
	type sourceView struct {
		Name             string `json:"name"`
		Enabled          bool   `json:"enabled"`
		IntervalSecs     int    `json:"interval_secs"`
		Cursor           string `json:"cursor,omitempty"`
		LastSyncAt       int64  `json:"last_sync_at,omitempty"`
		SinceLastSyncSec int64  `json:"since_last_sync_secs,omitempty"`
		LastError        string `json:"last_error,omitempty"`
		LastCount        int    `json:"last_count"`
	}
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		states, err := st.ListSourceStates()
		if err != nil {
			return toolError(err), nil
		}
		now := time.Now().Unix()
		out := make([]sourceView, 0, len(states))
		for _, s := range states {
			v := sourceView{
				Name: s.Name, Enabled: s.Enabled, IntervalSecs: s.IntervalSecs,
				Cursor: s.Cursor, LastSyncAt: s.LastSyncAt, LastError: s.LastError, LastCount: s.LastCount,
			}
			if s.LastSyncAt > 0 {
				v.SinceLastSyncSec = now - s.LastSyncAt
			}
			out = append(out, v)
		}
		return jsonResult(map[string]any{"sources": out, "checked_at": now})
	}
}

// totalsRule 与 store.Stats 的收支判定同源，写进返回值以免 LLM 自行脑补口径。
const totalsRule = "income 取 detail.type==income 的金额；其余带金额事件一律计为 expense，" +
	"故 huozhi 的转账/报销也进 expense（与网页时间轴顶栏同口径）"

// dayEvents 挑出全天精度事件，供「今天有没有写日记/记了几笔账」这类问题直接引用。
func dayEvents(list []compactEvent) []compactEvent {
	out := make([]compactEvent, 0, 4)
	for _, e := range list {
		if e.TimePrecision == model.PrecisionDay {
			out = append(out, e)
		}
	}
	return out
}

func weekday(date string) string {
	d, err := time.ParseInLocation("2006-01-02", date, model.LocalLocation())
	if err != nil {
		return ""
	}
	return []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[int(d.Weekday())]
}

// jsonResult 工具一律返回缩进 JSON 文本：结构稳定，客户端不需要额外解析约定。
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultErrorf("结果序列化失败: %v", err), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// toolError 把存储层错误作为工具级 isError 返回（协议层仍是成功响应），
// 否则客户端只看到一次没有上下文的 JSON-RPC 失败，模型无从自纠。
func toolError(err error) *mcp.CallToolResult {
	return mcp.NewToolResultErrorFromErr("查询失败", err)
}

// argDate 严格校验 YYYY-MM-DD。这里刻意不接受 RFC3339/时间戳：
// 源侧格式本就五花八门，MCP 入口只认一种，错了就明确报错让调用方改。
func argDate(req mcp.CallToolRequest, key string) (string, error) {
	v, err := req.RequireString(key)
	if err != nil {
		return "", fmt.Errorf("缺少参数 %s（YYYY-MM-DD，如 2026-10-16）", key)
	}
	if !dateRe.MatchString(v) {
		return "", fmt.Errorf("参数 %s 需为 YYYY-MM-DD，如 2026-10-16（收到 %q）", key, v)
	}
	if _, err := time.ParseInLocation("2006-01-02", v, model.LocalLocation()); err != nil {
		return "", fmt.Errorf("参数 %s 不是有效日期（YYYY-MM-DD），收到 %q: %v", key, v, err)
	}
	return v, nil
}

func argLimit(req mcp.CallToolRequest) int {
	n := req.GetInt("limit", defaultEvents)
	switch {
	case n <= 0:
		return defaultEvents
	case n > maxEvents:
		return maxEvents
	default:
		return n
	}
}
