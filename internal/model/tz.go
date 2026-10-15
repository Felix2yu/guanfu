package model

import (
	"sync"
	"time"
)

var (
	locMu   sync.RWMutex
	loc     *time.Location
	locOnce sync.Once
)

// SetTimezone 设置本地时区名（由 main 在启动时用 GUANFU_TZ 调用一次）。
// 必须在任何事件入库前调用；重复调用安全。
func SetTimezone(name string) {
	if name == "" {
		name = "Asia/Shanghai"
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		l = time.FixedZone(name, 8*3600)
	}
	locMu.Lock()
	loc = l
	locMu.Unlock()
}

// LocalLocation 返回观复本地时区。未显式设置时默认 Asia/Shanghai。
func LocalLocation() *time.Location {
	locOnce.Do(func() { SetTimezone("Asia/Shanghai") })
	locMu.RLock()
	defer locMu.RUnlock()
	if loc == nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}
