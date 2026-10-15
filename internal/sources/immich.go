package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// immichOverlapDays 每次增量同步回退的天数（手机端上传/补传会迟到，
// 元数据（坐标/收藏）也可能事后补写；upsert 幂等）。
const immichOverlapDays = 3

// immichPageSize 单页拉取条数（上游上限 1000；500 平衡单次响应体大小）。
const immichPageSize = 500

// immichMaxPages 单日翻页防御上限（500×40=2 万张/天，远超实际）。
const immichMaxPages = 40

// ImmichSource Immich 照片库适配器。
//
// 上游接口（2026-10-16 读 v3.3.1 源码核实，NestJS + Postgres）：
//
//	POST {base}/api/search/metadata
//	  Header: x-api-key（只读 key，最小权限 asset.read/view/download/statistics）
//	  Body:   {"takenAfter":"<ISO8601 带时区>","takenBefore":"…",
//	           "size":500,"page":1,"withExif":true}
//	  Resp:   {assets:{count,items:[…],nextPage:"2"|null}}
//	  —— takenAfter/takenBefore 过滤的是 asset.fileCreatedAt（绝对时刻，
//	     与本地时区归日一致；order 默认 fileCreatedAt desc）
//	  —— withExif:false 时 exifInfo 不下发，必须显式 true
//	  —— v3.2+ 新形态（filter/cursor）与旧扁平字段互斥，本适配器用扁平形态
//	  item: {id(uuid),type(IMAGE/VIDEO/AUDIO/OTHER),originalFileName,
//	         fileCreatedAt(ISO UTC),localDateTime,duration(ms,可空),
//	         width,height,isFavorite,exifInfo:{make,model,lensModel,
//	         dateTimeOriginal,latitude,longitude,city,state,country,description},
//	         tags:[{name}],people}
//	媒体：GET {base}/api/assets/{id}/thumbnail?size=thumbnail|preview|fullsize
//	      GET {base}/api/assets/{id}/original（大图/灯箱）
//	      （thumbnail 端点在素材缺失时可能 302 重定向，http.Client 自动跟随）
//
// 事件模型：**按天聚合成一条 photo 事件**（规划 §5.2「当日照片网格」）——
// 一天几十张照片各占一行会淹没时间轴；网格卡 + 懒加载 + 点击灯箱才是正解。
// ExternalID = 日期，天然幂等；媒体引用按拍摄时刻升序（网格从左到右=时间顺序）。
//
// 坐标：EXIF GPS 为 WGS-84，immich 原样存储，直接用。
//
// cursor：YYYY-MM-DD；首轮回溯 LookbackDays（默认 365）。
type ImmichSource struct {
	cfg config.SourceConfig
	loc *time.Location
}

func NewImmichSource(cfg config.SourceConfig) *ImmichSource {
	return &ImmichSource{cfg: cfg, loc: model.LocalLocation()}
}

func (s *ImmichSource) Name() string { return "immich" }

func (s *ImmichSource) Interval() time.Duration {
	if s.cfg.Interval > 0 {
		return s.cfg.Interval
	}
	return 6 * time.Hour // 照片时效性要求低（规划 §3.2）
}

// immichExif 资产 EXIF（withExif=true 时下发；指针/可空字段用 *T）。
type immichExif struct {
	Make             *string  `json:"make"`
	Model            *string  `json:"model"`
	LensModel        *string  `json:"lensModel"`
	DateTimeOriginal *string  `json:"dateTimeOriginal"`
	Latitude         *float64 `json:"latitude"`
	Longitude        *float64 `json:"longitude"`
	City             *string  `json:"city"`
	State            *string  `json:"state"`
	Country          *string  `json:"country"`
	Description      *string  `json:"description"`
}

type immichAsset struct {
	ID               string      `json:"id"`
	Type             string      `json:"type"` // IMAGE / VIDEO / AUDIO / OTHER
	OriginalFileName string      `json:"originalFileName"`
	FileCreatedAt    string      `json:"fileCreatedAt"` // ISO8601 UTC
	LocalDateTime    string      `json:"localDateTime"`
	Duration         *int64      `json:"duration"` // 毫秒（视频）
	Width            *int        `json:"width"`
	Height           *int        `json:"height"`
	IsFavorite       bool        `json:"isFavorite"`
	ExifInfo         *immichExif `json:"exifInfo"`
	Tags             []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

type immichSearchResp struct {
	Assets struct {
		Count    int           `json:"count"`
		Items    []immichAsset `json:"items"`
		NextPage *string       `json:"nextPage"`
	} `json:"assets"`
}

// Sync 按天增量同步照片：逐日 search/metadata 翻页拉全，聚合成一条事件。
func (s *ImmichSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if s.cfg.BaseURL == "" || s.cfg.APIKey == "" {
		return "", 0, fmt.Errorf("immich 未配置：需要 IMMICH_BASE_URL 与 IMMICH_API_KEY")
	}

	loc := s.loc
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -s.cfg.LookbackDays)
	if cursor != "" {
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			// 重叠 3 天：上传迟到 + 元数据补写；按天聚合重算无副作用。
			start = t.AddDate(0, 0, 1-immichOverlapDays)
		}
	}
	end := now
	if start.After(end) {
		return cursor, 0, nil
	}

	var (
		newCursor = cursor
		total     int
	)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		assets, err := s.syncDay(ctx, day)
		if err != nil {
			return "", total, err
		}
		if len(assets) == 0 {
			newCursor = day
			continue // 无照片日不卡 cursor
		}
		ev := s.mapDay(day, assets)
		if err := upsert(ev); err != nil {
			return "", total, err
		}
		total++
		newCursor = day
	}
	return newCursor, total, nil
}

// syncDay 拉取单日全部资产（翻页），按拍摄时刻升序返回。
func (s *ImmichSource) syncDay(ctx context.Context, day string) ([]immichAsset, error) {
	dayStart, err := time.ParseInLocation("2006-01-02", day, s.loc)
	if err != nil {
		return nil, fmt.Errorf("日期解析失败 %q", day)
	}
	dayEnd := dayStart.AddDate(0, 0, 1)

	var all []immichAsset
	for page := 1; page <= immichMaxPages; page++ {
		resp, err := s.searchPage(ctx, dayStart, dayEnd, page)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Assets.Items...)
		if len(resp.Assets.Items) < immichPageSize || resp.Assets.NextPage == nil {
			break
		}
	}
	// 拍摄时刻升序（网格时间顺序）；解析失败的排最后。
	sortAssetsByTime(all)
	return all, nil
}

func sortAssetsByTime(assets []immichAsset) {
	// 稳定排序：同刻照片保持上游相对顺序。
	for i := 1; i < len(assets); i++ {
		for j := i; j > 0; j-- {
			ti, okI := parseAssetTime(assets[j].FileCreatedAt)
			tj, okJ := parseAssetTime(assets[j-1].FileCreatedAt)
			if okI && okJ && ti.Before(tj) {
				assets[j], assets[j-1] = assets[j-1], assets[j]
				continue
			}
			break
		}
	}
}

func parseAssetTime(iso string) (time.Time, bool) {
	if iso == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		t, err = time.Parse(time.RFC3339, iso)
	}
	return t, err == nil
}

func (s *ImmichSource) searchPage(ctx context.Context, dayStart, dayEnd time.Time, page int) (*immichSearchResp, error) {
	body, _ := json.Marshal(map[string]any{
		"takenAfter":  dayStart.Format(time.RFC3339),
		"takenBefore": dayEnd.Format(time.RFC3339),
		"size":        immichPageSize,
		"page":        page,
		"withExif":    true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.BaseURL+"/api/search/metadata", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", s.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 immich 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("immich 返回 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var sr immichSearchResp
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("解析 immich 响应失败: %w", err)
	}
	return &sr, nil
}

// mapDay 把当日资产聚合成一条 photo 事件。
func (s *ImmichSource) mapDay(day string, assets []immichAsset) *model.Event {
	ev := model.NewDayEvent(s.Name(), day, model.EventPhoto, day)

	media := make([]model.MediaRef, 0, len(assets))
	var (
		images, videos, favorites, geo int
		cameras                        = map[string]int{}
		city                           string
		first, last                    string
		lat, lng                       float64
		hasGeo                         bool
	)
	for i := range assets {
		a := &assets[i]
		kind := "photo"
		if a.Type == "VIDEO" {
			kind = "video"
			videos++
		} else {
			images++
		}
		if a.IsFavorite {
			favorites++
		}
		ref := model.MediaRef{Kind: kind, ID: a.ID}
		if a.Width != nil {
			ref.Width = *a.Width
		}
		if a.Height != nil {
			ref.Height = *a.Height
		}
		media = append(media, ref)

		if t, ok := parseAssetTime(a.FileCreatedAt); ok {
			hm := t.In(s.loc).Format("15:04")
			if first == "" {
				first = hm
			}
			last = hm
		}
		if a.ExifInfo != nil {
			ex := a.ExifInfo
			if cam := cameraName(ex.Make, ex.Model); cam != "" {
				cameras[cam]++
			}
			if ex.Latitude != nil && ex.Longitude != nil && *ex.Latitude != 0 && *ex.Longitude != 0 {
				geo++
				if !hasGeo { // 取当天第一张带 GPS 的照片作为事件坐标
					hasGeo = true
					lat, lng = *ex.Latitude, *ex.Longitude
				}
			}
			if city == "" {
				city = firstNonEmpty(strPtr(ex.City), strPtr(ex.State), strPtr(ex.Country))
			}
		}
	}
	ev.Media = media

	// 标题：照片 N 张（· 视频 M 个）。
	titleParts := make([]string, 0, 2)
	if images > 0 {
		titleParts = append(titleParts, fmt.Sprintf("照片 %d 张", images))
	}
	if videos > 0 {
		titleParts = append(titleParts, fmt.Sprintf("视频 %d 个", videos))
	}
	if len(titleParts) == 0 {
		titleParts = append(titleParts, fmt.Sprintf("媒体 %d 项", len(assets)))
	}
	ev.Title = strings.Join(titleParts, " · ")

	// 摘要：主力相机 · 时段 · 收藏数。
	summaryParts := make([]string, 0, 3)
	if cam := topKey(cameras); cam != "" {
		summaryParts = append(summaryParts, cam)
	}
	if first != "" && last != "" && first != last {
		summaryParts = append(summaryParts, first+"–"+last)
	}
	if favorites > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("❤ %d", favorites))
	}
	ev.Summary = joinNonEmpty(summaryParts, " · ")

	// 坐标与地点：当天第一张带 GPS 的照片。
	if hasGeo {
		ev.Latitude = lat
		ev.Longitude = lng
		ev.LocationName = city
	}

	ev.Detail = map[string]any{
		"count":      len(assets),
		"images":     images,
		"videos":     videos,
		"favorites":  favorites,
		"geo_count":  geo,
		"first_time": first,
		"last_time":  last,
	}
	if cam := topKeys(cameras, 3); len(cam) > 0 {
		ev.Detail["cameras"] = cam
	}
	if city != "" {
		ev.Detail["city"] = city
	}
	return ev
}

func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// cameraName 组合 Make+Model（任一为空取另一个）。
func cameraName(make, model *string) string {
	mk, md := strPtr(make), strPtr(model)
	switch {
	case mk != "" && md != "":
		if strings.HasPrefix(strings.ToLower(md), strings.ToLower(mk)) {
			return md // "Apple" + "iPhone 15 Pro" → 不重复前缀
		}
		return mk + " " + md
	case md != "":
		return md
	default:
		return mk
	}
}

// topKey 取计数最高的键（并列取字母序小者，稳定）。
func topKey(m map[string]int) string {
	return topKeys(m, 1)[0]
}

func topKeys(m map[string]int, n int) []string {
	if len(m) == 0 {
		return []string{""}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 计数降序、同数键名升序（确定性输出）。
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if m[keys[j]] > m[keys[i]] || (m[keys[j]] == m[keys[i]] && keys[j] < keys[i]) {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	if n > len(keys) {
		n = len(keys)
	}
	return keys[:n]
}

// 编译期断言：ImmichSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*ImmichSource)(nil)
