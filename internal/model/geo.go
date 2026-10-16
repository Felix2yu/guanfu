package model

import "math"

// 中国区坐标偏移修正（GCJ-02 ↔ WGS-84）。
//
// mujian 场馆坐标以 GCJ-02 存储（其 geo 包注释：stores venue coordinates
// exactly as pasted from Chinese map apps (GCJ-02)，仅在 ICS 输出边界转
// WGS-84）。观复统一 WGS-84，故在 mujian 适配器内转换后再入库。
//
// 算法与 mujian backend/internal/geo/geo.go 逐行等价（含定点迭代逆解与
// OutOfChina 守卫），保证观复与 mujian ICS 导出的 WGS-84 结果一致；
// 等价性由 differential 差分测试验证（geo_test.go TestMatchesMujianReference）。

const (
	geoA  = 6378245.0              // Krasovsky 1940 长半轴
	geoEE = 0.00669342162296594323 // 偏心率平方
)

// OutOfChina 报告坐标是否在 GCJ-02 混淆区（中国大陆）之外。
// 区域外（如海外，Apple Maps 原生 WGS-84）不做任何变换。
func OutOfChina(lat, lng float64) bool {
	return !(lng > 73.66 && lng < 135.05 && lat > 3.86 && lat < 53.55)
}

func geoTransformLat(x, y float64) float64 {
	ret := -100.0 + 2.0*x + 3.0*y + 0.2*y*y + 0.1*x*y + 0.2*math.Sqrt(math.Abs(x))
	ret += (20.0*math.Sin(6.0*x*math.Pi) + 20.0*math.Sin(2.0*x*math.Pi)) * 2.0 / 3.0
	ret += (20.0*math.Sin(y*math.Pi) + 40.0*math.Sin(y/3.0*math.Pi)) * 2.0 / 3.0
	ret += (160.0*math.Sin(y/12.0*math.Pi) + 320*math.Sin(y*math.Pi/30.0)) * 2.0 / 3.0
	return ret
}

func geoTransformLng(x, y float64) float64 {
	ret := 300.0 + x + 2.0*y + 0.1*x*x + 0.1*x*y + 0.1*math.Sqrt(math.Abs(x))
	ret += (20.0*math.Sin(6.0*x*math.Pi) + 20.0*math.Sin(2.0*x*math.Pi)) * 2.0 / 3.0
	ret += (20.0*math.Sin(x*math.Pi) + 40.0*math.Sin(x/3.0*math.Pi)) * 2.0 / 3.0
	ret += (150.0*math.Sin(x/12.0*math.Pi) + 300.0*math.Sin(x/30.0*math.Pi)) * 2.0 / 3.0
	return ret
}

// WGS84ToGCJ02 把 WGS-84 坐标转为 GCJ-02。
func WGS84ToGCJ02(lat, lng float64) (float64, float64) {
	if OutOfChina(lat, lng) {
		return lat, lng
	}
	dLat := geoTransformLat(lng-105.0, lat-35.0)
	dLng := geoTransformLng(lng-105.0, lat-35.0)
	radLat := lat / 180.0 * math.Pi
	magic := math.Sin(radLat)
	magic = 1 - geoEE*magic*magic
	sqrtMagic := math.Sqrt(magic)
	dLat = (dLat * 180.0) / ((geoA * (1 - geoEE)) / (magic * sqrtMagic) * math.Pi)
	dLng = (dLng * 180.0) / (geoA / sqrtMagic * math.Cos(radLat) * math.Pi)
	return lat + dLat, lng + dLng
}

// GCJ02ToWGS84 把 GCJ-02（火星坐标）转为 WGS-84。逆解用两轮定点迭代，
// 残差远小于厘米级（mujian 同款实现）。
func GCJ02ToWGS84(lat, lng float64) (float64, float64) {
	if OutOfChina(lat, lng) {
		return lat, lng
	}
	wLat, wLng := lat, lng
	for i := 0; i < 2; i++ {
		gLat, gLng := WGS84ToGCJ02(wLat, wLng)
		wLat -= gLat - lat
		wLng -= gLng - lng
	}
	return wLat, wLng
}
