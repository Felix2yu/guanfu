package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/config"
)

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAppriseWithKey(t *testing.T) {
	var path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, body = r.URL.Path, readBody(t, r)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	ch := newApprise(config.NotifyChannel{
		Name: config.NotifyApprise, BaseURL: srv.URL, Key: "family",
	}, srv.Client())
	if _, err := ch.Send(context.Background(), Message{Title: "t", Body: "b", Priority: PriorityWarning}); err != nil {
		t.Fatal(err)
	}
	if path != "/notify/family" {
		t.Fatalf("key 模式路径不符: %q", path)
	}
	if strings.Contains(body, "urls") {
		t.Fatalf("key 模式不该带 urls: %s", body)
	}
	if !strings.Contains(body, `"warning"`) {
		t.Fatalf("优先级映射错误: %s", body)
	}
}

func TestAppriseTargetsMode(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.Unmarshal([]byte(readBody(t, r)), &body)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	ch := newApprise(config.NotifyChannel{
		Name:    config.NotifyApprise,
		BaseURL: srv.URL,
		Targets: "tgram://bot/chat, mailto://u:p@example.com\njson://localhost",
	}, srv.Client())
	if _, err := ch.Send(context.Background(), Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	urls, _ := body["urls"].([]any)
	if len(urls) != 3 {
		t.Fatalf("应解析出 3 个目标，得到 %v", body["urls"])
	}
}

func TestAppriseWithoutTargetErrors(t *testing.T) {
	ch := newApprise(config.NotifyChannel{Name: config.NotifyApprise, BaseURL: "http://127.0.0.1:1"}, http.DefaultClient)
	_, err := ch.Send(context.Background(), Message{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "目标 URL") {
		t.Fatalf("无 key 无目标应报明确错误: %v", err)
	}
}

func TestAppriseHTTPSuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"no services"}`))
	}))
	defer srv.Close()

	ch := newApprise(config.NotifyChannel{Name: config.NotifyApprise, BaseURL: srv.URL, Key: "k"}, srv.Client())
	res, err := ch.Send(context.Background(), Message{Title: "t"})
	if err == nil || res.OK {
		t.Fatalf("success=false 必须按失败处理: %+v %v", res, err)
	}
	if !strings.Contains(res.Error, "no services") {
		t.Fatalf("错误文案应带上游信息: %q", res.Error)
	}
}

func TestDispatcherOnlyEnabledChannels(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	cfg := config.NotifyConfig{Channels: map[string]config.NotifyChannel{
		config.NotifyApprise: {Name: config.NotifyApprise, BaseURL: srv.URL, Enabled: false, Key: "k"},
	}}
	d := NewDispatcher(func() config.NotifyConfig { return cfg })

	if _, err := d.Send(context.Background(), Message{Title: "t"}, ""); err == nil {
		t.Fatal("没有启用的渠道应报错")
	}
	if hits != 0 {
		t.Fatalf("未启用的渠道不该发送，命中 %d 次", hits)
	}

	cfg.Channels[config.NotifyApprise] = config.NotifyChannel{
		Name: config.NotifyApprise, BaseURL: srv.URL, Enabled: true, Key: "k",
	}
	results, err := d.Send(context.Background(), Message{Title: "t"}, "")
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if len(results) != 1 || results[0].Channel != config.NotifyApprise {
		t.Fatalf("应只发给启用的渠道: %+v", results)
	}
	if hits != 1 {
		t.Fatalf("命中次数不符: %d", hits)
	}
}

func TestDispatcherDedupeWindow(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	cfg := config.NotifyConfig{Channels: map[string]config.NotifyChannel{
		config.NotifyApprise: {Name: config.NotifyApprise, BaseURL: srv.URL, Enabled: true, Key: "k"},
	}}
	d := NewDispatcher(func() config.NotifyConfig { return cfg })
	d.SetWindow(time.Hour)

	if _, err := d.Send(context.Background(), Message{Title: "t"}, "huozhi"); err != nil {
		t.Fatal(err)
	}
	// 同键在窗口内：静默跳过（返回 nil, nil，不当作错误）。
	results, err := d.Send(context.Background(), Message{Title: "t"}, "huozhi")
	if err != nil || results != nil {
		t.Fatalf("窗口内应跳过: %v %v", results, err)
	}
	if hits != 1 {
		t.Fatalf("窗口内重复发送了: %d", hits)
	}
	// 不同键不受影响。
	if _, err := d.Send(context.Background(), Message{Title: "t"}, "immich"); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Fatalf("不同键应各自发送: %d", hits)
	}
	// 无去重键不过滤。
	if _, err := d.Send(context.Background(), Message{Title: "t"}, ""); err != nil {
		t.Fatal(err)
	}
	if hits != 3 {
		t.Fatalf("空键不该去重: %d", hits)
	}
}

func TestDispatcherTestUnconfigured(t *testing.T) {
	cfg := config.NotifyConfig{Channels: map[string]config.NotifyChannel{
		config.NotifyApprise: {Name: config.NotifyApprise}, // 没地址
	}}
	d := NewDispatcher(func() config.NotifyConfig { return cfg })
	if _, err := d.Test(context.Background(), config.NotifyApprise); err == nil {
		t.Fatal("未配置地址时测试发送应报错")
	}
	// 未知渠道也要明确报错，而不是 panic。
	if _, err := d.Test(context.Background(), "Nope"); err == nil {
		t.Fatal("未知渠道应报错")
	}
}

func TestDispatcherAggregatesErrors(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer bad.Close()

	cfg := config.NotifyConfig{Channels: map[string]config.NotifyChannel{
		config.NotifyApprise: {Name: config.NotifyApprise, BaseURL: bad.URL, Enabled: true, Key: "k"},
	}}
	d := NewDispatcher(func() config.NotifyConfig { return cfg })
	results, err := d.Send(context.Background(), Message{Title: "t"}, "")
	if err == nil {
		t.Fatal("失败必须冒泡")
	}
	if len(results) != 1 || results[0].Status != http.StatusUnauthorized {
		t.Fatalf("结果应保留上游状态码: %+v", results)
	}
	if !strings.Contains(results[0].Response, "bad key") {
		t.Fatalf("结果应保留上游响应体: %+v", results[0])
	}
}

func TestSplitTargets(t *testing.T) {
	got := splitTargets(" a://x , b://y\nc://z   ")
	want := []string{"a://x", "b://y", "c://z"}
	if len(got) != len(want) {
		t.Fatalf("切分错误: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项: %q != %q", i, got[i], want[i])
		}
	}
}
