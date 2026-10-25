package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// QingyeSource 青野集（qingye）家庭园艺适配器：植物日记（可带图）+ 养护记录。
//
// 上游接口（2026-10-16 对该实例实测确认，非按规范推断）：
//
//	GET {base}/api/diaries?page=1&pageSize=20
//	  → {"code":0,"message":"ok","data":{"list":[...],"page":1,"pageSize":20,"total":N}}
//	  item: {id, plantId, plant{...}, image, caption, takenAt, createdAt}
//	GET {base}/api/care-logs
//	  → {"code":0,"message":"ok","data":[...]}   ← **data 是裸数组，没有分页也没有 total**
//	  item: {id, plantId, plant{...}, type, title, at, note, source, taskId}
//
// 三条实测坑：
//  1. **两个端点外壳不一样**（一个 data.list 分页、一个 data 裸数组），不能共用一个 decode struct；
//  2. **没有任何日期过滤参数**：`startAt` / `from` / `startDate` 全被忽略（total 恒不变），
//     增量只能"按 takenAt 倒序翻页 + 水位在客户端截断"（与 shenshi 同类）；
//  3. 未知路径返回 `{"error":"not found"}` 而不是 `{code,...}`，所以判成功必须同时看
//     HTTP 状态码和 body 里的 code。
//
// 时间是 RFC3339 带纳秒 + `Z`（UTC）。日记的 takenAt 常是所选日期的 UTC 零点
// （实测 `2026-08-24T00:00:00Z`），语义是"哪一天"而非时刻 → 归为 day 精度；
// 养护记录的 `at` 是真实动作时刻 → second 精度。
//
// 图片：image 是同源绝对路径 `/uploads/<文件名>`（实测 200 image/jpeg、
// accept-ranges，无鉴权），观复经 /api/v1/media/qingye/{key} 代理回源。
//
// **全站零鉴权**，本源不需要 APIKey。
//
// cursor：已处理条目的最大 unix 秒；首轮为空则回溯 LookbackDays。
type QingyeSource struct {
	cfg config.SourceConfig
	loc *time.Location
}

func NewQingyeSource(cfg config.SourceConfig) *QingyeSource {
	// 适配器自报家门：认证方式与 Key 请求头都靠它解析，
	// 不依赖调用方记得填 Name（测试与页面配置两条路径都会经过这里）。
	if cfg.Name == "" {
		cfg.Name = "qingye"
	}
	return &QingyeSource{cfg: cfg, loc: model.LocalLocation()}
}

func (q *QingyeSource) Name() string { return "qingye" }

func (q *QingyeSource) Interval() time.Duration {
	if q.cfg.Interval > 0 {
		return q.cfg.Interval
	}
	return 6 * time.Hour
}

// 分页与截断参数。pageSize 上限由客户端自设（上游不封顶，实测 pageSize=1000 照单全收）。
const (
	qingyePageSize = 100
	qingyeMaxPages = 20                 // 单轮最多 2000 条日记，超出下轮继续
	qingyeOverlap  = 7 * 24 * time.Hour // 水位回退，容忍补录与修订
)

// qingyePlant 嵌套的植物对象（只取会用到的字段，长尾留在 detail 之外）。
type qingyePlant struct {
	Name     string `json:"name"`
	Species  string `json:"species"`
	Location string `json:"location"`
	Room     *struct {
		Name string `json:"name"`
	} `json:"room"`
}

func (p *qingyePlant) place() string {
	if p == nil {
		return ""
	}
	if p.Location != "" {
		return p.Location
	}
	if p.Room != nil {
		return p.Room.Name
	}
	return ""
}

// qingyeDiary 一条植物日记（照片日记）。
type qingyeDiary struct {
	ID        flexID       `json:"id"`
	PlantID   flexID       `json:"plantId"`
	Plant     *qingyePlant `json:"plant"`
	Image     string       `json:"image"`
	Caption   string       `json:"caption"`
	TakenAt   string       `json:"takenAt"`
	CreatedAt string       `json:"createdAt"`
}

// qingyeCareLog 一条养护记录。
type qingyeCareLog struct {
	ID      flexID       `json:"id"`
	PlantID flexID       `json:"plantId"`
	Plant   *qingyePlant `json:"plant"`
	Type    string       `json:"type"`
	Title   string       `json:"title"`
	At      string       `json:"at"`
	Note    string       `json:"note"`
	Source  string       `json:"source"`
	TaskID  flexID       `json:"taskId"`
}

type qingyeDiaryData struct {
	List     []qingyeDiary `json:"list"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
	Total    int           `json:"total"`
}

type qingyeEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

// Sync 拉日记与养护记录，按水位截断后逐条 upsert。
func (q *QingyeSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if q.cfg.BaseURL == "" {
		return "", 0, fmt.Errorf("qingye 未配置：需要 QINGYE_BASE_URL")
	}

	// 首轮按回溯窗口；有水位则从「水位 - 重叠」起（日记可能被补录、养护可能被修订）。
	cutoff := time.Now().In(q.loc).AddDate(0, 0, -q.cfg.LookbackDays)
	if cursor != "" {
		if sec, err := strconv.ParseInt(cursor, 10, 64); err == nil {
			cutoff = time.Unix(sec, 0).In(q.loc).Add(-qingyeOverlap)
		} else {
			log.Printf("[qingye] 游标 %q 不是 unix 秒，本轮按回溯窗口重新拉取", cursor)
		}
	}
	cutoffUnix := cutoff.Unix()

	var (
		count   int
		maxSeen int64
	)
	advance := func(ev *model.Event, ts int64) error {
		if err := upsert(ev); err != nil {
			return err
		}
		count++
		if ts > maxSeen {
			maxSeen = ts
		}
		return nil
	}

	// ---------- 日记：倒序翻页，翻到比水位更早就停 ----------
	diaries, err := q.fetchDiaries(ctx, cutoffUnix)
	if err != nil {
		return "", count, err
	}
	for _, d := range diaries {
		// 再核一遍时间与水位：fetchDiaries 已过滤，但上游若改成正序返回，
		// 这里兜住才不会把陈年日记当新数据写进去。
		ts, ok := parseQingyeTime(d.TakenAt, d.CreatedAt)
		if !ok || ts <= cutoffUnix {
			continue
		}
		if err := advance(q.mapDiary(d, ts), ts); err != nil {
			return "", count, err
		}
	}

	// ---------- 养护记录：裸数组，客户端过滤 ----------
	logs, err := q.fetchCareLogs(ctx)
	if err != nil {
		return "", count, err
	}
	for _, c := range logs {
		ts, ok := parseQingyeTime(c.At, "")
		if !ok || ts <= cutoffUnix {
			continue
		}
		if err := advance(q.mapCare(c, ts), ts); err != nil {
			return "", count, err
		}
	}

	if maxSeen == 0 {
		// 本轮没有新条目：水位不动，避免把游标倒退成 0 触发全量回溯。
		return cursor, count, nil
	}
	return strconv.FormatInt(maxSeen, 10), count, nil
}

// fetchDiaries 按页倒序拉取，直到遇到比 cutoff 更早的条目或翻完/触顶。
func (q *QingyeSource) fetchDiaries(ctx context.Context, cutoffUnix int64) ([]qingyeDiary, error) {
	var out []qingyeDiary
	for page := 1; page <= qingyeMaxPages; page++ {
		v := url.Values{}
		v.Set("page", strconv.Itoa(page))
		v.Set("pageSize", strconv.Itoa(qingyePageSize))
		var env qingyeEnvelope[qingyeDiaryData]
		if err := q.getJSON(ctx, "/api/diaries?"+v.Encode(), &env); err != nil {
			return nil, err
		}
		list := env.Data.List
		if len(list) == 0 {
			return out, nil
		}
		for _, d := range list {
			ts, ok := parseQingyeTime(d.TakenAt, d.CreatedAt)
			if !ok {
				continue
			}
			if ts <= cutoffUnix {
				// 上游按 takenAt 倒序，往后只会更早，可以收工。
				return out, nil
			}
			out = append(out, d)
		}
		if page*qingyePageSize >= env.Data.Total || len(list) < qingyePageSize {
			return out, nil
		}
		if page == qingyeMaxPages {
			// 上游没有日期过滤参数，倒序翻页触顶后无法"下轮继续"——水位会推到
			// 最新，未拉到的旧条目从此不会被重查。必须报错让水位停在原地。
			return out, fmt.Errorf(
				"qingye 日记翻页达 %d 页防御上限，水位之上有未拉取的日记（上游无日期过滤，无法续传）；中止本轮且不推进游标",
				qingyeMaxPages)
		}
	}
	return out, nil
}

func (q *QingyeSource) fetchCareLogs(ctx context.Context) ([]qingyeCareLog, error) {
	var env qingyeEnvelope[[]qingyeCareLog]
	if err := q.getJSON(ctx, "/api/care-logs", &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// getJSON 取一个 JSON 响应。成功判定同时看 HTTP 状态与 body 的 code：
// 未知路径会以 200/404 + {"error":"not found"} 的形态回来，只看状态码会误判。
func (q *QingyeSource) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.cfg.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 qingye 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qingye %s 返回 HTTP %d: %s", path, resp.StatusCode, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("解析 qingye %s 响应失败: %w", path, err)
	}
	var code int
	switch t := dst.(type) {
	case *qingyeEnvelope[qingyeDiaryData]:
		code = t.Code
	case *qingyeEnvelope[[]qingyeCareLog]:
		code = t.Code
	}
	if code != 0 {
		return fmt.Errorf("qingye %s 业务码非 0: %s", path, truncate(string(body), 200))
	}
	return nil
}

// mapDiary 植物日记 → plant_diary（day 精度：takenAt 是"哪一天"而非时刻）。
func (q *QingyeSource) mapDiary(d qingyeDiary, ts int64) *model.Event {
	day := time.Unix(ts, 0).In(q.loc).Format("2006-01-02")
	ev := model.NewDayEvent(q.Name(), "dia:"+d.ID.String(), model.EventPlantDiary, day)
	ev.Title = plantTitle(d.Plant, d.PlantID)
	ev.Summary = truncate(strings.TrimSpace(d.Caption), 2000)
	ev.LocationName = d.Plant.place()
	ev.SourceURL = q.cfg.BaseURL + "/"

	detail := map[string]any{"plant_id": d.PlantID.String()}
	if d.Plant != nil {
		detail["plant_name"] = d.Plant.Name
		if d.Plant.Species != "" {
			detail["species"] = d.Plant.Species
		}
	}
	if d.Caption != "" {
		detail["caption"] = truncate(d.Caption, 2000)
	}
	if d.TakenAt != "" {
		detail["taken_at"] = d.TakenAt
	}
	if img := qingyeImageKey(d.Image); img != "" {
		// 原始路径留着，便于回溯上游语义；代理路径由 API 输出时现算。
		detail["image"] = d.Image
		ev.Media = []model.MediaRef{{ID: img, Kind: "photo"}}
	}
	ev.Detail = detail
	return ev
}

// mapCare 养护记录 → plant_care（second 精度：at 是真实动作时刻）。
func (q *QingyeSource) mapCare(c qingyeCareLog, ts int64) *model.Event {
	ev := &model.Event{
		Source:        q.Name(),
		ExternalID:    "care:" + c.ID.String(),
		EventType:     model.EventPlantCare,
		TimePrecision: model.PrecisionSecond,
		OccurredAt:    ts,
		LocalDate:     time.Unix(ts, 0).In(q.loc).Format("2006-01-02"),
		Title:         plantTitle(c.Plant, c.PlantID),
		LocationName:  c.Plant.place(),
		SourceURL:     q.cfg.BaseURL + "/",
	}
	parts := make([]string, 0, 3)
	if label := firstNonEmpty(c.Title, c.Type); label != "" {
		parts = append(parts, label)
	}
	if note := strings.TrimSpace(c.Note); note != "" {
		parts = append(parts, truncate(note, 200))
	}
	ev.Summary = joinNonEmpty(parts, " · ")

	detail := map[string]any{"plant_id": c.PlantID.String()}
	if c.Plant != nil {
		detail["plant_name"] = c.Plant.Name
	}
	if c.Type != "" {
		detail["type"] = c.Type
	}
	if c.Title != "" {
		detail["title"] = c.Title
	}
	if c.Note != "" {
		detail["note"] = truncate(c.Note, 2000)
	}
	if c.Source != "" {
		detail["source"] = c.Source
	}
	// 上游用 0 表示"未关联任务"，与字段缺失同义，都不写进 detail。
	if c.TaskID != "" && c.TaskID != "0" {
		detail["task_id"] = c.TaskID.String()
	}
	ev.Detail = detail
	return ev
}

// plantTitle 卡片标题统一用植物名，让同一株的记录在时间轴上成组；
// 嵌套 plant 对象缺失时退化成 "植物 #<id>"，不留空标题。
func plantTitle(p *qingyePlant, plantID flexID) string {
	if p != nil && strings.TrimSpace(p.Name) != "" {
		return strings.TrimSpace(p.Name)
	}
	return "植物 #" + plantID.String()
}

// qingyeImageKey 从 image 字段取出可用于代理的文件名。
// 实测形态是同源绝对路径 `/uploads/<雪花号>_<随机>.jpg`；也容忍上游改成完整 URL
// 或只给文件名。返回空串表示没有图片。
func qingyeImageKey(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if u, err := url.Parse(image); err == nil && u.Host != "" {
		image = u.Path
	}
	return strings.TrimPrefix(strings.TrimPrefix(image, "/"), "uploads/")
}

// parseQingyeTime 解析 RFC3339（Go 的小数秒解析天然容忍纳秒），
// 主字段失败时回退备用字段；两个都拿不到返回 false，由调用方跳过该条。
func parseQingyeTime(primary, fallback string) (int64, bool) {
	for _, s := range []string{primary, fallback} {
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// 编译期断言：QingyeSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*QingyeSource)(nil)
