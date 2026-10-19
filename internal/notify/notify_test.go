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

func TestUnraidSendShape(t *testing.T) {
	var gotPath, gotKey, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-API-Key")
		gotBody = readBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"createNotification":{"id":"n1"}}}`))
	}))
	defer srv.Close()

	ch := newUnraid(config.NotifyChannel{
		Name: config.NotifyUnraid, BaseURL: srv.URL, Token: "u-key",
	}, srv.Client())

	res, err := ch.Send(context.Background(), Message{
		Title: "同步失败", Body: "huozhi 401", Priority: PriorityAlert,
	})
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !res.OK || res.Status != 200 {
		t.Fatalf("结果不符: %+v", res)
	}
	if gotPath != "/graphql" {
		t.Fatalf("路径应为 /graphql，得到 %q", gotPath)
	}
	if gotKey != "u-key" {
		t.Fatalf("X-API-Key 未带上: %q", gotKey)
	}
	for _, want := range []string{"createNotification", "同步失败", "huozhi 401", "ALERT"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("请求体缺少 %q: %s", want, gotBody)
		}
	}
}

func TestUnraidEndpointWithGraphqlSuffix(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	ch := newUnraid(config.NotifyChannel{Name: config.NotifyUnraid, BaseURL: srv.URL + "/graphql/"}, srv.Client())
	if _, err := ch.Send(context.Background(), Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if path != "/graphql" {
		t.Fatalf("重复拼 /graphql: %q", path)
	}
}

func TestUnraidGraphQLErrorIsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HTTP 200 + errors：最容易误判成成功的一种。
		_, _ = w.Write([]byte(`{"errors":[{"message":"Not authorized"}]}`))
	}))
	defer srv.Close()

	ch := newUnraid(config.NotifyChannel{Name: config.NotifyUnraid, BaseURL: srv.URL}, srv.Client())
	res, err := ch.Send(context.Background(), Message{Title: "t"})
	if err == nil {
		t.Fatal("GraphQL errors 必须报错")
	}
	if res.OK {
		t.Fatal("结果不能标记成功")
	}
	if !strings.Contains(res.Error, "Not authorized") {
		t.Fatalf("错误信息应带上游文案: %q", res.Error)
	}
}

func TestUnraidAuthModes(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.Header.Get("X-API-Key") != "" {
			t.Errorf("bearer 模式不该再发 X-API-Key")
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	ch := newUnraid(config.NotifyChannel{
		Name: config.NotifyUnraid, BaseURL: srv.URL, Auth: config.AuthBearer, Token: "tok",
	}, srv.Client())
	if _, err := ch.Send(context.Background(), Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" {
		t.Fatalf("bearer 头不符: %q", auth)
	}
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
	var unraidHits, appriseHits int32
	unraid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&unraidHits, 1)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer unraid.Close()
	apprise := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&appriseHits, 1)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer apprise.Close()

	cfg := config.NotifyConfig{Channels: map[string]config.NotifyChannel{
		config.NotifyUnraid:  {Name: config.NotifyUnraid, BaseURL: unraid.URL, Enabled: true},
		config.NotifyApprise: {Name: config.NotifyApprise, BaseURL: apprise.URL, Enabled: false, Key: "k"},
	}}
	d := NewDispatcher(func() config.NotifyConfig { return cfg })

	results, err := d.Send(context.Background(), Message{Title: "t"}, "")
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if len(results) != 1 || results[0].Channel != config.NotifyUnraid {
		t.Fatalf("只应发给启用的渠道: %+v", results)
	}
	if unraidHits != 1 || appriseHits != 0 {
		t.Fatalf("命中次数不符 unraid=%d apprise=%d", unraidHits, appriseHits)
	}
}

func TestDispatcherDedupeWindow(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	cfg := config.NotifyConfig{Channels: map[string]config.NotifyChannel{
		config.NotifyUnraid: {Name: config.NotifyUnraid, BaseURL: srv.URL, Enabled: true},
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
		config.NotifyUnraid: {Name: config.NotifyUnraid}, // 没地址
	}}
	d := NewDispatcher(func() config.NotifyConfig { return cfg })
	if _, err := d.Test(context.Background(), config.NotifyUnraid); err == nil {
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
		config.NotifyUnraid: {Name: config.NotifyUnraid, BaseURL: bad.URL, Enabled: true},
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
