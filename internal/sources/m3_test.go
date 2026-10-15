package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// ---------- diarum ----------

// diarumFixture 起一个模拟 diarum 服务的测试服务器。
// 返回服务器与「已见请求」切片指针（含鉴权头）。
func diarumFixture(t *testing.T, jwt string, loginCalls *int) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" auth="+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/auth/login":
			if loginCalls != nil {
				*loginCalls++
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": jwt})
		case r.URL.Path == "/api/v1/diaries/exists":
			// 10-15 有内容；10-16 仅天气自动填充（has_content=false）。
			_ = json.NewEncoder(w).Encode(map[string]any{
				"dates": []string{"2026-10-16", "2026-10-15"},
				"entries": []map[string]any{
					{"date": "2026-10-16", "mood": 0, "weather": "晴", "city": "上海", "temp_min": 12, "temp_max": 18, "has_content": false},
					{"date": "2026-10-15", "mood": 4, "weather": "多云", "city": "上海", "temp_min": 13, "temp_max": 20, "has_content": true},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/api/v1/diaries/by-date/"):
			date := strings.TrimPrefix(r.URL.Path, "/api/v1/diaries/by-date/")
			if date == "2026-10-16" {
				// 空日记：exists 说无内容，by-date 也返回空。
				_ = json.NewEncoder(w).Encode(map[string]any{"date": date, "content": "", "exists": true, "id": "d16"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "d15", "date": date, "exists": true,
				"content":     "<p>今天去看了昆曲，</p><p>下午下了点雨。</p>",
				"mood":        4,
				"mood_states": []string{"平静"},
				"scenarios":   []string{"日常"},
				"weather":     "多云",
				"city":        "上海",
				"temp_min":    13,
				"temp_max":    20,
				"tags":        []string{"昆曲", "雨天"},
				"created":     "2026-10-15T20:01:00Z",
				"updated":     "2026-10-15T20:05:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestDiarumSyncJWTAndMapping(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InUxIn0.signature"
	srv, seen := diarumFixture(t, jwt, nil)

	src := NewDiarumSource(config.SourceConfig{BaseURL: srv.URL, APIKey: jwt, LookbackDays: 30})
	var events []*model.Event
	cursor, count, err := src.Sync(context.Background(), "", func(ev *model.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	// 空日记（仅天气）被过滤：只同步 10-15。
	if count != 1 {
		t.Fatalf("期望 1 条日记，实际 %d", count)
	}
	if cursor != "2026-10-15" {
		t.Fatalf("cursor 异常: %q", cursor)
	}
	// 所有请求都带 JWT 鉴权。
	for _, s := range *seen {
		if !strings.Contains(s, "auth=Bearer "+jwt) {
			t.Fatalf("请求缺少 JWT 鉴权: %s", s)
		}
	}
	// exists 区间查询（一次请求覆盖整个窗口）。
	if !strings.Contains((*seen)[0], "/api/v1/diaries/exists?start=") {
		t.Fatalf("未见 exists 区间查询: %v", *seen)
	}

	e := events[0]
	if e.Source != "diarum" || e.EventType != model.EventDiary || e.TimePrecision != model.PrecisionDay {
		t.Fatalf("事件标识异常: %s/%s/%s", e.Source, e.EventType, e.TimePrecision)
	}
	if e.ExternalID != "d15" || e.LocalDate != "2026-10-15" {
		t.Fatalf("外部 ID/日期异常: %s/%s", e.ExternalID, e.LocalDate)
	}
	// 标题取正文首行（HTML 已剥离）。
	if e.Title != "今天去看了昆曲，" {
		t.Fatalf("标题异常: %q", e.Title)
	}
	// 摘要：心情状态 · 天气 · 气温 · 标签。
	if e.Summary != "心情 平静 · 多云 · 13~20°C · #昆曲 #雨天" {
		t.Fatalf("摘要异常: %q", e.Summary)
	}
	// detail：正文纯文本 + 结构化字段。
	if got := e.Detail["content_text"].(string); got != "今天去看了昆曲，\n下午下了点雨。" {
		t.Fatalf("纯文本异常: %q", got)
	}
	if e.Detail["mood"].(int) != 4 || e.Detail["weather"].(string) != "多云" {
		t.Fatalf("detail 异常: %v", e.Detail)
	}
	tags := e.Detail["tags"].([]string)
	if len(tags) != 2 || tags[0] != "昆曲" {
		t.Fatalf("tags 异常: %v", tags)
	}
	// 深链回源站（规划 §4 diarum）。
	if e.SourceURL != srv.URL+"/?date=2026-10-15" {
		t.Fatalf("深链异常: %q", e.SourceURL)
	}
	// 全天事件时刻为当地中午。
	if e.LocalTime != "12:00" && e.LocalTime != "" {
		t.Fatalf("全天事件时刻异常: %q", e.LocalTime)
	}
}

func TestDiarumCredentialLoginAndCaching(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InUxIn0.signature"
	loginCalls := 0
	srv, _ := diarumFixture(t, jwt, &loginCalls)

	// "用户名:密码" 形态：先登录换 JWT，再带 JWT 请求。
	src := NewDiarumSource(config.SourceConfig{BaseURL: srv.URL, APIKey: "yufei:secret", LookbackDays: 30})
	if _, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil }); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if loginCalls != 1 {
		t.Fatalf("应恰好登录 1 次（缓存），实际 %d", loginCalls)
	}
	// 第二轮同步复用缓存 token，不再登录。
	if _, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil }); err != nil {
		t.Fatalf("第二轮 Sync 失败: %v", err)
	}
	if loginCalls != 1 {
		t.Fatalf("第二轮不应再登录，实际 %d", loginCalls)
	}
}

func TestDiarumUnconfigured(t *testing.T) {
	src := NewDiarumSource(config.SourceConfig{})
	if _, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
		t.Fatal("期望未配置错误")
	}
	// 只配 BaseURL 缺 key 同样报错（REST 必须鉴权）。
	src2 := NewDiarumSource(config.SourceConfig{BaseURL: "http://127.0.0.1:1"})
	if _, _, err := src2.Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
		t.Fatal("期望缺 key 错误")
	}
}

// ---------- immich ----------

// immichFixture 起一个模拟 immich 的测试服务器。
// assetsFn 按 (dayStart, dayEnd, page) 返回该页资产与 nextPage。
func immichFixture(t *testing.T, apiKey string, assetsFn func(dayStart, dayEnd string, page int) ([]map[string]any, *string)) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/search/metadata" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		takenAfter := body["takenAfter"].(string)
		takenBefore := body["takenBefore"].(string)
		items, next := assetsFn(takenAfter, takenBefore, int(body["page"].(float64)))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"assets": map[string]any{
				"count":    len(items),
				"items":    items,
				"nextPage": next,
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func mkAsset(id, typ string, createdAt string, fav bool, exif map[string]any) map[string]any {
	a := map[string]any{
		"id": id, "type": typ, "originalFileName": id + ".jpg",
		"fileCreatedAt": createdAt, "localDateTime": createdAt, "isFavorite": fav,
	}
	if exif != nil {
		a["exifInfo"] = exif
	}
	return a
}

func TestImmichSyncDayAggregation(t *testing.T) {
	// 2026-10-15 有 3 项（2 图 1 视频，其中 1 张带 GPS+相机）；
	// 10-14 有 1 张但首页即不满页；其余日期无数据。
	uuid := func(i int) string { return fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", i) }
	srv, bodies := immichFixture(t, "im-key", func(dayStart, dayEnd string, page int) ([]map[string]any, *string) {
		switch {
		case strings.HasPrefix(dayStart, "2026-10-15"):
			if page == 1 {
				items := []map[string]any{
					mkAsset(uuid(1), "IMAGE", "2026-10-15T04:02:00.000Z", false, map[string]any{
						"make": "Apple", "model": "iPhone 15 Pro",
						"latitude": 31.2323, "longitude": 121.4692, "city": "上海市",
					}),
					mkAsset(uuid(2), "VIDEO", "2026-10-15T06:30:00.000Z", true, nil),
					mkAsset(uuid(3), "IMAGE", "2026-10-15T10:33:00.000Z", true, map[string]any{
						"city": "苏州市",
					}),
				}
				return items, nil // count<size → 不再翻页
			}
			return nil, nil
		case strings.HasPrefix(dayStart, "2026-10-14"):
			if page == 1 {
				return []map[string]any{mkAsset(uuid(4), "IMAGE", "2026-10-14T12:00:00.000Z", false, nil)}, nil
			}
			return nil, nil
		default:
			return nil, nil
		}
	})

	src := NewImmichSource(config.SourceConfig{BaseURL: srv.URL, APIKey: "im-key", LookbackDays: 30})
	var events []*model.Event
	cursor, count, err := src.Sync(context.Background(), "", func(ev *model.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	// 两天有数据 → 2 条聚合事件（无数据日不产生事件、不卡 cursor）。
	if count != 2 {
		t.Fatalf("期望 2 条聚合事件，实际 %d", count)
	}
	if cursor != time.Now().In(model.LocalLocation()).Format("2006-01-02") {
		t.Fatalf("cursor 应推到今天: %q", cursor)
	}

	byDate := map[string]*model.Event{}
	for _, e := range events {
		if e.Source != "immich" || e.EventType != model.EventPhoto || e.TimePrecision != model.PrecisionDay {
			t.Fatalf("事件标识异常: %s/%s/%s", e.Source, e.EventType, e.TimePrecision)
		}
		if e.ExternalID != e.LocalDate {
			t.Fatalf("ExternalID 应为日期: %s != %s", e.ExternalID, e.LocalDate)
		}
		byDate[e.LocalDate] = e
	}

	e := byDate["2026-10-15"]
	if e == nil {
		t.Fatal("缺少 2026-10-15 聚合事件")
	}
	if e.Title != "照片 2 张 · 视频 1 个" {
		t.Fatalf("标题异常: %q", e.Title)
	}
	// 摘要：主力相机 · 时段 · 收藏。
	if e.Summary != "Apple iPhone 15 Pro · 12:02–18:33 · ❤ 2" {
		t.Fatalf("摘要异常: %q", e.Summary)
	}
	// 媒体按拍摄时刻升序；视频 kind=video。
	if len(e.Media) != 3 {
		t.Fatalf("媒体数量异常: %d", len(e.Media))
	}
	if e.Media[0].ID != "aaaaaaaa-0000-4000-8000-000000000001" || e.Media[1].Kind != "video" {
		t.Fatalf("媒体顺序/类型异常: %+v", e.Media)
	}
	// 坐标取当天第一张带 GPS 的照片（WGS-84 直通）。
	if e.Latitude != 31.2323 || e.Longitude != 121.4692 {
		t.Fatalf("坐标异常: (%v,%v)", e.Latitude, e.Longitude)
	}
	if e.LocationName != "上海市" {
		t.Fatalf("地点名异常: %q", e.LocationName)
	}
	// detail 汇总字段。
	if e.Detail["count"].(int) != 3 || e.Detail["images"].(int) != 2 || e.Detail["videos"].(int) != 1 || e.Detail["favorites"].(int) != 2 {
		t.Fatalf("detail 计数异常: %v", e.Detail)
	}
	if e.Detail["first_time"].(string) != "12:02" || e.Detail["last_time"].(string) != "18:33" {
		t.Fatalf("时段异常: %v", e.Detail)
	}
	cams := e.Detail["cameras"].([]string)
	if len(cams) != 1 || cams[0] != "Apple iPhone 15 Pro" {
		t.Fatalf("相机列表异常: %v", cams)
	}

	// 请求体：takenAfter/takenBefore 为当天本地边界（RFC3339 带时区），
	// withExif 必须为 true（否则 exifInfo 不下发）。
	var checked bool
	for _, b := range *bodies {
		ta, _ := b["takenAfter"].(string)
		tb, _ := b["takenBefore"].(string)
		if strings.HasPrefix(ta, "2026-10-15T00:00:00") {
			checked = true
			if !strings.HasPrefix(tb, "2026-10-16T00:00:00") {
				t.Fatalf("takenBefore 非次日零点: %q", tb)
			}
			if b["withExif"] != true {
				t.Fatalf("withExif 必须为 true: %v", b["withExif"])
			}
			if _, ok := b["size"].(float64); !ok {
				t.Fatalf("缺少 size: %v", b)
			}
		}
	}
	if !checked {
		t.Fatalf("未见 10-15 的查询: %v", *bodies)
	}
}

func TestImmichSyncPagination(t *testing.T) {
	uuid := func(i int) string { return fmt.Sprintf("bbbbbbbb-0000-4000-8000-%012d", i) }
	page2Calls := 0
	srv, _ := immichFixture(t, "im-key", func(dayStart, dayEnd string, page int) ([]map[string]any, *string) {
		if !strings.HasPrefix(dayStart, "2026-10-15") {
			return nil, nil
		}
		if page == 1 {
			// 满页（500 条）+ nextPage → 必须继续翻页。
			items := make([]map[string]any, 0, immichPageSize)
			for i := 0; i < immichPageSize; i++ {
				items = append(items, mkAsset(uuid(i), "IMAGE", "2026-10-15T04:00:00.000Z", false, nil))
			}
			np := "2"
			return items, &np
		}
		page2Calls++
		return []map[string]any{mkAsset(uuid(999), "IMAGE", "2026-10-15T05:00:00.000Z", false, nil)}, nil
	})

	src := NewImmichSource(config.SourceConfig{BaseURL: srv.URL, APIKey: "im-key", LookbackDays: 30})
	var events []*model.Event
	if _, _, err := src.Sync(context.Background(), "", func(ev *model.Event) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if page2Calls != 1 {
		t.Fatalf("满页后应翻页，page2 调用 %d 次", page2Calls)
	}
	if len(events) != 1 || len(events[0].Media) != immichPageSize+1 {
		t.Fatalf("翻页聚合异常: events=%d media=%d", len(events), len(events[0].Media))
	}
}

func TestImmichUnconfigured(t *testing.T) {
	src := NewImmichSource(config.SourceConfig{})
	if _, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
		t.Fatal("期望未配置错误")
	}
	src2 := NewImmichSource(config.SourceConfig{BaseURL: "http://127.0.0.1:1"})
	if _, _, err := src2.Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
		t.Fatal("期望缺 key 错误")
	}
}

// ---------- 工具函数 ----------

func TestHTMLToTextAndFirstLine(t *testing.T) {
	html := "<p>第一行</p><p>第二行<br>第二行后半</p><div>&nbsp;</div><ul><li>列表项</li></ul>"
	got := htmlToText(html)
	// &nbsp; 规范化为空格（保留为一个空格而非删光）。
	want := "第一行\n第二行\n第二行后半\n \n列表项\n"
	if got != want {
		t.Fatalf("htmlToText = %q, want %q", got, want)
	}
	if l := firstLine("\n\n  截断测试行\n第二行", 4); l != "截断测试…" {
		t.Fatalf("firstLine 截断异常: %q", l)
	}
	if l := firstLine("  \n  ", 10); l != "" {
		t.Fatalf("firstLine 空文本异常: %q", l)
	}
}

func TestCameraNameAndTopKeys(t *testing.T) {
	mk, md := "Apple", "iPhone 15 Pro"
	// iPhone 机型名不含厂牌前缀 → 正常组合（不误去重）。
	if got := cameraName(&mk, &md); got != "Apple iPhone 15 Pro" {
		t.Fatalf("组合异常: %q", got)
	}
	mk2, md2 := "Apple", "Apple Watch Ultra"
	if got := cameraName(&mk2, &md2); got != "Apple Watch Ultra" {
		t.Fatalf("前缀重复未去重: %q", got)
	}
	mk3 := "SONY"
	if got := cameraName(&mk3, &md); got != "SONY iPhone 15 Pro" {
		t.Fatalf("跨厂牌组合异常: %q", got)
	}
	if got := cameraName(nil, nil); got != "" {
		t.Fatalf("空相机异常: %q", got)
	}
	// topKeys：计数降序，同数键名升序（确定性）。
	m := map[string]int{"a": 2, "b": 3, "c": 3, "d": 1}
	got := topKeys(m, 3)
	if len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "a" {
		t.Fatalf("topKeys 异常: %v", got)
	}
	if got := topKeys(map[string]int{}, 1); len(got) != 1 || got[0] != "" {
		t.Fatalf("空 map 异常: %v", got)
	}
}

func TestMoodEmoji(t *testing.T) {
	cases := map[int]string{0: "", 1: "😞", 3: "😐", 5: "😄"}
	for mood, want := range cases {
		if got := moodEmoji(mood); got != want {
			t.Errorf("moodEmoji(%d) = %q, want %q", mood, got, want)
		}
	}
}

func TestIsJWT(t *testing.T) {
	if !isJWT("eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InUxIn0.sig") {
		t.Error("合法 JWT 应识别")
	}
	if isJWT("yufei:secret") || isJWT("not-a-jwt") || isJWT("eyJ.only.two") {
		t.Error("非 JWT 不应误判")
	}
}

func TestIsCredentialPair(t *testing.T) {
	// 只有「两段都非空的 user:pass」才走登录；JWT / 长效 token 一律直传。
	if !isCredentialPair("yufei:secret") {
		t.Error("user:pass 应识别为凭证对")
	}
	for _, s := range []string{
		"eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InUxIn0.sig", // JWT 不含冒号
		"9ce83309255878e56aef3100b9c90bec",         // 长效 API token
		"yufei:", ":secret", ":", "",
	} {
		if isCredentialPair(s) {
			t.Errorf("%q 不应判定为凭证对", s)
		}
	}
}

// TestDiarumTokenPassthrough 非凭证形态一律直传 Bearer；401 给可操作提示。
func TestDiarumTokenPassthrough(t *testing.T) {
	const rawToken = "plain-long-lived-token"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			t.Error("直传形态不应触发登录")
		}
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer srv.Close()

	src := NewDiarumSource(config.SourceConfig{BaseURL: srv.URL, APIKey: rawToken, LookbackDays: 30})
	_, _, err := src.Sync(context.Background(), "", func(*model.Event) error { return nil })
	if err == nil {
		t.Fatal("期望 401 错误")
	}
	if gotAuth != "Bearer "+rawToken {
		t.Fatalf("应直传原 token，实际 %q", gotAuth)
	}
	if !strings.Contains(err.Error(), "鉴权失败") {
		t.Fatalf("401 错误信息不可操作: %v", err)
	}
}
