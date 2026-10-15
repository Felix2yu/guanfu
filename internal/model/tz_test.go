package model

import (
	"testing"
	"time"
)

func TestSetTimezoneAndLocalTime(t *testing.T) {
	SetTimezone("Asia/Shanghai")
	if got := LocalLocation().String(); got != "Asia/Shanghai" {
		t.Fatalf("时区异常: %s", got)
	}
	// 2026-10-15T04:30:00Z 的本地时刻应为 12:30（+08:00）。
	e := &Event{OccurredAt: time.Date(2026, 10, 15, 4, 30, 0, 0, time.UTC).Unix()}
	e.FillLocalTime()
	if e.LocalTime != "12:30" {
		t.Fatalf("LocalTime 异常: %q", e.LocalTime)
	}
	// 跨 UTC 午夜：23:30Z 应为次日 07:30 本地时刻（归日由适配器负责）。
	e2 := &Event{OccurredAt: time.Date(2026, 10, 15, 23, 30, 0, 0, time.UTC).Unix()}
	e2.FillLocalTime()
	if e2.LocalTime != "07:30" {
		t.Fatalf("跨日 LocalTime 异常: %q", e2.LocalTime)
	}
}
