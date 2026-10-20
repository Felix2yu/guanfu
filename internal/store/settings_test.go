package store

import (
	"errors"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEndpointConfigRoundTrip(t *testing.T) {
	s := openTest(t)

	if _, err := s.GetEndpointConfig("huozhi"); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("空库应返回 ErrNoEndpoint，得到 %v", err)
	}

	in := EndpointConfig{
		Name: "huozhi", Kind: KindSource, BaseURL: "http://h:8080",
		Auth: "bearer", Secret: "enc:v1:xxx", Enabled: true, IntervalSecs: 1800,
	}
	if err := s.SaveEndpointConfig(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEndpointConfig("huozhi")
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != in.BaseURL || got.Auth != in.Auth || got.Secret != in.Secret ||
		!got.Enabled || got.IntervalSecs != in.IntervalSecs {
		t.Fatalf("往返不一致: %+v", got)
	}
	if got.UpdatedAt == 0 {
		t.Fatal("UpdatedAt 应由 Save 填上")
	}

	// 覆盖写：空值必须能覆盖（「页面留空 = 清空」的落库前提）。
	in.BaseURL = ""
	in.Secret = ""
	in.Auth = "none"
	in.Enabled = false
	if err := s.SaveEndpointConfig(in); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetEndpointConfig("huozhi")
	if got.BaseURL != "" || got.Secret != "" || got.Auth != "none" || got.Enabled {
		t.Fatalf("空值覆盖失败: %+v", got)
	}
}

func TestEndpointConfigListAndDelete(t *testing.T) {
	s := openTest(t)
	_ = s.SaveEndpointConfig(EndpointConfig{Name: "huozhi", Kind: KindSource, Enabled: true})
	_ = s.SaveEndpointConfig(EndpointConfig{Name: "immich", Kind: KindSource, Enabled: true})
	_ = s.SaveEndpointConfig(EndpointConfig{Name: "apprise", Kind: KindNotify, Enabled: true})

	all, err := s.ListEndpointConfigs("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("应列出 3 条，得到 %d", len(all))
	}
	// 排序稳定（按 name 升序），前端表格不必自己排。
	if all[0].Name != "apprise" || all[1].Name != "huozhi" || all[2].Name != "immich" {
		t.Fatalf("排序不符: %v", []string{all[0].Name, all[1].Name, all[2].Name})
	}

	src, _ := s.ListEndpointConfigs(KindSource)
	if len(src) != 2 {
		t.Fatalf("kind 过滤失效: %d", len(src))
	}
	ntf, _ := s.ListEndpointConfigs(KindNotify)
	if len(ntf) != 1 || ntf[0].Name != "apprise" {
		t.Fatalf("kind=notify 过滤失效: %+v", ntf)
	}

	ok, err := s.DeleteEndpointConfig("huozhi")
	if err != nil || !ok {
		t.Fatalf("删除失败: %v %v", ok, err)
	}
	if _, err := s.GetEndpointConfig("huozhi"); !errors.Is(err, ErrNoEndpoint) {
		t.Fatal("删除后应查不到")
	}
	ok, _ = s.DeleteEndpointConfig("huozhi")
	if ok {
		t.Fatal("重复删除应返回 false")
	}
}

func TestSettingRoundTrip(t *testing.T) {
	s := openTest(t)
	if v, err := s.GetSetting("notify.on_sync_failure"); err != nil || v != "" {
		t.Fatalf("缺省应为空: %q %v", v, err)
	}
	if err := s.SetSetting("notify.on_sync_failure", "1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting("notify.on_sync_failure"); v != "1" {
		t.Fatalf("读到 %q", v)
	}
	if err := s.SetSetting("notify.on_sync_failure", "0"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting("notify.on_sync_failure"); v != "0" {
		t.Fatalf("覆盖失败: %q", v)
	}
	if err := s.SetSetting("notify.on_sync_failure", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting("notify.on_sync_failure"); v != "" {
		t.Fatalf("空值应删除而非存空串: %q", v)
	}
}
