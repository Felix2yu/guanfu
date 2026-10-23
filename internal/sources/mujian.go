package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// mujianOverlapDays 每次增量同步回退的天数（补录/编辑过的历史记录会迟到）。
const mujianOverlapDays = 7

// MujianSource 幕间（mujian）演出记录适配器。
//
// 上游接口（2026-10-16 读源码确认，backend/internal/handlers/handlers.go:75）：
//
//	GET {base}/api/records
//	  Query:  start/end（unix 秒或 YYYY-MM-DD，end 含当天）,
//	          limit（默认 500，硬上限 2000）, offset
//	  Resp:   {records:[…], total:N, search_truncated:bool}
//	  item:   {id, name, date(unix秒), dateText, city, address, rating(int),
//	           coordinate:{latitude,longitude}, coverFile, coverThumb}
//	          id 上游已改为字符串（2026-10-23 现网同步报错实证）→ flexID
//	票根:   GET {base}/api/records/{id}/photos → {photos:[{id,recordId,fileName,sort}]}
//	         静态访问 {base}/uploads/{fileName}（无认证）
//	认证:   可选 MJ_AUTH_TOKEN；未配置则放通。配置后支持
//	         Authorization: Bearer / X-Auth-Token / ?token= 三种传法。
//
// 坐标系：mujian 以 GCJ-02 存储（geo 包注释实证），本适配器在入库前转 WGS-84
// （model.GCJ02ToWGS84，与 mujian ICS 输出边界同算法，差分验证过）。
//
// cursor：YYYY-MM-DD（已同步到的日期）；首轮回溯 LookbackDays（默认 365，
// 演出记录历史价值高）。按天查询，与时间轴按天聚合的消费模式一致。
type MujianSource struct {
	cfg config.SourceConfig
	loc *time.Location
}

func NewMujianSource(cfg config.SourceConfig) *MujianSource {
	// 适配器自报家门：认证方式与 Key 请求头都靠它解析，
	// 不依赖调用方记得填 Name（测试与页面配置两条路径都会经过这里）。
	if cfg.Name == "" {
		cfg.Name = "mujian"
	}
	return &MujianSource{cfg: cfg, loc: model.LocalLocation()}
}

func (m *MujianSource) Name() string { return "mujian" }

func (m *MujianSource) Interval() time.Duration {
	if m.cfg.Interval > 0 {
		return m.cfg.Interval
	}
	return 6 * time.Hour // 演出记录写入频率低
}

type mujianRecord struct {
	ID         flexID `json:"id"`
	Name       string `json:"name"`
	Date       int64  `json:"date"` // unix 秒（mujian 本地时间基准）
	City       string `json:"city"`
	Address    string `json:"address"`
	Rating     int    `json:"rating"`
	CoverFile  string `json:"coverFile"`
	CoverThumb string `json:"coverThumb"`
	Coordinate *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"coordinate"`
}

type mujianListResp struct {
	Records         []mujianRecord `json:"records"`
	Total           int            `json:"total"`
	SearchTruncated bool           `json:"search_truncated"`
}

type mujianPhotosResp struct {
	Photos []mujianPhoto `json:"photos"`
}

// mujianPhoto 票根照片条目（GET /api/records/{id}/photos）。
type mujianPhoto struct {
	ID       flexID `json:"id"`
	RecordID flexID `json:"recordId"`
	FileName string `json:"fileName"` // 存储键，形如 covers/<hash>.<ext>
	Sort     int    `json:"sort"`
}

// Sync 按天增量拉取演出记录。
func (m *MujianSource) Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (string, int, error) {
	if m.cfg.BaseURL == "" {
		return "", 0, fmt.Errorf("mujian 未配置：需要 MUJIAN_BASE_URL")
	}

	loc := m.loc
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -m.cfg.LookbackDays)
	if cursor != "" {
		if t, err := time.ParseInLocation("2006-01-02", cursor, loc); err == nil {
			// 重叠 7 天：用户可能补录/编辑过去一周的演出（mujian 记录是手工录入的）。
			start = t.AddDate(0, 0, 1-mujianOverlapDays)
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
	// 按天查询：start=end=当日（end 含当天）。跨度大时逐天推进，
	// 每轮请求量小且失败重试粒度细。
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		n, err := m.syncDay(ctx, day, upsert)
		if err != nil {
			return "", total, err
		}
		total += n
		newCursor = day
	}
	return newCursor, total, nil
}

// syncDay 同步单日：先拉记录列表（limit 2000 单页覆盖单日量级），
// 再逐条拉票根照片。照片拉取失败不阻塞记录本身（降级为无媒体事件）。
func (m *MujianSource) syncDay(ctx context.Context, day string, upsert func(*model.Event) error) (int, error) {
	records, err := m.fetchRecords(ctx, day)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range records {
		rec := &records[i]
		ev, err := m.mapRecord(rec)
		if err != nil {
			return n, fmt.Errorf("映射演出 #%s 失败: %w", rec.ID, err)
		}
		// 票根照片：逐条拉；失败仅降级（该事件无媒体），不影响其他记录。
		if photos, perr := m.fetchPhotos(ctx, rec.ID.String()); perr == nil && len(photos) > 0 {
			media := make([]model.MediaRef, 0, len(photos))
			for _, p := range photos {
				if p.FileName == "" {
					continue
				}
				media = append(media, model.MediaRef{
					Kind: "photo",
					ID:   p.FileName, // 存储键即媒体引用；ProxyPath 由 API 层按源拼装
				})
			}
			if len(media) > 0 {
				ev.Media = media
			}
		}
		if err := upsert(ev); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (m *MujianSource) fetchRecords(ctx context.Context, day string) ([]mujianRecord, error) {
	q := url.Values{}
	q.Set("start", day)
	q.Set("end", day)
	q.Set("limit", "2000") // 上游硬上限，单日量级远小于此
	q.Set("offset", "0")
	reqURL := m.cfg.BaseURL + "/api/records?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if err := m.setAuth(req); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 mujian 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mujian 返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var lr mujianListResp
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("解析 mujian 响应失败: %w", err)
	}
	return lr.Records, nil
}

func (m *MujianSource) fetchPhotos(ctx context.Context, recordID string) ([]mujianPhoto, error) {
	reqURL := fmt.Sprintf("%s/api/records/%s/photos", m.cfg.BaseURL, url.PathEscape(recordID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if err := m.setAuth(req); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 mujian 票根失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mujian 票根 HTTP %d", resp.StatusCode)
	}
	var pr mujianPhotosResp
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("解析 mujian 票根失败: %w", err)
	}
	// 按 sort 升序（相册展示顺序与 mujian 前端一致）。
	photos := pr.Photos
	sort.SliceStable(photos, func(i, j int) bool { return photos[i].Sort < photos[j].Sort })
	return photos, nil
}

// setAuth 按配置的认证方式发 token（默认 X-Auth-Token；也可改 Bearer/Basic）。
func (m *MujianSource) setAuth(req *http.Request) error {
	return m.cfg.ApplyAuth(req)
}

// mapRecord 映射演出记录。date 为 unix 秒，按本地时区解释（mujian dateText
// 为 "2006-01-02 15:04" 本地时间，与之一致）。
func (m *MujianSource) mapRecord(rec *mujianRecord) (*model.Event, error) {
	occurred := time.Unix(rec.Date, 0).In(m.loc)
	ev := &model.Event{
		Source:        m.Name(),
		ExternalID:    rec.ID.String(),
		EventType:     model.EventPerformance,
		TimePrecision: model.PrecisionSecond,
		OccurredAt:    occurred.UTC().Unix(),
		LocalDate:     occurred.Format("2006-01-02"),
		Title:         strings.TrimSpace(rec.Name),
		Detail: map[string]any{
			"city":    rec.City,
			"address": rec.Address,
		},
		SourceURL: fmt.Sprintf("%s/#/records/%s", m.cfg.BaseURL, rec.ID),
	}
	if ev.Title == "" {
		ev.Title = "演出"
	}
	if rec.Rating > 0 {
		ev.Detail["rating"] = rec.Rating
	}
	// 摘要行：城市 + 评分（紧凑单行，与用户 UI 偏好一致）。
	var parts []string
	if rec.City != "" {
		parts = append(parts, rec.City)
	}
	if rec.Rating > 0 {
		parts = append(parts, fmt.Sprintf("评分 %d", rec.Rating))
	}
	ev.Summary = strings.Join(parts, " · ")

	// 坐标：GCJ-02 → WGS-84（与 mujian ICS 输出边界同算法）。
	if rec.Coordinate != nil && rec.Coordinate.Latitude != 0 && rec.Coordinate.Longitude != 0 {
		wLat, wLng := model.GCJ02ToWGS84(rec.Coordinate.Latitude, rec.Coordinate.Longitude)
		ev.Latitude = wLat
		ev.Longitude = wLng
		if rec.Address != "" {
			ev.LocationName = rec.Address
		} else if rec.City != "" {
			ev.LocationName = rec.City
		}
	}
	return ev, nil
}

// 编译期断言：MujianSource 实现 Adapter。
var _ interface {
	Name() string
	Interval() time.Duration
	Sync(context.Context, string, func(*model.Event) error) (string, int, error)
} = (*MujianSource)(nil)
