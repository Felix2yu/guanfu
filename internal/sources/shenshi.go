package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// shenshiOverlapDays 增量回退天数（任务可能事后补记完成时间、复盘常隔天补写）。
const shenshiOverlapDays = 3

// shenshiTaskFetchLimit 拉取已完成任务的上限。上游 /api/tasks 只有 LIMIT、
// 没有 OFFSET，**且没有任何"按完成日过滤"的参数**（from/to 过滤的是到期日），
// 因此增量只能靠"按完成时间倒序拉最近 N 条 + 客户端窗口过滤"。
// 单用户量级下 500 条足以覆盖数月的完成记录；触顶时打印告警日志。
const shenshiTaskFetchLimit = 500

// ShenshiSource 慎始（shenshi）任务管理适配器。
//
// 上游接口（2026-10-16 读源码确认，server/internal/api/*.go）：
//
//	GET {base}/api/tasks?status=done&sortBy=completed&limit=N
//	  → {"tasks":[{id,listId,title,notes,status,priority,startDate,dueDate,
//	      completedAt,createdAt,updatedAt,tags[]{id,name},subtasks[],
//	      important,urgent,starred,archived},...],"count":N}
//	GET {base}/api/reviews?limit=N
//	  → {"reviews":[{id,date,mood,wins,blockers,tomorrow,createdAt,updatedAt}]}
//	GET {base}/api/habits?from=YYYY-MM-DD&to=YYYY-MM-DD&includeArchived=1
//	  → {from,to,today,habits[]{id,name,icon,cadence,target,...},
//	     logs[]{id,habitId,day,count,note,createdAt},stats[]}
//
// 关键陷阱：
//   - `/api/tasks` 的 from/to 过滤 **due_date**（到期日）而非完成日；incremental
//     必须客户端按 completedAt 截断。
//   - completedAt 是**本地时区 RFC3339**（带偏移），不是 unix 秒也不是纯日期。
//   - `/api/reviews/{date}` 缺记录时返回 200 + 空壳（只有 date），要靠内容判空。
//   - 习惯看板 includeArchived=1 表示"含归档"（任务侧同名参数语义相反：只看归档）。
//
// 事件模型（幂等键加前缀）：
//   - task_done   `task:<id>`   完成时刻精确到秒（second）
//   - review      `review:<date>`
//   - habit_check `habit:<day>` 按天聚合（一天的打卡汇总成一张卡）
//
// 鉴权：SHENSHI_TOKEN 未配置时服务端整体放行；配置后观复用
// `Authorization: Bearer <token>`（gate 同时支持 ?token= / Cookie / Basic）。
//
// cursor：YYYY-MM-DD；首轮回溯 LookbackDays（默认 90）。
type ShenshiSource struct {
	cfg config.SourceConfig
	loc *time.Location
}

func NewShenshiSource(cfg config.SourceConfig) *ShenshiSource {
	// 适配器自报家门：认证方式与 Key 请求头都靠它解析，
	// 不依赖调用方记得填 Name（测试与页面配置两条路径都会经过这里）。
	if cfg.Name == "" {
		cfg.Name = "shenshi"
	}
	return &ShenshiSource{cfg: cfg, loc: model.LocalLocation()}
}

func (s *ShenshiSource) Name() string { return "shenshi" }

func (s *ShenshiSource) Interval() time.Duration {
	if s.cfg.Interval > 0 {
		return s.cfg.Interval
	}
	return time.Hour
}

type shenshiTag struct {
	Name string `json:"name"`
}

type shenshiTask struct {
	ID          flexID        `json:"id"`
	Title       string        `json:"title"`
	Notes       string        `json:"notes"`
	Status      string        `json:"status"`
	Priority    int           `json:"priority"`
	DueDate     *string       `json:"dueDate"`
	CompletedAt *string       `json:"completedAt"`
	Important   bool          `json:"important"`
	Urgent      bool          `json:"urgent"`
	Starred     bool          `json:"starred"`
	Archived    bool          `json:"archived"`
	Tags        []shenshiTag  `json:"tags"`
	Subtasks    []shenshiTask `json:"subtasks"`
	URL         string        `json:"url"`
	UpdatedAt   string        `json:"updatedAt"`
}

type shenshiReview struct {
	Date      string `json:"date"`
	Mood      string `json:"mood"`
	Wins      string `json:"wins"`
	Blockers  string `json:"blockers"`
	Tomorrow  string `json:"tomorrow"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type shenshiHabit struct {
	ID       flexID `json:"id"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Cadence  string `json:"cadence"`
	Target   int    `json:"target"`
	Archived bool   `json:"archived"`
}

type shenshiHabitLog struct {
	HabitID   flexID `json:"habitId"`
	Day       string `json:"day"`
	Count     int    `json:"count"`
	Note      string `json:"note"`
	CreatedAt string `json:"createdAt"`
}

type shenshiHabitBoard struct {
	From   string            `json:"from"`
	To     string            `json:"to"`
	Habits []shenshiHabit    `json:"habits"`
	Logs   []shenshiHabitLog `json:"logs"`
}

// Sync 拉取窗口内的任务完成、每日复盘与习惯打卡。
func (s *ShenshiSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if s.cfg.BaseURL == "" {
		return "", 0, fmt.Errorf("shenshi 未配置：需要 SHENSHI_BASE_URL")
	}

	loc := s.loc
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -s.cfg.LookbackDays)
	if cursor != "" {
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			start = t.AddDate(0, 0, 1-shenshiOverlapDays)
		}
	}
	end := now
	if start.After(end) {
		return cursor, 0, nil
	}
	from, to := start.Format("2006-01-02"), end.Format("2006-01-02")

	var total int

	// 1) 任务完成
	tasks, err := s.fetchDoneTasks(ctx, from)
	if err != nil {
		return "", total, err
	}
	for i := range tasks {
		t := &tasks[i]
		ev := s.mapTask(t, loc)
		if ev == nil {
			continue
		}
		if ev.LocalDate < from || ev.LocalDate > to {
			continue // 窗口外（上游按完成时间倒序返回，超窗属正常）
		}
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
	}

	// 2) 每日复盘（列表接口一次拉够，客户端按窗口过滤）
	reviews, err := s.fetchReviews(ctx, daysBetween(from, to))
	if err != nil {
		return "", total, err
	}
	for i := range reviews {
		r := &reviews[i]
		ev := mapReview(s.Name(), s.cfg.BaseURL, r)
		if ev == nil || ev.LocalDate < from || ev.LocalDate > to {
			continue
		}
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
	}

	// 3) 习惯打卡（按天聚合）
	logs, names, err := s.fetchHabitLogs(ctx, from, to)
	if err != nil {
		return "", total, err
	}
	for _, ev := range s.mapHabitLogs(logs, names) {
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
	}

	return to, total, nil
}

// get 带鉴权 GET 并解码 JSON。
func (s *ShenshiSource) get(ctx context.Context, reqURL string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if err := s.cfg.ApplyAuth(req); err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 shenshi 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("shenshi 鉴权失败（HTTP 401）：SHENSHI_TOKEN 与服务端不一致")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("shenshi 返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("解析 shenshi 响应失败: %w", err)
	}
	return nil
}

// fetchDoneTasks 拉取最近完成的 N 条任务（按完成时间倒序）。
// from 是本轮窗口起点：若拉满上限且最旧一条仍晚于 from，说明窗口内有
// 未拉到的完成记录，报错让游标停在原地（静默跳过会永久丢失）。
func (s *ShenshiSource) fetchDoneTasks(ctx context.Context, from string) ([]shenshiTask, error) {
	v := url.Values{}
	v.Set("status", "done")
	v.Set("sortBy", "completed")
	v.Set("limit", strconv.Itoa(shenshiTaskFetchLimit))
	var resp struct {
		Tasks []shenshiTask `json:"tasks"`
		Count int           `json:"count"`
	}
	if err := s.get(ctx, s.cfg.BaseURL+"/api/tasks?"+v.Encode(), &resp); err != nil {
		return nil, err
	}
	if len(resp.Tasks) >= shenshiTaskFetchLimit {
		// 倒序返回的末尾是最旧一条；它已早于窗口起点的话，这 500 条
		// 足以覆盖整个窗口，不算截断。
		covered := false
		if t := resp.Tasks[len(resp.Tasks)-1]; t.CompletedAt != nil && *t.CompletedAt != "" {
			if at, err := time.Parse(time.RFC3339, *t.CompletedAt); err == nil {
				if at.In(s.loc).Format("2006-01-02") <= from {
					covered = true
				}
			}
		}
		if !covered {
			return nil, fmt.Errorf(
				"shenshi 已完成任务拉取达上限 %d 条且未覆盖到窗口起点 %s，窗口内可能有未拉取的完成记录；中止本轮且不推进游标",
				shenshiTaskFetchLimit, from)
		}
	}
	return resp.Tasks, nil
}

// fetchReviews 拉取最近 limit 条复盘（date DESC）。
func (s *ShenshiSource) fetchReviews(ctx context.Context, limit int) ([]shenshiReview, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	v := url.Values{}
	v.Set("limit", strconv.Itoa(limit))
	var resp struct {
		Reviews []shenshiReview `json:"reviews"`
	}
	if err := s.get(ctx, s.cfg.BaseURL+"/api/reviews?"+v.Encode(), &resp); err != nil {
		return nil, err
	}
	return resp.Reviews, nil
}

// fetchHabitLogs 拉取区间打卡；同时返回习惯 id→名称 映射供展示。
func (s *ShenshiSource) fetchHabitLogs(ctx context.Context, from, to string) ([]shenshiHabitLog, map[flexID]string, error) {
	v := url.Values{}
	v.Set("from", from)
	v.Set("to", to)
	v.Set("includeArchived", "1") // 含归档：历史打卡仍需入轴
	var board shenshiHabitBoard
	if err := s.get(ctx, s.cfg.BaseURL+"/api/habits?"+v.Encode(), &board); err != nil {
		return nil, nil, err
	}
	names := make(map[flexID]string, len(board.Habits))
	for _, h := range board.Habits {
		names[h.ID] = h.Name
	}
	return board.Logs, names, nil
}

// mapTask 完成的任务 → task_done（second 精度）。
func (s *ShenshiSource) mapTask(t *shenshiTask, loc *time.Location) *model.Event {
	if t.CompletedAt == nil || *t.CompletedAt == "" {
		return nil
	}
	at, err := time.Parse(time.RFC3339, *t.CompletedAt)
	if err != nil {
		return nil
	}
	at = at.In(loc)
	ev := &model.Event{
		Source:        s.Name(),
		ExternalID:    "task:" + t.ID.String(),
		EventType:     model.EventTaskDone,
		TimePrecision: model.PrecisionSecond,
		OccurredAt:    at.UTC().Unix(),
		LocalDate:     at.Format("2006-01-02"),
		Title:         titleCase(t.Title, "完成任务"),
		SourceURL:     s.cfg.BaseURL + "/",
	}

	detail := map[string]any{"task_id": t.ID.String()}
	if t.Priority > 0 {
		detail["priority"] = t.Priority
	}
	if t.DueDate != nil && *t.DueDate != "" {
		detail["due_date"] = *t.DueDate
	}
	if t.Notes != "" {
		detail["notes"] = truncate(t.Notes, 2000)
	}
	if t.URL != "" {
		detail["url"] = t.URL
	}
	summary := make([]string, 0, 4)
	if len(t.Tags) > 0 {
		names := make([]string, 0, len(t.Tags))
		for _, tag := range t.Tags {
			if tag.Name != "" {
				names = append(names, tag.Name)
			}
		}
		if len(names) > 0 {
			detail["tags"] = names
			summary = append(summary, "#"+strings.Join(names, " #"))
		}
	}
	switch {
	case t.Important && t.Urgent:
		summary = append(summary, "重要且紧急")
	case t.Important:
		summary = append(summary, "重要")
	case t.Urgent:
		summary = append(summary, "紧急")
	}
	if n := len(t.Subtasks); n > 0 {
		done := 0
		for _, st := range t.Subtasks {
			if st.Status == "done" {
				done++
			}
		}
		detail["subtask_done"] = done
		detail["subtask_total"] = n
		summary = append(summary, fmt.Sprintf("子任务 %d/%d", done, n))
	}
	if t.DueDate != nil && *t.DueDate != "" && at.Format("2006-01-02") > *t.DueDate {
		summary = append(summary, "逾期完成")
	}
	ev.Detail = detail
	ev.Summary = joinNonEmpty(summary, " · ")
	return ev
}

// mapReview 每日复盘 → review（全天）。空壳（四段全空）返回 nil。
func mapReview(source, baseURL string, r *shenshiReview) *model.Event {
	day := dayOnly(r.Date)
	if day == "" {
		return nil
	}
	mood := strings.TrimSpace(r.Mood)
	wins := strings.TrimSpace(r.Wins)
	blockers := strings.TrimSpace(r.Blockers)
	tomorrow := strings.TrimSpace(r.Tomorrow)
	if mood == "" && wins == "" && blockers == "" && tomorrow == "" {
		return nil // 上游缺记录时返回 200 + 空壳
	}
	ev := model.NewDayEvent(source, "review:"+day, model.EventReview, day)
	ev.Title = "每日复盘"
	ev.SourceURL = baseURL + "/"

	detail := map[string]any{}
	if mood != "" {
		detail["mood"] = truncate(mood, 200)
	}
	if wins != "" {
		detail["wins"] = truncate(wins, 4000)
	}
	if blockers != "" {
		detail["blockers"] = truncate(blockers, 4000)
	}
	if tomorrow != "" {
		detail["tomorrow"] = truncate(tomorrow, 4000)
	}
	ev.Detail = detail

	summary := make([]string, 0, 3)
	if mood != "" {
		summary = append(summary, mood)
	}
	if wins != "" {
		summary = append(summary, "收获 "+firstLine(wins, 40))
	}
	ev.Summary = joinNonEmpty(summary, " · ")
	return ev
}

// mapHabitLogs 按天聚合打卡 → habit_check（全天，一天一条）。
func (s *ShenshiSource) mapHabitLogs(logs []shenshiHabitLog, names map[flexID]string) []*model.Event {
	byDay := map[string][]shenshiHabitLog{}
	for _, l := range logs {
		day := dayOnly(l.Day)
		if day == "" {
			continue
		}
		byDay[day] = append(byDay[day], l)
	}
	out := make([]*model.Event, 0, len(byDay))
	for day, items := range byDay {
		ev := model.NewDayEvent(s.Name(), "habit:"+day, model.EventHabitCheck, day)
		ev.Title = "习惯打卡"
		ev.SourceURL = s.cfg.BaseURL + "/"

		habitNames := make([]string, 0, len(items))
		habits := make([]map[string]any, 0, len(items))
		for _, l := range items {
			name := names[l.HabitID]
			if name == "" {
				name = "习惯 " + l.HabitID.String()
			}
			count := l.Count
			if count < 1 {
				count = 1
			}
			item := map[string]any{"name": name, "count": count}
			if l.Note != "" {
				item["note"] = truncate(l.Note, 200)
			}
			habits = append(habits, item)
			habitNames = append(habitNames, name)
		}
		ev.Detail = map[string]any{"habits": habits, "habit_count": len(habits)}
		ev.Summary = fmt.Sprintf("%d 项 · %s", len(habits), truncate(strings.Join(habitNames, "、"), 60))
		out = append(out, ev)
	}
	return out
}

// daysBetween 两个 YYYY-MM-DD 之间的天数（含首尾），用于给上游 limit 取够。
func daysBetween(from, to string) int {
	loc := model.LocalLocation()
	a, err1 := time.ParseInLocation("2006-01-02", from, loc)
	b, err2 := time.ParseInLocation("2006-01-02", to, loc)
	if err1 != nil || err2 != nil {
		return 30
	}
	days := int(b.Sub(a).Hours()/24) + 1
	if days < 1 {
		return 1
	}
	return days
}

// 编译期断言：ShenshiSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*ShenshiSource)(nil)
