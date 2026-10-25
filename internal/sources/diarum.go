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
// 上游有两套互不相通的鉴权面（2026-10-25 读源码 + 线上实测确认）：
//
//	JWT 面（internal/auth，auth.Middleware 只认 Bearer 登录 JWT，30 天有效）：
//	  GET  {base}/api/v1/diaries/exists?start&end
//	    → {dates:[...], entries:[{date,mood,weather,city,temp_min,temp_max,has_content}]}
//	  GET  {base}/api/v1/diaries/by-date/:date → 完整日记（含 mood_states/tags/气温）
//	  POST {base}/api/v1/auth/login {usernameOrEmail,password} → {token}
//	公开面（internal/api/public.go，API token 经 ?token= 或 Bearer 头校验）：
//	  GET  {base}/api/v1/diaries?date=:date → 完整日记（字段与 by-date 一致）
//	  GET  {base}/api/v1/diaries?start&end  → {diaries:[{id,date,content,mood,scenarios,weather}]}
//
// DIARUM_API_KEY 三种形态，按形态选面（只看凭据本身，不受页面认证方式覆盖影响）：
//   - JWT（eyJ 开头三段式、中段可解出 claims）：直传，走 JWT 面；
//   - "用户名:密码"（含冒号且两段非空）：自动登录换 JWT 并缓存至临近过期，走 JWT 面；
//   - 其余一律视为设置页签发的 API token（MCP 同款）：Bearer 直传，走公开面。
//
// 策略：先拿区间内有内容的日期（JWT 面 1 次 exists，has_content 服务端判定；
// 公开面 1 次列表，但列表不回 tags/mood_states/气温，只能按 content/mood/scenarios
// 粗筛——「仅标签或心情状态、无正文无心情分」的日记在公开面会漏，服务端
// has_content 无法复刻，接受此差异），再逐日取全文。仅含自动天气填充、
// 无正文/心情/标签的「空日记」不进时间轴（与服务端 diaryHasContent 语义一致）。
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
	// 适配器自报家门：认证方式与 Key 请求头都靠它解析，
	// 不依赖调用方记得填 Name（测试与页面配置两条路径都会经过这里）。
	if cfg.Name == "" {
		cfg.Name = "diarum"
	}
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

// usesPublicAPI 判断凭据是否为 API token 形态（走公开接口 /api/v1/diaries）。
// JWT 与「用户名:密码」最终都变成 Bearer JWT，走 auth.Middleware 保护的接口；
// 其余一律视为 diarum 设置页签发的 API token。两个面的接口路径不同、鉴权面
// 不同（见类型注释），路由选择只看凭据形态，与页面上的认证方式覆盖无关。
func (d *DiarumSource) usesPublicAPI() bool {
	k := d.cfg.APIKey
	return k != "" && !isCredentialPair(k) && !isJWT(k)
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

// diarumPublicItem GET /api/v1/diaries?start&end 列表项（公开面，是完整日记的字段子集）。
type diarumPublicItem struct {
	ID        string   `json:"id"`
	Date      string   `json:"date"`
	Content   string   `json:"content"`
	Mood      int      `json:"mood"`
	Scenarios []string `json:"scenarios"`
	Weather   string   `json:"weather"`
}

// diarumPublicListResp 公开面区间列表响应。
type diarumPublicListResp struct {
	Diaries []diarumPublicItem `json:"diaries"`
	Total   int                `json:"total"`
}

// Sync 按区间增量同步日记：先定位有内容的日期，逐日取全文。
func (d *DiarumSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if d.cfg.BaseURL == "" || d.cfg.APIKey == "" {
		return "", 0, fmt.Errorf("diarum 未配置：需要 DIARUM_BASE_URL 与 DIARUM_API_KEY（JWT、用户名:密码 或 API token）")
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

	// 区间内有内容的日期（升序，cursor 语义更稳）。
	dates, err := d.contentDates(ctx, start, end)
	if err != nil {
		return "", 0, err
	}

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

// contentDates 拿区间内有内容的日期（升序）。按凭据形态选面：JWT 面 exists
// 自带服务端判定的 has_content；公开面列表字段是子集，只能先粗筛再逐日精判。
func (d *DiarumSource) contentDates(ctx context.Context, start, end time.Time) ([]string, error) {
	if d.usesPublicAPI() {
		lst, err := d.fetchPublicList(ctx, start, end)
		if err != nil {
			return nil, err
		}
		dates := make([]string, 0, len(lst.Diaries))
		for _, it := range lst.Diaries {
			if publicListHasContent(it) {
				dates = append(dates, it.Date)
			}
		}
		sort.Strings(dates)
		return dates, nil
	}
	ex, err := d.fetchExists(ctx, start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	dates := make([]string, 0, len(ex.Entries))
	for _, e := range ex.Entries {
		if e.HasContent {
			dates = append(dates, e.Date)
		}
	}
	sort.Strings(dates)
	return dates, nil
}

// fetchPublicList 公开面拿区间内全部日记记录（含仅天气填充的空日记，由粗筛剔除）。
func (d *DiarumSource) fetchPublicList(ctx context.Context, start, end time.Time) (*diarumPublicListResp, error) {
	reqURL := fmt.Sprintf("%s/api/v1/diaries?start=%s&end=%s", d.cfg.BaseURL, start.Format("2006-01-02"), end.Format("2006-01-02"))
	body, err := d.getJSON(ctx, reqURL)
	if err != nil {
		return nil, err
	}
	var lst diarumPublicListResp
	if err := json.Unmarshal(body, &lst); err != nil {
		return nil, fmt.Errorf("解析 diarum 公开列表响应失败: %w", err)
	}
	return &lst, nil
}

// publicListHasContent 公开面区间列表的粗筛：列表不回 tags/mood_states，
// 拿得到的内容/心情分/情景任一非空即视为有内容（逐日全量后再精判，
// 偏宽进不偏宽出——多取一天的 by-date 没有副作用，漏掉才是丢数据）。
func publicListHasContent(it diarumPublicItem) bool {
	return it.Mood != 0 || len(it.Scenarios) > 0 || strings.TrimSpace(it.Content) != ""
}

func (d *DiarumSource) fetchDiary(ctx context.Context, day string) (*diarumDiary, error) {
	// 单日全量在两个面上路径不同，响应字段一致（同一个 diaryResponse 构造）。
	reqURL := fmt.Sprintf("%s/api/v1/diaries/by-date/%s", d.cfg.BaseURL, day)
	if d.usesPublicAPI() {
		reqURL = fmt.Sprintf("%s/api/v1/diaries?date=%s", d.cfg.BaseURL, day)
	}
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
		switch {
		case d.usesPublicAPI():
			hint = "DIARUM_API_KEY（API token）无效或已被重置，到 diarum 设置 → API 重新生成"
		case isJWT(d.cfg.APIKey):
			if exp, ok := jwtExpiry(d.cfg.APIKey); ok && time.Now().After(exp) {
				hint = fmt.Sprintf("DIARUM_API_KEY 已于 %s 过期，请重新登录获取 JWT（或改填 用户名:密码 / API token）",
					exp.Local().Format("2006-01-02"))
			}
		default:
			// 用户名:密码 走到这里有两种可能：认证方式被覆盖成非 JWT（凭据被原样
			// 直发，登录根本没发生），或登录成功后新签的 JWT 仍被拒（服务端换密钥）。
			if d.cfg.AuthMode() != config.AuthJWT {
				hint = fmt.Sprintf("「用户名:密码」自动登录只在认证方式为 JWT 时生效，当前按 %s 直发，改回 JWT 即可", d.cfg.AuthMode())
				break
			}
			hint = "登录成功但 JWT 仍被拒绝（服务端可能更换了签名密钥或用户被删，检查 diarum 部署）"
		}
		return nil, fmt.Errorf("diarum 鉴权失败（HTTP 401）：%s", hint)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("diarum 返回 HTTP %d: %s", code, truncate(string(body), 200))
	}
	return body, nil
}

func (d *DiarumSource) doGet(ctx context.Context, reqURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, err
	}
	// 认证方式可被页面覆盖：
	//   jwt（默认）   —— 直传 JWT 或「用户名:密码」由 token() 换票；
	//   bearer        —— 原样当 Bearer 发（适合上游改成了长期 token 的场景）；
	//   none/api_key/basic —— 交给 ApplyAuth 按对应方式发。
	cfg := d.cfg
	cfg.Auth = d.cfg.AuthMode()
	switch cfg.Auth {
	case config.AuthJWT:
		token, err := d.token(ctx)
		if err != nil {
			return nil, 0, err
		}
		cfg.APIKey, cfg.Auth = token, config.AuthBearer
	case config.AuthBearer:
		if cfg.APIKey == "" {
			return nil, 0, fmt.Errorf("DIARUM_API_KEY 未配置（Bearer 方式需要一个 token）")
		}
	}
	if err := cfg.ApplyAuth(req); err != nil {
		return nil, 0, err
	}
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
