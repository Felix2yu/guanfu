package model

import (
	"math"
	"testing"
)

// 与 mujian backend/internal/geo 逐行等价的算法；属性测试策略与其保持一致
// （不臆造绝对值对，只验证往返/量级/区域外直通）。等价性曾用 mujian 实现
// 照搬的差分程序在 9 个样本点上逐位比对通过。

func TestGeoRoundTrip(t *testing.T) {
	pts := [][2]float64{
		{31.2304, 121.4737}, // 上海
		{39.9087, 116.3975}, // 北京
		{22.5429, 114.0596}, // 深圳
		{23.1291, 113.2644}, // 广州
		{30.2741, 120.1551}, // 杭州
	}
	for _, p := range pts {
		gLat, gLng := WGS84ToGCJ02(p[0], p[1])
		wLat, wLng := GCJ02ToWGS84(gLat, gLng)
		if math.Abs(wLat-p[0]) > 1e-5 || math.Abs(wLng-p[1]) > 1e-5 {
			t.Errorf("往返漂移过大 %v: got (%f,%f)", p, wLat, wLng)
		}
	}
}

func TestGeoOffsetMagnitude(t *testing.T) {
	// GCJ-02 偏移应在数十至数百米量级：非零、但不超过 2km。
	wLat, wLng := 31.2304, 121.4737
	gLat, gLng := WGS84ToGCJ02(wLat, wLng)
	dLatM := (gLat - wLat) * 111320
	dLngM := (gLng - wLng) * 111320 * math.Cos(wLat*math.Pi/180)
	if dLatM == 0 && dLngM == 0 {
		t.Error("中国境内 GCJ-02 偏移不应为零")
	}
	if math.Abs(dLatM) > 2000 || math.Abs(dLngM) > 2000 {
		t.Errorf("偏移量异常: (%f,%f) m", dLatM, dLngM)
	}
}

func TestGeoOverseasPassThrough(t *testing.T) {
	lat, lng := 35.6812, 139.7671 // 东京
	if !OutOfChina(lat, lng) {
		t.Fatal("东京应判定为区域外")
	}
	if wLat, wLng := GCJ02ToWGS84(lat, lng); wLat != lat || wLng != lng {
		t.Errorf("区域外应原样返回, got (%v,%v)", wLat, wLng)
	}
	if gLat, gLng := WGS84ToGCJ02(lat, lng); gLat != lat || gLng != lng {
		t.Errorf("区域外应原样返回, got (%v,%v)", gLat, gLng)
	}
}

func TestGeoZeroValue(t *testing.T) {
	// 零坐标（无坐标数据）在两个方向都不得被算出诡异偏移。
	if wLat, wLng := GCJ02ToWGS84(0, 0); wLat != 0 || wLng != 0 {
		t.Errorf("零值异常: (%v,%v)", wLat, wLng)
	}
	if gLat, gLng := WGS84ToGCJ02(0, 0); gLat != 0 || gLng != 0 {
		t.Errorf("零值异常: (%v,%v)", gLat, gLng)
	}
}
