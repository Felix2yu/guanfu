package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/model"
)

// qingye 契约（2026-10-16 对该实例实测）：
//   - /api/diaries → {code,message,data:{list,page,pageSize,total}}，按 takenAt 倒序
//   - /api/care-logs → {code,message,data:[...]}（裸数组，没有分页也没有 total）
//   - 没有任何日期过滤参数；时间是 RFC3339 带纳秒 + Z
//   - 图片是同源绝对路径 /uploads/<file>
// 下面这些 fixture 严格照真实形态写，不照"理想 API 应该长什么样"写。

func qyPlantJSON(id int64, name, location string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "species": "Epipremnum aureum", "photo": "",
		"roomId": 1, "room": map[string]any{"id": 1, "name": "客厅", "sort": 1, "isOutdoor": false, "icon": ""},
		"note": "浇水：土壤干透后浇水", "location": location,
		"acquiredDate": nil, "lightReq": "", "attributes": "",
		"createdAt": "2026-08-26T10:24:45.989547237Z", "updatedAt": "2026-08-26T10:24:45.989547237Z",
	}
}

func qyDiaryJSON(id int64, plantID int64, plantName, takenAt, image, caption string) map[string]any {
	return map[string]any{
		"id": id, "plantId": plantID, "plant": qyPlantJSON(plantID, plantName, ""),
		"image": image, "caption": caption, "takenAt": takenAt,
		"createdAt": "2026-08-19T10:24:45.989547237Z",
	}
}

// qingyeStub 按请求路径回真实外壳；pages 记录被请求过的 page 值。
func qingyeStub(t *testing.T, pages *[]string, diaries []map[string]any, total int, secondPage []map[string]any, care []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/diaries":
			page := r.URL.Query().Get("page")
			if pages != nil {
				*pages = append(*pages, page)
			}
			list := diaries
			if page == "2" {
				list = secondPage
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{"list": list, "page": mustAtoi(page), "pageSize": mustAtoi(r.URL.Query().Get("pageSize")), "total": total},
			})
		case "/api/care-logs":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "ok", "data": care})
		default:
			// 真实上游对未知路径回 {"error":"not found"}，不是 {code,...}。
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
		}
	}))
}

func mustAtoi(s string) int { n, _ := strconv.Atoi(s); return n }

func newQingye(base string) *QingyeSource {
	return NewQingyeSource(config.SourceConfig{BaseURL: base, LookbackDays: 365})
}

// utc 把 UTC 时刻格式化成上游那种带纳秒的 RFC3339。
func utc(y int, mo time.Month, d, hh, mm, ss int) string {
	return time.Date(y, mo, d, hh, mm, ss, 123456789, time.UTC).Format("2006-01-02T15:04:05.999999999Z07:00")
}

func TestQingyeFirstRound(t *testing.T) {
	stub := qingyeStub(t, nil,
		[]map[string]any{
			// 跨日样本：UTC 10-15 20:30 = 本地 10-16 04:30，归日必须是 10-16。
			qyDiaryJSON(3, 2, "龟背竹", utc(2026, 10, 15, 20, 30, 0), "/uploads/1792120109510945390_DDen7iOg.jpg", "换盆后长出了新叶片"),
			qyDiaryJSON(2, 2, "龟背竹", utc(2026, 8, 23, 10, 24, 45), "", "无图日记"),
		}, 2, nil,
		[]map[string]any{
			{"id": 1, "plantId": 5, "plant": qyPlantJSON(5, "紫薇", "阳台"),
				"type": "water", "title": "浇水", "at": utc(2026, 10, 16, 3, 8, 50),
				"note": "土壤干透", "source": "task", "taskId": 6},
		})
	defer stub.Close()

	up, got := collect()
	cursor, count, err := newQingye(stub.URL).Sync(context.Background(), "", up)
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if count != 3 {
		t.Fatalf("期望 3 条，实际 %d", count)
	}
	if cursor == "" {
		t.Fatal("首轮应推进水位")
	}

	var diaryWithImage, diaryNoImage, care *model.Event
	for _, ev := range *got {
		switch ev.ExternalID {
		case "dia:3":
			diaryWithImage = ev
		case "dia:2":
			diaryNoImage = ev
		case "care:1":
			care = ev
		}
	}
	if diaryWithImage == nil || diaryNoImage == nil || care == nil {
		t.Fatalf("external_id 前缀或条目缺失: %+v", *got)
	}

	// 日记：day 精度 + 跨日归日正确。
	if diaryWithImage.EventType != model.EventPlantDiary || diaryWithImage.TimePrecision != model.PrecisionDay {
		t.Fatalf("日记应为 plant_diary/day: %s/%s", diaryWithImage.EventType, diaryWithImage.TimePrecision)
	}
	if diaryWithImage.LocalDate != "2026-10-16" {
		t.Fatalf("UTC 10-15T20:30 应归本地 10-16，实际 %s", diaryWithImage.LocalDate)
	}
	if diaryWithImage.Title != "龟背竹" {
		t.Fatalf("标题应是植物名（同株记录成组），实际 %q", diaryWithImage.Title)
	}
	if diaryWithImage.Summary != "换盆后长出了新叶片" {
		t.Fatalf("正文应进 summary，实际 %q", diaryWithImage.Summary)
	}
	// 图片：剥掉 /uploads/ 前缀，只留可代理的文件名。
	if len(diaryWithImage.Media) != 1 || diaryWithImage.Media[0].ID != "1792120109510945390_DDen7iOg.jpg" {
		t.Fatalf("媒体 key 有误: %+v", diaryWithImage.Media)
	}
	if diaryWithImage.Detail["image"] != "/uploads/1792120109510945390_DDen7iOg.jpg" {
		t.Fatalf("detail 应保留原始路径: %+v", diaryWithImage.Detail)
	}

	// 无图日记不得留下空媒体占位（前端整行不渲染）。
	if len(diaryNoImage.Media) != 0 {
		t.Fatalf("无图日记不该有 media: %+v", diaryNoImage.Media)
	}

	// 养护：second 精度，纳秒时间可解析，摘要 = 动作 · 备注。
	if care.EventType != model.EventPlantCare || care.TimePrecision != model.PrecisionSecond {
		t.Fatalf("养护应为 plant_care/second: %s/%s", care.EventType, care.TimePrecision)
	}
	if care.Summary != "浇水 · 土壤干透" {
		t.Fatalf("养护摘要有误: %q", care.Summary)
	}
	if care.LocationName != "阳台" {
		t.Fatalf("养护地点应取 plant.location: %q", care.LocationName)
	}
	if care.Detail["source"] != "task" || care.Detail["task_id"] != int64(6) {
		t.Fatalf("养护来源字段未保留: %+v", care.Detail)
	}
	// 养护不带图，卡片不应渲染照片格。
	if len(care.Media) != 0 {
		t.Fatalf("养护不该有 media: %+v", care.Media)
	}
}

func TestQingyeCursorEarlyStop(t *testing.T) {
	// 水位 = 10-14 12:00 UTC，重叠窗口 7 天 → cutoff = 10-07 12:00 UTC。
	watermark := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	var pages []string
	stub := qingyeStub(t, &pages,
		[]map[string]any{
			qyDiaryJSON(10, 1, "绿萝", utc(2026, 10, 15, 1, 0, 0), "", "比水位新"), // 收
			qyDiaryJSON(9, 1, "绿萝", utc(2026, 10, 10, 1, 0, 0), "", "重叠窗口内"), // 收（补录容忍）
			qyDiaryJSON(8, 1, "绿萝", utc(2026, 10, 1, 1, 0, 0), "", "比水位旧"),   // 停，且不再翻页
		}, 999, []map[string]any{qyDiaryJSON(7, 1, "绿萝", utc(2026, 10, 20, 1, 0, 0), "", "不该被请求")}, nil)
	defer stub.Close()

	up, _ := collect()
	cursor, count, err := newQingye(stub.URL).Sync(context.Background(), strconv.FormatInt(watermark.Unix(), 10), up)
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("重叠窗口外的条目应被跳过，实际写入 %d 条", count)
	}
	if strings.Join(pages, ",") != "1" {
		t.Fatalf("倒序遇到旧条目就该收工，不该再翻页，实际请求页: %v", pages)
	}
	// 水位只前进不后退。
	newSec, _ := strconv.ParseInt(cursor, 10, 64)
	if newSec != time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("水位应推进到本轮最新一条 %v，实际 %s", time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC).Unix(), cursor)
	}
}

func TestQingyePaginatesUntilTotal(t *testing.T) {
	// 第一页满 100 条且 total=120 → 必须再取第二页。时间按小时倒推，全部落在水位之外。
	var pages []string
	fill := func(from, n int) []map[string]any {
		base := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
		out := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			idx := from + i
			out = append(out, qyDiaryJSON(int64(idx), 1, "绿萝",
				base.Add(-time.Duration(idx)*time.Hour).Format("2006-01-02T15:04:05.999999999Z07:00"),
				"", "d"))
		}
		return out
	}
	stub := qingyeStub(t, &pages, fill(1, 100), 120, fill(101, 20), nil)
	defer stub.Close()

	up, _ := collect()
	if _, count, err := newQingye(stub.URL).Sync(context.Background(), "", up); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	} else if count != 120 {
		t.Fatalf("应翻完两页共 120 条，实际 %d（请求页 %v）", count, pages)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("翻页序列有误: %v", pages)
	}
}

func TestQingyeNoNewKeepsCursor(t *testing.T) {
	stub := qingyeStub(t, nil, []map[string]any{
		qyDiaryJSON(1, 1, "绿萝", utc(2020, 1, 1, 0, 0, 0), "", "很久以前"),
	}, 1, nil, nil)
	defer stub.Close()

	up, _ := collect()
	old := strconv.FormatInt(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC).Unix(), 10)
	cursor, count, err := newQingye(stub.URL).Sync(context.Background(), old, up)
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if count != 0 || cursor != old {
		t.Fatalf("无新条目时游标不得倒退（会把下轮变成全量回溯）: cursor=%s count=%d", cursor, count)
	}
}

func TestQingyeBusinessCodeAndBadBody(t *testing.T) {
	// 业务码非 0：HTTP 仍是 200，只看状态码会静默当空数据。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": "internal", "data": nil})
	}))
	defer bad.Close()
	if _, _, err := newQingye(bad.URL).Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
		t.Fatal("业务码非 0 应报错，不能当空结果吞掉")
	}

	// 未挂前端产物时未知路径回 HTML：解析必须失败而不是静默 0 条。
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html>"))
	}))
	defer html.Close()
	if _, _, err := newQingye(html.URL).Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
		t.Fatal("HTML 响应应报错")
	}

	// 没配 BaseURL 要显式失败（不静默跳过整源）。
	if _, _, err := newQingye("").Sync(context.Background(), "", func(*model.Event) error { return nil }); err == nil {
		t.Fatal("缺 QINGYE_BASE_URL 应报错")
	}
}

func TestQingyeImageKeyForms(t *testing.T) {
	cases := map[string]string{
		"/uploads/a_b.jpg":                         "a_b.jpg", // 实测形态：同源绝对路径
		"https://qingye.example.com/uploads/c.jpg": "c.jpg",   // 完整 URL 也能剥
		"d.jpg":     "d.jpg", // 只给文件名
		"":          "",
		"   ":       "",
		"/":         "",
		"/uploads/": "",
	}
	for in, want := range cases {
		if got := qingyeImageKey(in); got != want {
			t.Fatalf("qingyeImageKey(%q) = %q，期望 %q", in, got, want)
		}
	}
}
