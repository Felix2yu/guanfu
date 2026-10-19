package sync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Felix2yu/guanfu/internal/model"
	"github.com/Felix2yu/guanfu/internal/store"
)

// fakeAdapter 可控适配器：记录被调用次数，可选择阻塞到 ctx 取消。
type fakeAdapter struct {
	name     string
	interval time.Duration
	err      error
	block    bool
	calls    atomic.Int32
	started  chan struct{}
	stopped  chan struct{}
}

func newFake(name string) *fakeAdapter {
	return &fakeAdapter{
		name: name, interval: 10 * time.Millisecond,
		started: make(chan struct{}, 8), stopped: make(chan struct{}, 8),
	}
}

func (f *fakeAdapter) Name() string            { return f.name }
func (f *fakeAdapter) Interval() time.Duration { return f.interval }

func (f *fakeAdapter) Sync(ctx context.Context, _ string, _ func(*model.Event) error) (string, int, error) {
	f.calls.Add(1)
	select {
	case f.started <- struct{}{}:
	default:
	}
	if f.block {
		<-ctx.Done()
		f.stopped <- struct{}{}
		return "", 0, ctx.Err()
	}
	if f.err != nil {
		return "", 0, f.err
	}
	return "2026-01-01", 1, nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/e.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

func TestEngineStartsEnabledSources(t *testing.T) {
	st := openStore(t)
	a := newFake("a")
	reg := NewRegistry()
	reg.Register(a)

	e := NewEngine(st, reg, func(name string) bool { return name == "a" })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Stop()

	waitFor(t, "a 被同步一次", func() bool { return a.calls.Load() >= 1 })
}

func TestEngineReconfigureSwapsAdapters(t *testing.T) {
	st := openStore(t)
	old := newFake("a")
	old.block = true // 卡住，用来观察它是否被取消

	reg1 := NewRegistry()
	reg1.Register(old)

	e := NewEngine(st, reg1, func(string) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Stop()

	waitFor(t, "旧适配器进入 Sync", func() bool { return old.calls.Load() >= 1 })

	// 换成一个新适配器：旧的那批 goroutine 必须被取消，新的必须跑起来。
	fresh := newFake("b")
	reg2 := NewRegistry()
	reg2.Register(fresh)
	e.Reconfigure(reg2)

	select {
	case <-old.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("重建后旧调度没有被取消")
	}
	waitFor(t, "新适配器被同步", func() bool { return fresh.calls.Load() >= 1 })
}

func TestEngineReconfigureBeforeStartOnlySwapsRegistry(t *testing.T) {
	st := openStore(t)
	a := newFake("a")
	reg := NewRegistry()
	reg.Register(a)

	e := NewEngine(st, reg, func(string) bool { return true })
	fresh := newFake("b")
	reg2 := NewRegistry()
	reg2.Register(fresh)
	e.Reconfigure(reg2) // 未 Start：只换注册表，不该 panic

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Stop()
	waitFor(t, "启动后用新注册表", func() bool { return fresh.calls.Load() >= 1 })
	if a.calls.Load() != 0 {
		t.Fatal("旧注册表不该再被调起")
	}
}

func TestEngineTriggerUsesNewRegistry(t *testing.T) {
	st := openStore(t)
	a := newFake("a")
	a.block = true
	reg := NewRegistry()
	reg.Register(a)

	e := NewEngine(st, reg, func(string) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Stop()
	waitFor(t, "首次同步", func() bool { return a.calls.Load() >= 1 })

	fresh := newFake("a") // 同名新实例，模拟适配器重建
	reg2 := NewRegistry()
	reg2.Register(fresh)
	e.Reconfigure(reg2)

	before := fresh.calls.Load()
	e.Trigger("a")
	waitFor(t, "手动触发打到新适配器", func() bool { return fresh.calls.Load() > before })
}

func TestOnFailureOnlyOnTransition(t *testing.T) {
	st := openStore(t)
	e := NewEngine(st, NewRegistry(), func(string) bool { return true })

	hits := make(chan string, 4)
	e.SetOnFailure(func(name string, err error) { hits <- name })

	// 1) 库里没有状态行（从没跑过）→ 不通知。
	e.recordError("huozhi", errors.New("boom-1"))
	select {
	case <-hits:
		t.Fatal("首次失败不该通知")
	case <-time.After(50 * time.Millisecond):
	}

	// 2) 有行且 last_error 为空（此前正常）→ 通知。
	if err := st.SaveSourceState("huozhi", true, 0, "", time.Now().Unix(), "", 3); err != nil {
		t.Fatal(err)
	}
	e.recordError("huozhi", errors.New("boom-2"))
	select {
	case name := <-hits:
		if name != "huozhi" {
			t.Fatalf("回调源名不符: %q", name)
		}
	case <-time.After(time.Second):
		t.Fatal("由正常转失败应通知")
	}

	// 3) 仍在失败态 → 不重复通知。
	e.recordError("huozhi", errors.New("boom-3"))
	select {
	case <-hits:
		t.Fatal("持续失败不该重复通知")
	case <-time.After(50 * time.Millisecond):
	}
}
