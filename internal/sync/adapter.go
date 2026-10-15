// Package sync 实现多源同步引擎：适配器接口 + 调度器。
//
// 适配器契约：
//   - Sync 内部自行翻页/分批，把事件逐条交给 Upsert（store 已保证幂等）；
//   - 返回的 cursor 是下次增量拉取的起点（如 "2026-10-15"），由引擎持久化；
//   - 单源失败只记录 last_error 并继续，不影响其他源；
//   - 同步过程中可通过 ctx 取消（优雅停机）。
package sync

import (
	"context"
	"time"

	"github.com/Felix2yu/guanfu/internal/model"
	"github.com/Felix2yu/guanfu/internal/store"
)

// Adapter 单个数据源适配器。
type Adapter interface {
	// Name 源名，与 config.Sources 的键一致（huozhi/mujian/...）。
	Name() string
	// Interval 默认同步间隔；返回 0 表示只跑一次（手动触发）。
	Interval() time.Duration
	// Sync 执行一轮同步。upsert 由引擎提供；cursor 为上次位点（首轮为空）。
	// 返回新 cursor 与本次写入条数。
	Sync(ctx context.Context, cursor string, upsert func(*model.Event) error) (newCursor string, count int, err error)
}

// Registry 源适配器注册表。
type Registry struct {
	adapters map[string]Adapter
	order    []string
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{adapters: map[string]Adapter{}}
}

// Register 注册一个适配器（重复注册后者覆盖）。
func (r *Registry) Register(a Adapter) {
	if _, ok := r.adapters[a.Name()]; !ok {
		r.order = append(r.order, a.Name())
	}
	r.adapters[a.Name()] = a
}

// Get 取适配器。
func (r *Registry) Get(name string) (Adapter, bool) {
	a, ok := r.adapters[name]
	return a, ok
}

// All 按注册顺序返回全部适配器。
func (r *Registry) All() []Adapter {
	out := make([]Adapter, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.adapters[n])
	}
	return out
}

// Enabled 判断某源是否启用（有 BaseURL+Key 且未显式关闭）。
type EnabledFunc func(name string) bool

// Engine 同步调度引擎。
type Engine struct {
	store    *store.Store
	registry *Registry
	enabled  EnabledFunc
	trigger  chan string // 手动触发（源名或 "*" 表示全部）
	done     chan struct{}
}

// NewEngine 创建引擎。enabled 决定哪些源参与调度（由 config 提供）。
func NewEngine(st *store.Store, reg *Registry, enabled EnabledFunc) *Engine {
	return &Engine{
		store:    st,
		registry: reg,
		enabled:  enabled,
		trigger:  make(chan string, 16),
		done:     make(chan struct{}),
	}
}

// Start 启动引擎：立即同步一轮，随后按各源 Interval 定时同步。
// 启动时会为每个启用的源写入初始状态行（last_sync_at=0），
// 让前端源状态区在首次同步完成前即可见。
func (e *Engine) Start(ctx context.Context) {
	for _, a := range e.registry.All() {
		if !e.enabled(a.Name()) {
			continue
		}
		if cur, err := e.store.GetSourceCursor(a.Name()); err == nil && cur == "" {
			if _, err := e.store.GetSourceState(a.Name()); err != nil {
				_ = e.store.SaveSourceState(a.Name(), true, int(a.Interval().Seconds()), "", 0, "", 0)
			}
		}
		go e.runSource(ctx, a)
	}
	go e.runManual(ctx)
}

// Trigger 请求手动同步某源（name="*" 表示全部启用源）。非阻塞。
func (e *Engine) Trigger(name string) {
	select {
	case e.trigger <- name:
	default:
	}
}

// Stop 等待所有 goroutine 退出（ctx 取消后由调用方控制）。
func (e *Engine) Stop() { close(e.done) }

func (e *Engine) runManual(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case name := <-e.trigger:
			adapters := e.registry.All()
			if name != "*" {
				adapters = nil
				if a, ok := e.registry.Get(name); ok {
					adapters = []Adapter{a}
				}
			}
			for _, a := range adapters {
				if !e.enabled(a.Name()) {
					continue
				}
				e.syncOnce(ctx, a)
			}
		}
	}
}

func (e *Engine) runSource(ctx context.Context, a Adapter) {
	interval := a.Interval()
	if interval <= 0 {
		// 无周期：仅在启动时跑一轮，之后只响应手动触发。
		e.syncOnce(ctx, a)
		return
	}
	e.syncOnce(ctx, a)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.syncOnce(ctx, a)
		}
	}
}

// syncOnce 执行一轮同步并落库状态。失败不 panic，只记录。
func (e *Engine) syncOnce(ctx context.Context, a Adapter) {
	cursor, err := e.store.GetSourceCursor(a.Name())
	if err != nil {
		e.recordError(a.Name(), err)
		return
	}
	count := 0
	newCursor, n, err := a.Sync(ctx, cursor, func(ev *model.Event) error {
		changed, err := e.store.UpsertEvent(ev)
		if err != nil {
			return err
		}
		if changed {
			count++
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return // 优雅停机中，不写错误状态
		}
		e.recordError(a.Name(), err)
		return
	}
	if newCursor == "" {
		newCursor = cursor
	}
	intervalSecs := int(a.Interval().Seconds())
	_ = e.store.SaveSourceState(a.Name(), e.enabled(a.Name()), intervalSecs, newCursor, time.Now().Unix(), "", n)
}

func (e *Engine) recordError(name string, err error) {
	// 先读后写，保留原 cursor，避免失败一次就触发全量回溯。
	cur, _ := e.store.GetSourceCursor(name)
	_ = e.store.SaveSourceState(name, e.enabled(name), 0, cur, 0, err.Error(), 0)
}
