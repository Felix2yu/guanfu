<script lang="ts">
  // 总览页：四张指标卡 + 收支趋势 + 来源分布，下面挂今日打卡 / 数据源 / 通知。
  //
  // 所有数字都来自 /api/v1/overview（一次聚合）与 /api/v1/sources，前端不做二次累加，
  // 免得出现「卡片和明细对不上」的经典问题。环比用的是等长上一期，同样来自聚合接口。
  import { onMount } from 'svelte';
  import {
    fetchEvents,
    fetchOverview,
    fillDays,
    fmtCount,
    fmtMoney,
    fmtWeekday,
    rangeFor,
    RANGE_LABELS,
    shiftDate,
    SOURCE_COLOR,
    sourceLabel,
    dayCount,
    type Event,
    type Overview,
    type RangeMode,
    type SourceState
  } from '../api.ts';
  import { deriveNotices, loadRead, saveRead } from '../notices.ts';
  import DonutChart from './DonutChart.svelte';
  import HabitCard from './HabitCard.svelte';
  import Icon from './Icon.svelte';
  import MetricCard from './MetricCard.svelte';
  import NoticeCard from './NoticeCard.svelte';
  import SourceStatusCard from './SourceStatusCard.svelte';
  import TopBar from './TopBar.svelte';
  import TrendChart from './TrendChart.svelte';

  let {
    sources,
    syncing = '',
    onSync,
    refreshTick = 0,
    date
  }: {
    sources: SourceState[];
    syncing?: string;
    onSync: (name: string) => void;
    refreshTick?: number;
    date: string;
  } = $props();

  const MODES: RangeMode[] = ['today', 'week', 'month', 'year'];

  let mode = $state<RangeMode>('month');
  let data = $state<Overview | null>(null);
  let prev = $state<Overview | null>(null);
  let loading = $state(true);
  let error = $state('');
  let query = $state('');

  let habits = $state<{ name: string; count: number }[]>([]);
  let habitLoading = $state(true);

  let read = $state<Record<string, boolean>>({});
  onMount(() => (read = loadRead()));

  const range = $derived(rangeFor(mode, date));
  const days = $derived(dayCount(range.from, range.to));

  $effect(() => {
    const { from, to } = rangeFor(mode, date);
    void refreshTick;
    // 上一期：等长的前一个窗口（近 30 天就跟前一个 30 天比），环比才有意义。
    const len = dayCount(from, to);
    const prevTo = shiftDate(from, -1);
    const prevFrom = shiftDate(prevTo, -(len - 1));

    let cancelled = false;
    loading = true;
    error = '';
    Promise.all([fetchOverview(from, to), fetchOverview(prevFrom, prevTo)])
      .then(([cur, before]) => {
        if (cancelled) return;
        data = cur;
        prev = before;
      })
      .catch((e) => {
        if (cancelled) return;
        error = String(e);
        data = null;
        prev = null;
      })
      .finally(() => {
        if (!cancelled) loading = false;
      });
    return () => {
      cancelled = true;
    };
  });

  // 今日打卡与所选区间无关：它永远是「今天」，所以单独按 date 取。
  $effect(() => {
    const d = date;
    void refreshTick;
    let cancelled = false;
    habitLoading = true;
    fetchEvents(d)
      .then((list) => {
        if (cancelled) return;
        const ev = list.filter((e: Event) => e.event_type === 'habit_check').at(-1);
        const raw = ev?.detail?.habits;
        habits = Array.isArray(raw)
          ? (raw as { name?: string; count?: number }[])
              .filter((h) => h && h.name)
              .map((h) => ({ name: String(h.name), count: Number(h.count ?? 1) }))
          : [];
      })
      .catch(() => {
        if (!cancelled) habits = [];
      })
      .finally(() => {
        if (!cancelled) habitLoading = false;
      });
    return () => {
      cancelled = true;
    };
  });

  const currency = $derived.by(() => {
    const t = data?.totals ?? {};
    if (t['CNY']) return 'CNY';
    const keys = Object.keys(t);
    return keys.length > 0 ? keys[0] : 'CNY';
  });

  const totals = $derived(data?.totals?.[currency] ?? { income_cents: 0, expense_cents: 0 });
  const prevTotals = $derived(prev?.totals?.[currency] ?? { income_cents: 0, expense_cents: 0 });

  const filledDays = $derived(data ? fillDays(data.from, data.to, data.days) : []);

  const notices = $derived(deriveNotices(sources));
  const unread = $derived(notices.filter((n) => !read[n.id]).length);

  const configured = $derived(sources.filter((s) => s.configured));
  const failing = $derived(configured.filter((s) => s.last_error).length);
  const missing = $derived(sources.length - configured.length);

  const greeting = $derived.by(() => {
    const h = new Date().getHours();
    if (h < 5) return '夜深了';
    if (h < 9) return '早上好';
    if (h < 12) return '上午好';
    if (h < 14) return '中午好';
    if (h < 18) return '下午好';
    return '晚上好';
  });

  /** 环比：上一期为 0 时不给百分比（除零编不出有意义的值）。 */
  function deltaOf(cur: number, before: number, betterWhenUp: boolean) {
    if (before <= 0) {
      return {
        text: cur > 0 ? '上期无数据' : '与上期持平',
        tone: 'muted' as const,
        dir: 'flat' as const
      };
    }
    const pct = ((cur - before) / before) * 100;
    const dir = pct > 0.5 ? ('up' as const) : pct < -0.5 ? ('down' as const) : ('flat' as const);
    if (dir === 'flat') return { text: '与上期持平', tone: 'muted' as const, dir };
    const good = betterWhenUp ? pct > 0 : pct < 0;
    return {
      text: `较上期 ${pct > 0 ? '+' : '−'}${Math.abs(pct).toFixed(1)}%`,
      tone: good ? ('pos' as const) : ('neg' as const),
      dir
    };
  }

  const expenseDelta = $derived(
    deltaOf(totals.expense_cents, prevTotals.expense_cents, /* 支出升高不是好事 */ false)
  );
  const incomeDelta = $derived(deltaOf(totals.income_cents, prevTotals.income_cents, true));
  const countDelta = $derived(deltaOf(data?.total_count ?? 0, prev?.total_count ?? 0, true));

  // 搜索只作用于「列表型」区块（打卡与通知）；图表和指标是聚合值，关键字无从过滤。
  const q = $derived(query.trim().toLowerCase());
  const shownHabits = $derived(q ? habits.filter((h) => h.name.toLowerCase().includes(q)) : habits);
  const shownNotices = $derived(
    q ? notices.filter((n) => n.text.toLowerCase().includes(q)) : notices
  );

  const donutItems = $derived(
    (data?.sources ?? []).map((s) => ({
      key: s.key,
      label: sourceLabel(s.key),
      value: s.count,
      color: SOURCE_COLOR[s.key] ?? 'var(--gf-ink-3)'
    }))
  );

  function markRead(id: string) {
    read = { ...read, [id]: true };
    saveRead(read);
  }

  function markAll() {
    const next = { ...read };
    for (const n of notices) next[n.id] = true;
    read = next;
    saveRead(next);
  }
</script>

<TopBar current="总览">
  <label
    class="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-[9px] border border-line bg-surface px-3 focus-within:border-line-strong md:w-[260px] md:flex-none"
  >
    <Icon name="search" size={15} cls="shrink-0 text-ink-3" />
    <input
      type="search"
      class="min-w-0 flex-1 bg-transparent fs-125 text-ink outline-none placeholder:text-ink-3"
      placeholder="搜索打卡或通知"
      bind:value={query}
      aria-label="搜索打卡或通知"
    />
  </label>

  <button
    type="button"
    class="flex h-9 shrink-0 items-center gap-1.5 rounded-[9px] border border-line bg-surface px-2.5 fs-125 text-ink-2 transition-colors hover:border-line-strong hover:text-ink disabled:opacity-50"
    onclick={() => onSync('all')}
    disabled={syncing !== ''}
  >
    <Icon name="sync" size={14} cls={syncing ? 'animate-spin' : ''} />
    <span class="hidden sm:inline">{syncing === 'all' ? '同步中' : '同步全部'}</span>
  </button>

  {#if data && data.total_count > 0}
    <!-- 下载链接指向 Go 后端的 /api 接口，不是 Kit 路由 -->
    <!-- eslint-disable svelte/no-navigation-without-resolve -->
    <a
      class="flex h-9 shrink-0 items-center gap-1.5 rounded-[9px] bg-accent px-2.5 fs-125 font-medium text-accent-ink transition-opacity hover:opacity-90"
      href="/api/v1/export?from={range.from}&to={range.to}"
      download
      title="导出 {range.from} ~ {range.to} 的全部事件（JSON）"
    >
      <Icon name="download" size={14} />
      <span class="hidden sm:inline">导出 JSON</span>
    </a>
    <!-- eslint-enable svelte/no-navigation-without-resolve -->
  {/if}
</TopBar>

<div class="px-4 py-5 md:px-6 lg:px-8">
  {#if error}
    <div class="mb-3 rounded-lg bg-chip px-3 py-2 fs-125 text-neg">{error}</div>
  {/if}

  <!-- 问候 + 时间范围 -->
  <div class="flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
    <div class="min-w-0">
      <h1 class="fs-24 leading-tight font-semibold tracking-[-0.01em]">{greeting}</h1>
      <p class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 fs-125 text-ink-2">
        <span>{date} 周{fmtWeekday(date)}</span>
        <span class="text-line-strong">·</span>
        <span>{RANGE_LABELS[mode]}个人数据概览</span>
        <span class="tnum rounded-md bg-chip px-1.5 py-0.5 fs-11 text-ink-2">
          {range.from} ~ {range.to} · {days} 天
        </span>
      </p>
    </div>

    <div class="flex shrink-0 rounded-[10px] bg-chip p-[3px]" role="group" aria-label="时间范围">
      {#each MODES as m (m)}
        <button
          type="button"
          class="rounded-[7px] px-2.5 py-1.5 fs-125 transition-colors
            {mode === m
            ? 'bg-surface font-medium text-ink shadow-[0_1px_3px_rgba(27,29,34,0.08)]'
            : 'text-ink-2 hover:text-ink'}"
          onclick={() => (mode = m)}
          aria-pressed={mode === m}
        >
          {RANGE_LABELS[m]}
        </button>
      {/each}
    </div>
  </div>

  <!-- 指标卡 -->
  <div class="mt-4 grid grid-cols-2 gap-3 xl:grid-cols-4">
    <MetricCard
      label="{RANGE_LABELS[mode]}支出"
      value={loading ? '—' : fmtMoney(totals.expense_cents)}
      delta={loading ? '' : expenseDelta.text}
      tone={expenseDelta.tone}
      dir={expenseDelta.dir}
    />
    <MetricCard
      label="{RANGE_LABELS[mode]}收入"
      value={loading ? '—' : fmtMoney(totals.income_cents)}
      delta={loading ? '' : incomeDelta.text}
      tone={incomeDelta.tone}
      dir={incomeDelta.dir}
    />
    <MetricCard
      label="事件总数"
      value={loading ? '—' : fmtCount(data?.total_count ?? 0)}
      unit="条"
      delta={loading ? '' : countDelta.text}
      tone={countDelta.tone}
      dir={countDelta.dir}
    />
    <MetricCard
      label="已配置数据源"
      value="{configured.length} / {sources.length}"
      dot={failing > 0 ? 'var(--gf-neg)' : 'var(--gf-pos)'}
      delta="{failing} 个失败 · {missing} 个未配置"
      tone={failing > 0 ? 'neg' : 'muted'}
      dir="flat"
    />
  </div>

  <!-- 趋势 + 分布 -->
  <div class="mt-3 grid gap-3 lg:grid-cols-[minmax(0,1.7fr)_minmax(0,1fr)]">
    <section class="flex flex-col rounded-xl border border-line bg-surface p-4">
      <div class="mb-2 flex flex-wrap items-center gap-x-4 gap-y-2">
        <h2 class="fs-14 font-semibold">收支趋势 · {RANGE_LABELS[mode]}</h2>
        <div class="ml-auto flex items-center gap-3 fs-11 text-ink-2">
          <span class="flex items-center gap-1.5">
            <span class="h-1.5 w-1.5 rounded-full" style:background="var(--gf-pos)"></span>收入
          </span>
          <span class="flex items-center gap-1.5">
            <span class="h-1.5 w-1.5 rounded-full" style:background="var(--gf-neg)"></span>支出
          </span>
        </div>
      </div>
      <TrendChart days={filledDays} {currency} />
      {#if filledDays.length > 2}
        <p class="mt-1 fs-11 text-ink-3">
          两条曲线各自独立量程：收入与支出量级通常差一个数量级，同轴会把支出压平。
        </p>
      {/if}
    </section>

    <section class="flex flex-col rounded-xl border border-line bg-surface p-4">
      <div class="mb-3 flex flex-wrap items-baseline gap-2">
        <h2 class="fs-14 font-semibold">数据源分布</h2>
        <span class="fs-11 text-ink-3"
          >{RANGE_LABELS[mode]} · 共 {fmtCount(data?.total_count ?? 0)} 条</span
        >
      </div>

      {#if donutItems.length === 0}
        <div class="flex flex-1 items-center justify-center py-8 fs-12 text-ink-3">
          该区间没有事件
        </div>
      {:else}
        <div class="flex flex-wrap items-center gap-x-5 gap-y-4">
          <DonutChart items={donutItems} total={data?.total_count ?? 0} />
          <ul class="min-w-[160px] flex-1 space-y-1.5">
            {#each data?.sources ?? [] as s (s.key)}
              <li class="flex items-center gap-2 fs-12">
                <span
                  class="h-1.5 w-1.5 shrink-0 rounded-full"
                  style:background={SOURCE_COLOR[s.key] ?? 'var(--gf-ink-3)'}
                ></span>
                <span class="min-w-0 flex-1 truncate text-ink-2">{sourceLabel(s.key)}</span>
                <span class="tnum shrink-0 fs-11 text-ink-3">{fmtCount(s.count)}</span>
              </li>
            {/each}
          </ul>
        </div>
      {/if}

      <p class="mt-auto pt-3 fs-11 text-ink-3">
        {configured.length} 个来源已配置{#if missing > 0}
          · {missing} 个未配置{/if}
      </p>
    </section>
  </div>

  <!-- 底部三块 -->
  <div class="mt-3 grid items-start gap-3 md:grid-cols-2 xl:grid-cols-3">
    <HabitCard
      habits={shownHabits}
      {date}
      loading={habitLoading}
      emptyText={q ? '没有匹配的打卡' : '今天还没有打卡记录'}
    />
    <SourceStatusCard {sources} {syncing} {onSync} />
    <div class="md:col-span-2 xl:col-span-1">
      <NoticeCard
        notices={shownNotices}
        {read}
        onRead={markRead}
        onReadAll={markAll}
        emptyText={q ? '没有匹配的通知' : '还没有同步记录，先跑一次同步'}
      />
    </div>
  </div>

  {#if q}
    <p class="mt-3 fs-11 text-ink-3">
      搜索「{query.trim()}」命中：打卡 {shownHabits.length}/{habits.length} 项，通知 {shownNotices.length}/{notices.length}
      条。指标与图表是聚合值，不受关键字过滤。
    </p>
  {:else if unread > 0}
    <p class="mt-3 fs-11 text-ink-3">{unread} 条未读通知，点任意一条即可标记已读。</p>
  {/if}
</div>
