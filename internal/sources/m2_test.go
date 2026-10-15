package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// ---------- mujian ----------

func TestMujianSyncMappingAndCoordinateConversion(t *testing.T) {
	var seen []string
	// 记录日期（unix 秒，本地时区基准）。
	recUnix := time.Date(2026, 10, 9, 20, 0, 0, 0, model.LocalLocation()).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		if r.Header.Get("X-Auth-Token") != "mj-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/records/77/photos"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"photos": []map[string]any{
					{"id": 1, "recordId": 77, "fileName": "covers/b.avif", "sort": 2},
					{"id": 2, "recordId": 77, "fileName": "covers/a.avif", "sort": 1},
				},
			})
		default: // /api/records：仅当查询日期覆盖记录日期才返回（按天查询）
			start := r.URL.Query().Get("start")
			end := r.URL.Query().Get("end")
			recDate := time.Unix(recUnix, 0).In(model.LocalLocation()).Format("2006-01-02")
			if start <= recDate && recDate <= end {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"records": []map[string]any{
						{
							"id": 77, "name": "某乐队专场", "date": recUnix,
							"city": "上海", "address": "MAO Livehouse", "rating": 5,
							"coordinate": map[string]any{"latitude": 31.2304, "longitude": 121.4737}, // GCJ-02 粘贴值
						},
					},
					"total": 1,
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"records": []map[string]any{}, "total": 0})
			}
		}
	}))
	defer srv.Close()

	src := NewMujianSource(config.SourceConfig{BaseURL: srv.URL, APIKey: "mj-token", LookbackDays: 365})
	var events []*model.Event
	cursor, count, err := src.Sync(context.Background(), "", func(ev *model.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("期望 1 条演出，实际 %d", count)
	}
	if _, perr := time.Parse("2006-01-02", cursor); perr != nil {
		t.Fatalf("cursor 格式异常: %q", cursor)
	}

	e := events[0]
	if e.EventType != model.EventPerformance || e.ExternalID != "77" {
		t.Fatalf("事件标识异常: %s/%s", e.EventType, e.ExternalID)
	}
	if e.Title != "某乐队专场" || e.Summary != "上海 · 评分 5" {
		t.Fatalf("标题/摘要异常: %q / %q", e.Title, e.Summary)
	}
	// 坐标必须已转 WGS-84（与差分验证值逐位一致）。
	wantLat, wantLng := model.GCJ02ToWGS84(31.2304, 121.4737)
	if e.Latitude != wantLat || e.Longitude != wantLng {
		t.Fatalf("坐标转换异常: got (%.10f,%.10f) want (%.10f,%.10f)", e.Latitude, e.Longitude, wantLat, wantLng)
	}
	if e.LocationName != "MAO Livehouse" {
		t.Fatalf("location_name 异常: %q", e.LocationName)
	}
	// 票根照片：按 sort 升序（a 在 b 前）。
	if len(e.Media) != 2 || e.Media[0].ID != "covers/a.avif" || e.Media[1].ID != "covers/b.avif" {
		t.Fatalf("票根媒体异常: %+v", e.Media)
	}
	// 按天查询：start 与 end 为同一天（time 轴按天消费）。
	// seen 条目含 path，先剥掉再解析 query。
	q0, perr := url.ParseQuery(seen[0][strings.Index(seen[0], "?")+1:])
	if perr != nil {
		t.Fatalf("查询串解析失败: %v", perr)
	}
	if q0.Get("start") == "" || q0.Get("start") != q0.Get("end") {
		t.Fatalf("应按天同日查询: start=%q end=%q", q0.Get("start"), q0.Get("end"))
	}
	// 拉取了票根（逐条调用）。
	if !strings.Contains(strings.Join(seen, " "), "/api/records/77/photos") {
		t.Fatalf("未拉取票根照片: %v", seen)
	}
}

func TestMujianUnconfigured(t *testing.T) {
	src := NewMujianSource(config.SourceConfig{})
	if _, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
		t.Fatal("期望未配置错误")
	}
}

// ---------- zhichang ----------

func TestZhichangSyncDayAggregation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/metrics":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metrics": []map[string]any{
					{"metric": "step_count", "display_name_zh": "步数", "preferred_unit": "count"},
					{"metric": "heart_rate", "display_name_zh": "心率", "preferred_unit": "bpm"},
					{"metric": "sleep_analysis", "display_name_zh": "睡眠时长", "preferred_unit": "hr"},
				},
			})
		case "/api/daily":
			if r.URL.Query().Get("metrics") == "" || r.URL.Query().Get("from") == "" || r.URL.Query().Get("to") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"series": map[string]any{
					"step_count": []map[string]any{
						{"date": "2026-10-14", "metric": "step_count", "value": 8432.4, "sample_count": 3},
						{"date": "2026-10-15", "metric": "step_count", "value": 12001.0, "sample_count": 3},
					},
					"heart_rate": []map[string]any{
						{"date": "2026-10-14", "metric": "heart_rate", "value": 78.13, "sample_count": 3},
					},
					"sleep_analysis": []map[string]any{
						{"date": "2026-10-14", "metric": "sleep_analysis", "value": 7.25, "sample_count": 1},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	src := NewZhichangSource(config.SourceConfig{BaseURL: srv.URL, LookbackDays: 30})
	var events []*model.Event
	cursor, count, err := src.Sync(context.Background(), "", func(ev *model.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	// 两个有数据的日期 → 2 条 health_day。
	if count != 2 {
		t.Fatalf("期望 2 天汇总，实际 %d", count)
	}
	// cursor 直接推到 end（无数据日不卡 cursor）。
	if _, perr := time.Parse("2006-01-02", cursor); perr != nil {
		t.Fatalf("cursor 格式异常: %q", cursor)
	}

	byDate := map[string]*model.Event{}
	for _, e := range events {
		if e.EventType != model.EventHealthDay || e.TimePrecision != model.PrecisionDay {
			t.Fatalf("事件类型异常: %s/%s", e.EventType, e.TimePrecision)
		}
		byDate[e.LocalDate] = e
	}
	e := byDate["2026-10-14"]
	if e == nil {
		t.Fatal("缺少 2026-10-14 汇总")
	}
	if e.Title != "健康日报" {
		t.Fatalf("标题异常: %q", e.Title)
	}
	steps := e.Detail["step_count"].(map[string]any)
	if steps["value"].(float64) != 8432.4 || steps["display"] != "8,432" || steps["label"] != "步数" {
		t.Fatalf("步数 detail 异常: %v", steps)
	}
	sleep := e.Detail["sleep_analysis"].(map[string]any)
	if sleep["display"] != "7.3h" {
		t.Fatalf("睡眠格式化异常: %v", sleep)
	}
	// 摘要按配置顺序（步数优先），最多 4 项。
	if !strings.HasPrefix(e.Summary, "步数 8,432") {
		t.Fatalf("摘要顺序异常: %q", e.Summary)
	}
	// 全天事件时刻为当地中午。
	if e.LocalTime != "12:00" && e.LocalTime != "" {
		t.Fatalf("全天事件时刻异常: %q", e.LocalTime)
	}
}

func TestZhichangUnitsFailureStillSyncs(t *testing.T) {
	// /api/metrics 挂掉时，values 照常入库（label 回落 metric 名）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/daily" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"series": map[string]any{
					"step_count": []map[string]any{{"date": "2026-10-15", "metric": "step_count", "value": 100}},
				},
			})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	src := NewZhichangSource(config.SourceConfig{BaseURL: srv.URL, LookbackDays: 7})
	var events []*model.Event
	if _, _, err := src.Sync(context.Background(), "", func(ev *model.Event) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("字典失败不应中断同步: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("期望 1 条，实际 %d", len(events))
	}
	detail := events[0].Detail["step_count"].(map[string]any)
	if detail["label"] != "step_count" {
		t.Fatalf("label 应回落 metric 名: %v", detail)
	}
}

// ---------- dawarich ----------

func TestDawarichSyncConfirmedOnlyAndDurationMinutes(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RawQuery)
		if r.Header.Get("Authorization") != "Bearer dw-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		visits := []map[string]any{
			{
				"id": 1, "name": "公司", "status": "confirmed", "confidence": 0.9,
				"started_at": "2026-10-14T09:00:18+08:00", "ended_at": "2026-10-14T10:41:01+08:00",
				"duration": 101, // 分钟（timeline 的 journey 才是秒，别混）
				"place":    map[string]any{"latitude": 31.2304, "longitude": 121.4737, "id": 5},
			},
			{
				"id": 2, "name": "Suggested place", "status": "suggested", "confidence": 0.5,
				"started_at": "2026-10-14T13:00:00+08:00", "ended_at": "2026-10-14T13:30:00+08:00",
				"duration": 30,
				"place":    map[string]any{"latitude": 31.24, "longitude": 121.48, "id": 6},
			},
			{
				"id": 3, "name": "", "status": "confirmed", "confidence": 0.8,
				"started_at": "2026-10-14T19:00:00+08:00", "ended_at": "2026-10-14T20:10:00+08:00",
				"duration": 70,
				"place":    map[string]any{"latitude": 31.2000, "longitude": 121.5000, "id": 7},
			},
		}
		page := r.URL.Query().Get("page")
		if page == "1" {
			// 仅当查询窗口覆盖 2026-10-14 才返回（按天查询，与真实行为一致）。
			startAt := r.URL.Query().Get("start_at")
			if strings.HasPrefix(startAt, "2026-10-14") {
				_ = json.NewEncoder(w).Encode(visits)
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
			}
		} else {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer srv.Close()

	src := NewDawarichSource(config.SourceConfig{BaseURL: srv.URL, APIKey: "dw-key", LookbackDays: 30})
	var events []*model.Event
	_, count, err := src.Sync(context.Background(), "", func(ev *model.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	// suggested 被过滤：2 条入库。
	if count != 2 {
		t.Fatalf("期望 2 条（过滤 suggested），实际 %d", count)
	}

	e := events[0]
	if e.EventType != model.EventLocationVisit {
		t.Fatalf("事件类型异常: %s", e.EventType)
	}
	// duration 分钟原样入 detail；摘要按小时分格式。
	if e.Detail["duration_minutes"].(float64) != 101 {
		t.Fatalf("duration 异常: %v", e.Detail)
	}
	if e.Summary != "停留 1 小时 41 分" {
		t.Fatalf("时长摘要异常: %q", e.Summary)
	}
	// 坐标 WGS-84 直通（不转换）。
	if e.Latitude != 31.2304 || e.Longitude != 121.4737 {
		t.Fatalf("坐标应直通: (%v,%v)", e.Latitude, e.Longitude)
	}
	// started_at 本地归日。
	if e.LocalDate != "2026-10-14" {
		t.Fatalf("local_date 异常: %s", e.LocalDate)
	}
	// 空名 confirmed → 坐标兜底标题。
	if events[1].Title != "地点 31.2000,121.5000" {
		t.Fatalf("空名兜底异常: %q", events[1].Title)
	}
	// 查询参数带时区偏移与 per_page（回溯窗口内必有一天是 2026-10-14）。
	all := strings.Join(seen, " ")
	if !strings.Contains(all, "start_at=2026-10-14T00%3A00%3A00%2B08%3A00") {
		t.Fatalf("start_at 时区参数异常: %v", seen)
	}
	if !strings.Contains(all, "per_page=500") {
		t.Fatalf("per_page 异常: %v", seen)
	}
}

func TestDawarichUnconfigured(t *testing.T) {
	// 缺 key 必须明确报错（key 是全权凭证，不能静默跳过）。
	src := NewDawarichSource(config.SourceConfig{BaseURL: "http://127.0.0.1:1"})
	if _, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
		t.Fatal("期望缺 key 报错")
	}
}

// ---------- bili-history ----------

func TestBiliHistorySyncDateRangeAndMedia(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"records": []map[string]any{
					{
						"title": "测试视频", "bvid": "BV1xx411c7mD", "author_name": "测试UP",
						"view_at": time.Date(2026, 10, 15, 21, 30, 0, 0, model.LocalLocation()).Unix(),
						"duration": 600, "tag_name": "科技", "cover": "https://i0.hdslb.com/cover.jpg",
						"progress": 300,
					},
				},
				"total": 1, "size": 100, "current": 1,
			},
		})
	}))
	defer srv.Close()

	src := NewBiliHistorySource(config.SourceConfig{BaseURL: srv.URL, LookbackDays: 90})
	var events []*model.Event
	_, count, err := src.Sync(context.Background(), "", func(ev *model.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("期望 1 条，实际 %d", count)
	}

	e := events[0]
	if e.EventType != model.EventVideoWatch {
		t.Fatalf("事件类型异常: %s", e.EventType)
	}
	// 幂等键 = bvid@view_at（同视频多次观看）。
	if !strings.HasPrefix(e.ExternalID, "BV1xx411c7mD@") {
		t.Fatalf("幂等键异常: %q", e.ExternalID)
	}
	if e.Title != "测试视频" || e.Summary != "测试UP · 科技 · 看完 50%" {
		t.Fatalf("标题/摘要异常: %q / %q", e.Title, e.Summary)
	}
	// 封面外链入 media。
	if len(e.Media) != 1 || e.Media[0].Kind != "cover" || e.Media[0].ID != "https://i0.hdslb.com/cover.jpg" {
		t.Fatalf("封面 media 异常: %+v", e.Media)
	}
	if e.LocalDate != "2026-10-15" {
		t.Fatalf("local_date 异常: %s", e.LocalDate)
	}
	// date_range 参数形态 YYYYMMDD-YYYYMMDD。
	if !strings.Contains(seen[0], "date_range=") || strings.Count(seen[0], "date_range=") != 1 {
		t.Fatalf("date_range 参数异常: %v", seen)
	}
	dr := seen[0][strings.Index(seen[0], "date_range=")+11:]
	dr = dr[:strings.Index(dr, "&")]
	parts := strings.Split(dr, "-")
	if len(parts) != 2 || len(parts[0]) != 8 || len(parts[1]) != 8 {
		t.Fatalf("date_range 格式异常: %q", dr)
	}
}

func TestFormatMetricValue(t *testing.T) {
	cases := []struct {
		v    float64
		unit string
		want string
	}{
		{8432.4, "count", "8,432"},
		{12001, "count", "12,001"},
		{78.13, "bpm", "78"},
		{7.25, "hr", "7.3h"},
		{62.5, "kg", "62.5kg"},
		{98, "%", "98%"},
		{432.6, "ms", "433ms"},
		{3.14159, "", "3.14159"},
		{12.5, "kcal", "13kcal"},
	}
	for _, c := range cases {
		if got := formatMetricValue(c.v, c.unit); got != c.want {
			t.Errorf("formatMetricValue(%v,%q) = %q, want %q", c.v, c.unit, got, c.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		minutes float64
		want    string
	}{
		{45, "停留 45 分钟"},
		{101, "停留 1 小时 41 分"},
		{120, "停留 2 小时"},
	}
	for _, c := range cases {
		if got := formatDuration(c.minutes); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.minutes, got, c.want)
		}
	}
}
