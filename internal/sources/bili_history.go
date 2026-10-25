package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// biliOverlapDays 每次增量同步回退的天数（B 站历史偶发补录）。
// 与其余日期游标源统一为 3 天。
const biliOverlapDays = 3

// BiliHistorySource 拾帧集（bili-history）观看历史适配器。
//
// 上游接口（2026-10-16 读源码确认，backend/routers/history.go:15-34）：
//
//	GET {base}/api/history/all?date_range=YYYYMMDD-YYYYMMDD&page&size&sort_order
//	  （注意参数名是 date_range，不是 start/end；size 默认 10）
//	  Resp: {status, data:{records:[…], total, size, current}}
//	  item: {title, long_title, bvid, author_name, view_at(unix秒),
//	         view_time("2006-01-02 15:04:05"), duration(秒), tag_name,
//	         main_category, cover(B站外链), progress, business}
//	REST 无 auth 中间件（MCP token 只包 MCP handler）。
//
// 封面：B 站 CDN 外链存 media_json；观复媒体代理回源上游
// GET {base}/api/images/proxy?url=<cover>（带 Referer+SESSDATA，规避防盗链）。
//
// cursor：YYYY-MM-DD；首轮回溯 LookbackDays（默认 90）。
type BiliHistorySource struct {
	cfg config.SourceConfig
	loc *time.Location
}

func NewBiliHistorySource(cfg config.SourceConfig) *BiliHistorySource {
	// 适配器自报家门：认证方式与 Key 请求头都靠它解析，
	// 不依赖调用方记得填 Name（测试与页面配置两条路径都会经过这里）。
	if cfg.Name == "" {
		cfg.Name = "bili-history"
	}
	return &BiliHistorySource{cfg: cfg, loc: model.LocalLocation()}
}

func (b *BiliHistorySource) Name() string { return "bili-history" }

func (b *BiliHistorySource) Interval() time.Duration {
	if b.cfg.Interval > 0 {
		return b.cfg.Interval
	}
	return 2 * time.Hour // 观看记录时效性中等
}

type biliRecord struct {
	Title        string `json:"title"`
	LongTitle    string `json:"long_title"`
	Bvid         string `json:"bvid"`
	AuthorName   string `json:"author_name"`
	ViewAt       int64  `json:"view_at"` // unix 秒
	Duration     int    `json:"duration"`
	TagName      string `json:"tag_name"`
	Tname        string `json:"tname"`
	MainCategory string `json:"main_category"`
	Cover        string `json:"cover"`
	Progress     int    `json:"progress"` // 已看秒数
	Business     string `json:"business"`
	URI          string `json:"uri"`
}

type biliListResp struct {
	Status string `json:"status"`
	Data   struct {
		Records []biliRecord `json:"records"`
		Total   int          `json:"total"`
		Size    int          `json:"size"`
		Current int          `json:"current"`
	} `json:"data"`
}

// Sync 按范围拉取观看记录（date_range 单参数表达区间，逐页翻完）。
func (b *BiliHistorySource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if b.cfg.BaseURL == "" {
		return "", 0, fmt.Errorf("bili-history 未配置：需要 BILI_HISTORY_BASE_URL")
	}

	loc := b.loc
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -b.cfg.LookbackDays)
	if cursor != "" {
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			// 重叠 2 天：B 站历史偶发补录（如电视端观看后回写），upsert 幂等。
			start = t.AddDate(0, 0, 1-biliOverlapDays)
		}
	}
	end := now
	if start.After(end) {
		return cursor, 0, nil
	}

	const pageSize = 100
	var (
		total     int
		newCursor = end.Format("2006-01-02")
	)
	for page := 1; ; page++ {
		records, err := b.fetchPage(ctx, start, end, page, pageSize)
		if err != nil {
			return "", total, err
		}
		for i := range records {
			rec := &records[i]
			ev, err := b.mapRecord(rec)
			if err != nil {
				return "", total, fmt.Errorf("映射观看记录 %s 失败: %w", rec.Bvid, err)
			}
			if err := upsert(ev); err != nil {
				return "", total, err
			}
			total++
		}
		if len(records) < pageSize {
			break
		}
		if page > 100 {
			// 防御上限（单轮 1 万条）。触顶说明窗口内还有未拉取的记录，
			// 报错让游标停在原地——静默跳过会永久丢失。
			return "", total, fmt.Errorf(
				"bili-history 翻页达 100 页防御上限（%s~%s），可能有未拉取的观看记录；中止本轮且不推进游标",
				start.Format("2006-01-02"), end.Format("2006-01-02"))
		}
	}
	return newCursor, total, nil
}

func (b *BiliHistorySource) fetchPage(ctx context.Context, start, end time.Time, page, pageSize int) ([]biliRecord, error) {
	// date_range 格式 YYYYMMDD-YYYYMMDD。
	dr := start.Format("20060102") + "-" + end.Format("20060102")
	q := url.Values{}
	q.Set("date_range", dr)
	q.Set("page", strconv.Itoa(page))
	q.Set("size", strconv.Itoa(pageSize))
	q.Set("sort_order", "1") // ASC：老记录在前，cursor 语义更稳
	reqURL := b.cfg.BaseURL + "/api/history/all?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 bili-history 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bili-history 返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var lr biliListResp
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("解析 bili-history 响应失败: %w", err)
	}
	return lr.Data.Records, nil
}

// mapRecord 映射观看记录。幂等键用 bvid+view_at（同一视频可能多次观看）。
func (b *BiliHistorySource) mapRecord(rec *biliRecord) (*model.Event, error) {
	viewed := time.Unix(rec.ViewAt, 0).In(b.loc)

	title := rec.Title
	if title == "" {
		title = rec.LongTitle
	}
	if title == "" {
		title = "视频"
	}

	ev := &model.Event{
		Source:        b.Name(),
		ExternalID:    rec.Bvid + "@" + strconv.FormatInt(rec.ViewAt, 10),
		EventType:     model.EventVideoWatch,
		TimePrecision: model.PrecisionSecond,
		OccurredAt:    viewed.UTC().Unix(),
		LocalDate:     viewed.Format("2006-01-02"),
		Title:         title,
		Detail: map[string]any{
			"author":        rec.AuthorName,
			"bvid":          rec.Bvid,
			"duration_sec":  rec.Duration,
			"progress_sec":  rec.Progress,
			"tag":           firstNonEmpty(rec.TagName, rec.Tname),
			"main_category": rec.MainCategory,
		},
		SourceURL: "https://www.bilibili.com/video/" + rec.Bvid,
	}
	// 摘要：UP 主 · 分区 · 完播率。
	parts := make([]string, 0, 3)
	if rec.AuthorName != "" {
		parts = append(parts, rec.AuthorName)
	}
	if tag := firstNonEmpty(rec.TagName, rec.Tname); tag != "" {
		parts = append(parts, tag)
	}
	if rec.Duration > 0 && rec.Progress > 0 {
		parts = append(parts, fmt.Sprintf("看完 %d%%", rec.Progress*100/rec.Duration))
	}
	ev.Summary = joinNonEmpty(parts, " · ")

	// 封面：B 站外链；ProxyPath 由 API 层按源拼装（回源上游 images/proxy）。
	if rec.Cover != "" {
		ev.Media = []model.MediaRef{{Kind: "cover", ID: rec.Cover}}
	}
	return ev, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinNonEmpty(parts []string, sep string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return joinStrings(out, sep)
}

func joinStrings(parts []string, sep string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += sep
		}
		s += p
	}
	return s
}

// 编译期断言：BiliHistorySource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*BiliHistorySource)(nil)
