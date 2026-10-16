// Package model 定义观复的统一事件模型。
//
// 设计要点（来自 docs/观复规划.md §4）：
//   - 全源统一落到 events 一张表，用 (source, external_id) 唯一约束保证
//     同步幂等：同一源同一条外部记录重复拉取只做更新，不产生重复事件。
//   - 时间双字段：occurred_at 存 UTC unix 秒（排序/区间查询用），
//     local_date 存本地时区归日字符串（按天展示用）。源侧时区五花八门
//     （本地时间字符串 / unix / ISO8601带偏移），统一在适配器内一次性换算。
//   - 坐标统一 WGS-84；GCJ-02 源（如高德系数据）必须在适配器内转换后再入库。
//   - 金额统一整数分（cents）+ currency，杜绝浮点累加误差。
package model

import "time"

// 事件类型常量。新增数据源时优先复用既有类型，类型爆炸会让时间轴筛选失去意义。
const (
	EventTransaction   = "transaction" // 记账流水（huozhi）
	EventPerformance   = "performance" // 演出/观影现场（mujian）
	EventDiary         = "diary"       // 日记（diarum）
	EventHealthDay     = "health_day"  // 健康日汇总（zhichang，一条/天）
	EventHealthSleep   = "health_sleep"
	EventHealthWorkout = "health_workout"
	EventPhoto         = "photo"          // 照片（immich）
	EventLocationVisit = "location_visit" // 到访（dawarich visit）
	EventLocationDay   = "location_day"   // 到访日汇总（dawarich journeys 聚合）
	EventVideoWatch    = "video_watch"    // B 站观看（bili-history）
	EventSocialEvent   = "social_event"   // 人情往来（qiansi）
	EventTaskDone      = "task_done"      // 任务完成（shenshi）

	// M4 增值源
	EventGiftMoney   = "gift_money"   // 礼金收付（qiansi 交易）
	EventAnniversary = "anniversary"  // 纪念日/生日（qiansi）
	EventReview      = "review"       // 每日复盘（shenshi）
	EventHabitCheck  = "habit_check"  // 习惯打卡（shenshi，按天聚合）
	EventSkyForecast = "sky_forecast" // 朝霞/晚霞预报（liuxia，按天聚合）
)

// 时间精度：second=时刻流，day=全天分区（日记、健康汇总等无具体时刻的事件）。
const (
	PrecisionSecond = "second"
	PrecisionDay    = "day"
)

// Event 一条聚合事件。detail_json 存各源特有字段，避免为长尾字段加列。
type Event struct {
	ID int64 `json:"id"`

	// Source 数据源名（huozhi/mujian/...），ExternalID 源侧唯一 ID。
	// 两者组成业务幂等键。
	Source     string `json:"source"`
	ExternalID string `json:"external_id"`

	EventType     string `json:"event_type"`
	TimePrecision string `json:"time_precision"`

	// OccurredAt UTC unix 秒；TimePrecision=day 的事件填当地中午 12:00，
	// 保证按天聚合与排序行为可预期。
	OccurredAt int64 `json:"occurred_at"`
	// LocalDate 本地时区归日（YYYY-MM-DD），时间轴的主查询键。
	LocalDate string `json:"local_date"`
	// LocalTime 本地时区时刻（HH:MM），由服务端按 GUANFU_TZ 计算后下发。
	// 前端不得自行用浏览器时区格式化（用户设备时区不可假定）。
	LocalTime string `json:"local_time,omitempty"`

	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`

	// 金额（分）与币种；非金额事件为零值。
	AmountCents int64  `json:"amount_cents,omitempty"`
	Currency    string `json:"currency,omitempty"`

	// WGS-84 坐标；无坐标事件为零值（注意 0,0 是几内亚湾，用指针语义更稳，
	// 但 SQLite 存 NULL + 查询过滤已够用，这里用零值 + HasLatLng 辅助判断）。
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`

	// LocationName 地点名（dawarich visit / mujian 场馆）。
	LocationName string `json:"location_name,omitempty"`

	// Media 关联媒体（照片/封面），经观复代理端点访问，token 不外泄。
	Media []MediaRef `json:"media,omitempty"`

	// Detail 各源特有字段的 JSON 原文（已解析为 map 便于直接透传前端）。
	Detail map[string]any `json:"detail,omitempty"`

	// SourceURL 深链回源站（如 huozhi 账单详情页）。
	SourceURL string `json:"source_url,omitempty"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// MediaRef 一条媒体引用。ProxyPath 形如 /api/v1/media/{source}/{externalID}/{mediaID}。
type MediaRef struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // photo / cover / thumbnail
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	ProxyPath string `json:"proxy_path,omitempty"`
}

// HasLatLng 判断是否携带有效坐标（排除 0,0 与缺省）。
func (e *Event) HasLatLng() bool {
	return e.Latitude != 0 || e.Longitude != 0
}

// NewDayEvent 构造一条全天事件的便捷方法（时刻固定为当地中午）。
// 中午而非零点：全天事件在按时刻排序的时间轴里落在"当天中段"，
// 与 zhichang/dawarich 的日汇总口径一致（否则会一律挤到 00:00 最前）。
func NewDayEvent(source, externalID, eventType, localDate string) *Event {
	loc := LocalLocation()
	day, err := time.ParseInLocation("2006-01-02", localDate, loc)
	if err != nil {
		day = time.Now().In(loc)
	}
	noon := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc)
	return &Event{
		Source:        source,
		ExternalID:    externalID,
		EventType:     eventType,
		TimePrecision: PrecisionDay,
		OccurredAt:    noon.UTC().Unix(),
		LocalDate:     localDate,
	}
}

// FillLocalTime 按本地时区计算 LocalTime 展示字段（入库列不存，输出时现算）。
func (e *Event) FillLocalTime() {
	e.LocalTime = time.Unix(e.OccurredAt, 0).In(LocalLocation()).Format("15:04")
}
