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
	"sync"
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
//
// 适配器是「配置快照」构造出来的（各源把 BaseURL/Key 拿在手里），
// 所以页面改完配置后必须换一批适配器——Reconfigure 就是干这个的：
// 取消旧的一轮调度 goroutine，用新注册表重启，进程不重启。
type Engine struct {
	store   *store.Store
	trigger chan string // 手动触发（源名或 "*" 表示全部）
	done    chan struct{}

	mu        sync.RWMutex
	registry  *Registry
	enabled   EnabledFunc
	onFailure func(name string, err error)
	// parent 是 Start 收到的根 ctx；cancel 是当前这批调度 goroutine 的子 ctx。
	parent context.Context
	cancel context.CancelFunc

	// syncing 标记「这一源此刻有一轮同步在跑」，供 /api/v1/sources 下发给
	// 前端轮询（手动同步的完成判定靠它，不再盲等固定秒数）。
	syncMu  sync.Mutex
	syncing map[string]bool
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

// SetOnFailure 注册「某源由正常转为失败」的回调（通知渠道挂在上面）。
// 回调在独立 goroutine 里跑，不阻塞同步循环。
func (e *Engine) SetOnFailure(fn func(name string, err error)) {
	e.mu.Lock()
	e.onFailure = fn
	e.mu.Unlock()
}

// Start 启动引擎：立即同步一轮，随后按各源 Interval 定时同步。
// 启动时会为每个启用的源写入初始状态行（last_sync_at=0），
// 让前端源状态区在首次同步完成前即可见。
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	e.parent = ctx
	e.mu.Unlock()
	go e.runManual(ctx)
	e.respawn(true) // 进程启动这一轮：每个启用源都立即同步一次
}

// Reconfigure 用新的适配器注册表替换当前调度（配置改完后调用）。
// 未 Start 过时只记下新注册表，等 Start 时生效。
//
// 重建只换调度，不追加同步：所有启用源都被扫一轮的话，改一个源的配置
// 就等于全量同步（上游被打扰、页面看起来像"点了全局同步"）。
// 需要立刻同步被改动的那个源时，由调用方随后 Trigger(name)。
func (e *Engine) Reconfigure(reg *Registry) {
	e.mu.Lock()
	e.registry = reg
	started := e.parent != nil
	e.mu.Unlock()
	if started {
		e.respawn(false)
	}
}

// respawn 取消上一批调度 goroutine 并按当前注册表重开。
// syncNow=false 时不追加首轮同步（配置变更后重建调度用），新周期由各自的 ticker 触发。
func (e *Engine) respawn(syncNow bool) {
	e.mu.Lock()
	if e.parent == nil {
		e.mu.Unlock()
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	runCtx, cancel := context.WithCancel(e.parent)
	e.cancel = cancel
	reg := e.registry
	e.mu.Unlock()

	for _, a := range reg.All() {
		if !e.isEnabled(a.Name()) {
			continue
		}
		if cur, err := e.store.GetSourceCursor(a.Name()); err == nil && cur == "" {
			if _, err := e.store.GetSourceState(a.Name()); err != nil {
				_ = e.store.SaveSourceState(a.Name(), true, int(a.Interval().Seconds()), "", 0, "", 0)
			}
		}
		go e.runSource(runCtx, a, syncNow)
	}
}

// currentRegistry 取当前注册表快照（手动触发用它，避免读到半新半旧的集合）。
func (e *Engine) currentRegistry() *Registry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.registry
}

// isEnabled 判断某源当前是否参与调度。
func (e *Engine) isEnabled(name string) bool {
	e.mu.RLock()
	fn := e.enabled
	e.mu.RUnlock()
	if fn == nil {
		return false
	}
	return fn(name)
}

// TriggerResult 手动触发的受理结果。调用方据此回状态码——
// 「压根没受理」不能继续对外报「已触发同步」。
type TriggerResult int

const (
	TriggerQueued    TriggerResult = iota // 已排进触发队列
	TriggerUnknown                        // 注册表里没有这个源
	TriggerDisabled                       // 有这个名字，但当前不参与调度
	TriggerBusy                           // 队列已满，本次请求被丢弃
	TriggerNotStarted                     // 引擎没 Start 过（GUANFU_SYNC_ENABLED=0），触发队列没有消费者
)

// Trigger 请求手动同步某源（"*" 表示全部启用源）。非阻塞。
// 受理判定读当前注册表快照；若判定后到消费前发生了 Reconfigure，
// runManual 会按新注册表再解析一次，不会去同步已经不存在的源。
func (e *Engine) Trigger(name string) TriggerResult {
	// 引擎没启动（同步总开关关闭）时触发队列没有消费者，受理了也只是
	// 静默积压——必须在这里拒绝，让 API 层如实回 409 而不是假装已触发。
	e.mu.RLock()
	started := e.parent != nil
	e.mu.RUnlock()
	if !started {
		return TriggerNotStarted
	}
	reg := e.currentRegistry()
	if name == "*" {
		any := false
		for _, a := range reg.All() {
			if e.isEnabled(a.Name()) {
				any = true
				break
			}
		}
		if !any {
			return TriggerDisabled
		}
	} else if _, ok := reg.Get(name); !ok {
		return TriggerUnknown
	} else if !e.isEnabled(name) {
		return TriggerDisabled
	}
	select {
	case e.trigger <- name:
		return TriggerQueued
	default:
		return TriggerBusy
	}
}

// Stop 取消调度并等待 goroutine 退出（ctx 取消后由调用方控制）。
func (e *Engine) Stop() {
	e.mu.Lock()
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	e.mu.Unlock()
	close(e.done)
}

func (e *Engine) runManual(ctx context.Context) {
	defer e.clearSyncing() // 引擎停机时清掉标记，别让 API 读到僵尸状态
	for {
		select {
		case <-ctx.Done():
			return
		case name := <-e.trigger:
			reg := e.currentRegistry()
			adapters := reg.All()
			if name != "*" {
				adapters = nil
				if a, ok := reg.Get(name); ok {
					adapters = []Adapter{a}
				}
			}
			// "*" 是串行执行的：两源之间会出现「无人在跑」的瞬间，轮询方会
			// 误判整批已完成。先把整批都标记上，跑完一个清一个。
			if name == "*" {
				for _, a := range adapters {
					if e.isEnabled(a.Name()) {
						e.markSyncing(a.Name(), true)
					}
				}
			}
			for _, a := range adapters {
				if !e.isEnabled(a.Name()) {
					e.markSyncing(a.Name(), false)
					continue
				}
				e.syncOnce(ctx, a)
			}
		}
	}
}

// markSyncing 维护单源的「同步进行中」标记。
func (e *Engine) markSyncing(name string, on bool) {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	if e.syncing == nil {
		e.syncing = map[string]bool{}
	}
	if on {
		e.syncing[name] = true
	} else {
		delete(e.syncing, name)
	}
}

// Syncing 返回当前正在同步的源集合快照。
func (e *Engine) Syncing() map[string]bool {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	out := make(map[string]bool, len(e.syncing))
	for k := range e.syncing {
		out[k] = true
	}
	return out
}

func (e *Engine) clearSyncing() {
	e.syncMu.Lock()
	e.syncing = map[string]bool{}
	e.syncMu.Unlock()
}

func (e *Engine) runSource(ctx context.Context, a Adapter, syncNow bool) {
	interval := a.Interval()
	if interval <= 0 {
		// 无周期：仅在启动时跑一轮，之后只响应手动触发。
		if syncNow {
			e.syncOnce(ctx, a)
		}
		return
	}
	if syncNow {
		e.syncOnce(ctx, a)
	}
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
	e.markSyncing(a.Name(), true)
	defer e.markSyncing(a.Name(), false)
	cursor, err := e.store.GetSourceCursor(a.Name())
	if err != nil {
		e.recordError(a.Name(), err)
		return
	}
	count := 0
	newCursor, _, err := a.Sync(ctx, cursor, func(ev *model.Event) error {
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
	// last_count 存真实变更数（新增+内容有变化），不是适配器返回的处理总数——
	// 重复拉同一批数据时它为 0，前端「新增 N 条」才不会虚报。
	_ = e.store.SaveSourceState(a.Name(), e.isEnabled(a.Name()), intervalSecs, newCursor, time.Now().Unix(), "", count)
}

func (e *Engine) recordError(name string, err error) {
	// 先读后写，保留原 cursor，避免失败一次就触发全量回溯。
	cur, _ := e.store.GetSourceCursor(name)
	prev, prevErr := e.store.GetSourceState(name)
	// 同时保留上次成功同步的时间/条数与间隔：失败只该覆盖 last_error，
	// 抹掉「上次成功于何时」会让源状态页从此显示「从未同步」。
	lastSync, lastCount, interval := int64(0), 0, 0
	if prevErr == nil {
		lastSync, lastCount, interval = prev.LastSyncAt, prev.LastCount, prev.IntervalSecs
	}
	_ = e.store.SaveSourceState(name, e.isEnabled(name), interval, cur, lastSync, err.Error(), lastCount)

	// 只在「由正常转失败」时通知：持续失败不重复轰炸（Dispatcher 那层还有
	// 时间窗去重，这里先挡掉最常见的重复来源）。从没跑过的源不通知——
	// 首次启动就失败的告警，用户点开设置页就能看到。
	if prevErr != nil || prev.LastError != "" {
		return
	}
	e.mu.RLock()
	hook := e.onFailure
	e.mu.RUnlock()
	if hook != nil {
		go hook(name, err)
	}
}
