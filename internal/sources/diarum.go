package sources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// diarumOverlapDays 每次增量同步回退的天数（日记写的是「今天」，
// 昨晚补写前几天的日记很常见；upsert 幂等，重查无副作用）。
const diarumOverlapDays = 7

// DiarumSource 吾身（diarum）日记适配器。
//
// 上游接口（2026-10-16 读源码确认，backend/internal/api/diaries.go）：
//
//	GET  {base}/api/v1/diaries/exists?start&end
//	  → {dates:[...], entries:[{date,mood,weather,city,temp_min,temp_max,has_content}]}
//	  （一条请求拿整个区间的「有日记的日期」，含 has_content 标记）
//	GET  {base}/api/v1/diaries/by-date/:date
//	  → 完整日记 {id,date,content(HTML),mood,mood_states,scenarios,weather,city,
//	              temp_min,temp_max,tags,created,updated,exists}
//	POST {base}/api/v1/auth/login {usernameOrEmail,password} → {token}
//
// 鉴权：REST 全部走 auth.Middleware，只认 Authorization: Bearer <JWT>
// （30 天有效；MCP 的 API token 不通用，勿混用）。DIARUM_API_KEY 支持两种形态：
//   - 直接贴 JWT（三 segment、eyJ 开头）：即取即用，过期后报错提示更新；
//   - "用户名:密码"：适配器自动登录换取 JWT 并缓存至临近过期，无需人工轮换。
//
// 判定形态只看「是否含冒号」：JWT 不含冒号，一律直传；含冒号且两段非空
// 才走登录。这样既兼容未来服务端支持的长效 API token，也不会把 JWT 误判。
//
// 策略：先用 exists 拿区间内有内容的日期（1 请求），再逐日 by-date 取全文。
// 只收 has_content=true 的条目——仅含自动天气填充、无正文/心情/标签的
// 「空日记」不进时间轴（与服务端 diaryHasContent 语义一致）。
//
// cursor：YYYY-MM-DD；首轮回溯 LookbackDays（默认 365）。
type DiarumSource struct {
	cfg config.SourceConfig
	loc *time.Location

	// token 缓存（仅 "user:pass" 形态需要）。手动触发与定时同步可能并发
	// 调用同一适配器，缓存必须加锁。
	mu     sync.Mutex
	jwt    string
	jwtExp time.Time // 过期时刻（含安全边际）
}

func NewDiarumSource(cfg config.SourceConfig) *DiarumSource {
	return &DiarumSource{cfg: cfg, loc: model.LocalLocation()}
}

func (d *DiarumSource) Name() string { return "diarum" }

func (d *DiarumSource) Interval() time.Duration {
	if d.cfg.Interval > 0 {
		return d.cfg.Interval
	}
	return 30 * time.Minute // 日记时效性中等
}

// isJWT 判断字符串是否形如 JWT（三 segment、eyJ 开头、中段可解出 JSON）。
// 仅用于诊断与日志提示，不参与分支判定（见 isCredentialPair）。
func isJWT(s string) bool {
	if !strings.HasPrefix(s, "eyJ") || strings.Count(s, ".") != 2 {
		return false
	}
	parts := strings.Split(s, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims map[string]any
	return json.Unmarshal(raw, &claims) == nil
}

// isCredentialPair 判断 APIKey 是否为「用户名:密码」形态（走登录换 JWT）。
func isCredentialPair(s string) bool {
	user, pass, ok := strings.Cut(s, ":")
	return ok && user != "" && pass != ""
}

// diarumExistsResp GET /api/v1/diaries/exists 响应。
type diarumExistsResp struct {
	Dates   []string `json:"dates"`
	Entries []struct {
		Date       string  `json:"date"`
		Mood       int     `json:"mood"`
		Weather    string  `json:"weather"`
		City       string  `json:"city"`
		TempMin    float64 `json:"temp_min"`
		TempMax    float64 `json:"temp_max"`
		HasContent bool    `json:"has_content"`
	} `json:"entries"`
}

// diarumDiary GET /api/v1/diaries/by-date/:date 响应（diaryResponse）。
type diarumDiary struct {
	ID         string   `json:"id"`
	Date       string   `json:"date"`
	Content    string   `json:"content"`
	Mood       int      `json:"mood"`
	MoodStates []string `json:"mood_states"`
	Scenarios  []string `json:"scenarios"`
	Weather    string   `json:"weather"`
	City       string   `json:"city"`
	TempMin    float64  `json:"temp_min"`
	TempMax    float64  `json:"temp_max"`
	Tags       []string `json:"tags"`
	Created    string   `json:"created"`
	Updated    string   `json:"updated"`
	Exists     bool     `json:"exists"`
}

// Sync 按区间增量同步日记：exists 定位有内容的日期，逐日取全文。
func (d *DiarumSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if d.cfg.BaseURL == "" || d.cfg.APIKey == "" {
		return "", 0, fmt.Errorf("diarum 未配置：需要 DIARUM_BASE_URL 与 DIARUM_API_KEY（JWT 或 用户名:密码）")
	}

	loc := d.loc
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -d.cfg.LookbackDays)
	if cursor != "" {
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			// 重叠 7 天：补写/编辑近几天的日记会迟到，upsert 幂等。
			start = t.AddDate(0, 0, 1-diarumOverlapDays)
		}
	}
	end := now
	if start.After(end) {
		return cursor, 0, nil
	}

	// 一条请求拿整个区间的有日记日期。
	ex, err := d.fetchExists(ctx, start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return "", 0, err
	}
	// 取区间内有内容的日期（升序，cursor 语义更稳）。
	dates := make([]string, 0, len(ex.Entries))
	for _, e := range ex.Entries {
		if e.HasContent {
			dates = append(dates, e.Date)
		}
	}
	sort.Strings(dates)

	var (
		newCursor = cursor
		total     int
	)
	for _, day := range dates {
		dia, err := d.fetchDiary(ctx, day)
		if err != nil {
			return "", total, fmt.Errorf("拉取 %s 日记失败: %w", day, err)
		}
		if dia == nil {
			continue // exists 说有、by-date 说无（极端竞态）：跳过
		}
		ev := d.mapDiary(dia)
		if ev == nil {
			continue // 空日记（仅天气填充），不进时间轴
		}
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
		newCursor = day
	}
	return newCursor, total, nil
}

func (d *DiarumSource) fetchExists(ctx context.Context, start, end string) (*diarumExistsResp, error) {
	reqURL := fmt.Sprintf("%s/api/v1/diaries/exists?start=%s&end=%s", d.cfg.BaseURL, start, end)
	body, err := d.getJSON(ctx, reqURL)
	if err != nil {
		return nil, err
	}
	var ex diarumExistsResp
	if err := json.Unmarshal(body, &ex); err != nil {
		return nil, fmt.Errorf("解析 diarum exists 响应失败: %w", err)
	}
	return &ex, nil
}

func (d *DiarumSource) fetchDiary(ctx context.Context, day string) (*diarumDiary, error) {
	reqURL := fmt.Sprintf("%s/api/v1/diaries/by-date/%s", d.cfg.BaseURL, day)
	body, err := d.getJSON(ctx, reqURL)
	if err != nil {
		return nil, err
	}
	var dia diarumDiary
	if err := json.Unmarshal(body, &dia); err != nil {
		return nil, fmt.Errorf("解析 diarum 日记失败: %w", err)
	}
	if !dia.Exists || dia.ID == "" {
		return nil, nil
	}
	return &dia, nil
}

// getJSON 带鉴权 GET；401 时对凭证形态自动重新登录重试一次。
func (d *DiarumSource) getJSON(ctx context.Context, reqURL string) ([]byte, error) {
	body, code, err := d.doGet(ctx, reqURL)
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized && isCredentialPair(d.cfg.APIKey) {
		// 凭证形态：服务端可能已吊销该 JWT，强制重登后重试一次。
		d.mu.Lock()
		d.jwt, d.jwtExp = "", time.Time{}
		d.mu.Unlock()
		body, code, err = d.doGet(ctx, reqURL)
		if err != nil {
			return nil, err
		}
	}
	if code == http.StatusUnauthorized {
		hint := "DIARUM_API_KEY 无效或已过期（JWT 30 天有效，请重新登录获取；也可改填 用户名:密码 由观复自动续期）"
		if isJWT(d.cfg.APIKey) {
			if exp, ok := jwtExpiry(d.cfg.APIKey); ok && time.Now().After(exp) {
				hint = fmt.Sprintf("DIARUM_API_KEY 已于 %s 过期，请重新登录获取 JWT（或改填 用户名:密码）",
					exp.Local().Format("2006-01-02"))
			}
		}
		return nil, fmt.Errorf("diarum 鉴权失败（HTTP 401）：%s", hint)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("diarum 返回 HTTP %d: %s", code, truncate(string(body), 200))
	}
	return body, nil
}

func (d *DiarumSource) doGet(ctx context.Context, reqURL string) ([]byte, int, error) {
	token, err := d.token(ctx)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求 diarum 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return body, resp.StatusCode, nil
}

// token 取当前可用的 JWT：直传形态直接用；凭证形态走登录缓存。
func (d *DiarumSource) token(ctx context.Context) (string, error) {
	if d.cfg.APIKey == "" {
		return "", fmt.Errorf("DIARUM_API_KEY 未配置（JWT 或 用户名:密码）")
	}
	if !isCredentialPair(d.cfg.APIKey) {
		return d.cfg.APIKey, nil // 直传形态（JWT / 长效 token）
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.jwt != "" && time.Now().Before(d.jwtExp) {
		return d.jwt, nil
	}
	user, pass, _ := strings.Cut(d.cfg.APIKey, ":")
	jwt, exp, err := d.login(ctx, user, pass)
	if err != nil {
		return "", err
	}
	d.jwt, d.jwtExp = jwt, exp
	return jwt, nil
}

// login 用账号密码换 JWT，并解析 exp 计算缓存期限（留 1 天边际）。
func (d *DiarumSource) login(ctx context.Context, user, pass string) (string, time.Time, error) {
	payload, _ := json.Marshal(map[string]string{"usernameOrEmail": user, "password": pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.BaseURL+"/api/v1/auth/login", strings.NewReader(string(payload)))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("diarum 登录失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("diarum 登录返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &lr); err != nil || lr.Token == "" {
		return "", time.Time{}, fmt.Errorf("diarum 登录响应无 token")
	}
	// 解析 JWT exp（不验签，token 刚从服务端 HTTPS 取得）。
	exp := time.Now().Add(7 * 24 * time.Hour) // 兜底：7 天
	if e, ok := jwtExpiry(lr.Token); ok {
		exp = e.Add(-24 * time.Hour) // 提前 1 天重登，避开临界过期
	}
	return lr.Token, exp, nil
}

// jwtExpiry 从 JWT 中段解出 exp（unix 秒）。失败返回 false。
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// moodEmoji 心情 1-5 → emoji（0 表示未记录）。
func moodEmoji(mood int) string {
	switch mood {
	case 1:
		return "😞"
	case 2:
		return "🙁"
	case 3:
		return "😐"
	case 4:
		return "🙂"
	case 5:
		return "😄"
	}
	return ""
}

// mapDiary 映射日记。返回 nil 表示空日记（仅天气自动填充），调用方跳过。
func (d *DiarumSource) mapDiary(dia *diarumDiary) *model.Event {
	text := strings.TrimSpace(htmlToText(dia.Content))
	hasContent := dia.Mood != 0 || len(dia.MoodStates) > 0 || len(dia.Scenarios) > 0 || len(dia.Tags) > 0 || strings.TrimSpace(text) != ""
	if !hasContent {
		return nil
	}
	day := dia.Date
	if len(day) >= 10 {
		day = day[:10]
	}
	// date 形如 "2026-10-15"（服务端 DateOnly 已裁好）；防御性再校验。
	if _, err := time.ParseInLocation("2006-01-02", day, d.loc); err != nil {
		return nil
	}

	ev := model.NewDayEvent(d.Name(), dia.ID, model.EventDiary, day)
	ev.Title = firstLine(text, 24)
	if ev.Title == "" {
		ev.Title = "日记"
	}
	ev.Summary = diarySummary(dia)
	ev.SourceURL = fmt.Sprintf("%s/?date=%s", d.cfg.BaseURL, day)

	ev.Detail = map[string]any{
		"content":      truncate(dia.Content, 20000), // HTML 原文（前端可渲染富文本预览）
		"content_text": truncate(text, 4000),         // 纯文本（标题/摘要/折叠预览用）
		"created":      dia.Created,
		"updated":      dia.Updated,
	}
	if dia.Mood != 0 {
		ev.Detail["mood"] = dia.Mood
	}
	if len(dia.MoodStates) > 0 {
		ev.Detail["mood_states"] = dia.MoodStates
	}
	if len(dia.Scenarios) > 0 {
		ev.Detail["scenarios"] = dia.Scenarios
	}
	if len(dia.Tags) > 0 {
		ev.Detail["tags"] = dia.Tags
	}
	if dia.Weather != "" {
		ev.Detail["weather"] = dia.Weather
	}
	if dia.City != "" {
		ev.Detail["city"] = dia.City
	}
	if dia.TempMin != 0 || dia.TempMax != 0 {
		ev.Detail["temp_min"] = dia.TempMin
		ev.Detail["temp_max"] = dia.TempMax
	}
	return ev
}

// diarySummary 紧凑摘要：心情/状态 · 天气 · 气温 · 标签。
func diarySummary(dia *diarumDiary) string {
	parts := make([]string, 0, 4)
	if dia.Mood != 0 {
		if states := joinNonEmpty(dia.MoodStates, "/"); states != "" {
			parts = append(parts, fmt.Sprintf("心情 %s", states))
		} else {
			parts = append(parts, "心情已记录")
		}
	} else if states := joinNonEmpty(dia.MoodStates, "/"); states != "" {
		parts = append(parts, states)
	}
	if dia.Weather != "" {
		parts = append(parts, dia.Weather)
	}
	if dia.TempMin != 0 || dia.TempMax != 0 {
		parts = append(parts, fmt.Sprintf("%.0f~%.0f°C", dia.TempMin, dia.TempMax))
	}
	if tags := joinNonEmpty(dia.Tags, " #"); tags != "" {
		parts = append(parts, "#"+tags)
	}
	return joinNonEmpty(parts, " · ")
}

// htmlToText 去 HTML 标签与实体，保留换行（日记 content 为富文本 HTML）。
func htmlToText(s string) string {
	if s == "" {
		return ""
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(s, "&nbsp;", " "), "&#160;", " ")
	normalized = strings.ReplaceAll(normalized, "<br>", "\n")
	normalized = strings.ReplaceAll(normalized, "<br/>", "\n")
	normalized = strings.ReplaceAll(normalized, "<br />", "\n")
	normalized = strings.ReplaceAll(normalized, "</p>", "\n")
	normalized = strings.ReplaceAll(normalized, "</div>", "\n")
	normalized = strings.ReplaceAll(normalized, "</li>", "\n")
	var b strings.Builder
	inTag := false
	for _, r := range normalized {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// firstLine 取首个非空行并截断（runes 计）。
func firstLine(text string, maxRunes int) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > maxRunes {
			return string(runes[:maxRunes]) + "…"
		}
		return line
	}
	return ""
}

// 编译期断言：DiarumSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*DiarumSource)(nil)
