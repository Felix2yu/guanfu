<script lang="ts">
  import { onMount } from 'svelte';
  import EventCard from '#lib/components/EventCard.svelte';
  import Heatmap from '#lib/components/Heatmap.svelte';
  import {
    fetchCountsRange,
    fetchEvents,
    fetchRange,
    fetchSources,
    fmtWeekday,
    monthEnd,
    monthOf,
    monthStart,
    shiftDate,
    shiftMonth,
    todayStr,
    triggerSync,
    type Event,
    type SourceState
  } from '#lib/api.ts';

  let date = $state(todayStr());
  let events = $state<Event[]>([]);
  let sources = $state<SourceState[]>([]);
  let range = $state({ min: '', max: '' });
  let loading = $state(false);
  let error = $state('');
  let syncing = $state('');

  // 日历热力图：默认收起，展开时拉取当月每日事件数。
  let showHeatmap = $state(false);
  let heatMonth = $state(monthOf(todayStr()));
  let heatCounts = $state<Record<string, number>>({});

  onMount(async () => {
    try {
      [range, sources] = await Promise.all([fetchRange(), fetchSources()]);
    } catch (e) {
      error = String(e);
    }
    await load(date);
  });

  $effect(() => {
    if (!showHeatmap) return;
    // 依赖 month 与 range（range 变化时数据边界可能变化）。
    const month = heatMonth;
    void range;
    fetchCountsRange(monthStart(month), monthEnd(month))
      .then((c) => (heatCounts = c))
      .catch(() => (heatCounts = {}));
  });

  async function load(d: string) {
    loading = true;
    error = '';
    try {
      events = await fetchEvents(d);
    } catch (e) {
      error = String(e);
      events = [];
    } finally {
      loading = false;
    }
  }

  async function goto(d: string) {
    if (d === date) return;
    date = d;
    heatMonth = monthOf(d);
    await load(d);
  }

  function dayOffset(days: number) {
    goto(shiftDate(date, days));
  }

  async function sync(source: string) {
    syncing = source;
    try {
      await triggerSync(source);
      // 同步是异步的，等 3 秒后刷新当前日。
      setTimeout(async () => {
        await load(date);
        sources = await fetchSources();
        syncing = '';
      }, 3000);
    } catch (e) {
      error = String(e);
      syncing = '';
    }
  }

  // 全天事件置顶展示（日记/健康汇总等无具体时刻），其余按时刻排列。
  const sorted = $derived([
    ...events.filter((e) => e.time_precision === 'day'),
    ...events.filter((e) => e.time_precision !== 'day').sort((a, b) => a.occurred_at - b.occurred_at)
  ]);
  const incomeTotal = $derived(
    events.filter((e) => e.event_type === 'transaction' && e.detail?.type === 'income').reduce((s, e) => s + (e.amount_cents ?? 0), 0)
  );
  const expenseTotal = $derived(
    events.filter((e) => e.event_type === 'transaction' && e.detail?.type === 'expense').reduce((s, e) => s + (e.amount_cents ?? 0), 0)
  );
  const enabledSources = $derived(sources.filter((s) => s.enabled));
</script>

<svelte:head>
  <title>观复 · {date}</title>
</svelte:head>

<div class="mx-auto max-w-3xl px-4 py-6">
  <!-- 顶栏：日期切换 + 今日汇总 -->
  <header class="mb-5 flex flex-wrap items-center gap-3">
    <button class="btn" onclick={() => dayOffset(-1)} aria-label="前一天">←</button>
    <div class="flex items-baseline gap-2">
      <input
        type="date"
        class="rounded-lg border border-zinc-200 bg-white px-2 py-1 text-sm tabular-nums dark:border-zinc-700 dark:bg-zinc-800"
        bind:value={date}
        onchange={() => load(date)}
        min={range.min || undefined}
        max={range.max || undefined}
      />
      <h1 class="text-lg font-semibold">周{fmtWeekday(date)}</h1>
    </div>
    <button class="btn" onclick={() => dayOffset(1)} aria-label="后一天">→</button>
    {#if date !== todayStr()}
      <button class="btn" onclick={() => goto(todayStr())}>今天</button>
    {/if}

    <div class="ml-auto flex items-center gap-3 text-sm tabular-nums">
      {#if incomeTotal > 0}<span class="text-emerald-600 dark:text-emerald-400">+¥{(incomeTotal / 100).toFixed(2)}</span>{/if}
      {#if expenseTotal > 0}<span class="text-rose-600 dark:text-rose-400">−¥{(expenseTotal / 100).toFixed(2)}</span>{/if}
      <span class="text-zinc-400">{events.length} 条</span>
    </div>
  </header>

  <!-- 源状态行：离线/报错一目了然，可手动触发同步 -->
  {#if enabledSources.length > 0}
    <!--
      刻意不折行：源数量会随配置涨到 11 个，折行会把「同步全部 / 导出 / 热力图」
      这三个动作挤到第二行去。改成源 chips 单独横向滚动（滚条藏起来），
      动作组 shrink-0 钉在右侧，任何视口宽度下都保持一行。
    -->
    <div class="mb-4 flex items-center gap-2 text-xs text-zinc-500 dark:text-zinc-400">
      <div
        class="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto [-ms-overflow-style:none] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      >
        {#each enabledSources as s (s.name)}
          <button
            class="flex shrink-0 items-center gap-1 rounded-full border border-zinc-200 px-2 py-0.5 transition-colors hover:border-zinc-400 dark:border-zinc-700"
            onclick={() => sync(s.name)}
            disabled={syncing === s.name}
            title={s.last_error || `游标 ${s.cursor || '—'} · 上次同步 ${s.last_sync_at ? new Date(s.last_sync_at * 1000).toLocaleString() : '从未'}`}
          >
            <span class="h-1.5 w-1.5 rounded-full {s.last_error ? 'bg-rose-500' : 'bg-emerald-500'}"></span>
            {s.name}{syncing === s.name ? ' …' : ''}
          </button>
        {/each}
      </div>
      <button class="shrink-0 rounded-full border border-zinc-200 px-2 py-0.5 hover:border-zinc-400 dark:border-zinc-700" onclick={() => sync('all')}>
        同步全部
      </button>
      {#if range.min}
        <!-- 一键导出：库里有数据才出现（空库导出是个只会骗人下载的空文件）。 -->
        <a
          class="shrink-0 rounded-full border border-zinc-200 px-2 py-0.5 hover:border-zinc-400 dark:border-zinc-700"
          href="/api/v1/export?from={range.min}&to={range.max}"
          download
          title="导出 {range.min} ~ {range.max} 的全部事件（JSON）"
        >
          导出
        </a>
      {/if}
      <button
        class="shrink-0 rounded-full border px-2 py-0.5 transition-colors {showHeatmap
          ? 'border-zinc-400 text-zinc-700 dark:text-zinc-200'
          : 'border-zinc-200 hover:border-zinc-400 dark:border-zinc-700'}"
        onclick={() => (showHeatmap = !showHeatmap)}
      >
        {showHeatmap ? '收起热力图' : '热力图'}
      </button>
    </div>
  {/if}

  <!-- 日历热力图：月视图，点击格子跳日 -->
  {#if showHeatmap}
    <div class="mb-4 flex items-start gap-2">
      <button class="btn mt-3" onclick={() => (heatMonth = shiftMonth(heatMonth, -1))} aria-label="上个月">←</button>
      <div class="flex-1">
        <Heatmap
          month={heatMonth}
          counts={heatCounts}
          selected={date}
          minDate={range.min}
          maxDate={range.max}
          onpick={(d: string) => goto(d)}
        />
      </div>
      <button class="btn mt-3" onclick={() => (heatMonth = shiftMonth(heatMonth, 1))} aria-label="下个月">→</button>
    </div>
  {/if}

  {#if error}
    <div class="mb-4 rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">
      {error}
    </div>
  {/if}

  <!-- 时间轴 -->
  {#if loading}
    <div class="py-16 text-center text-sm text-zinc-400">加载中…</div>
  {:else if sorted.length === 0}
    <div class="py-16 text-center text-sm text-zinc-400">
      这一天还没有数据。
      {#if range.min}<span class="block mt-1">数据范围 {range.min} ~ {range.max}</span>{/if}
    </div>
  {:else}
    <div class="relative pl-1">
      <div class="timeline-rail"></div>
      {#each sorted as event (event.id)}
        <EventCard {event} />
      {/each}
    </div>
  {/if}
</div>

<style>
  .btn {
    border-radius: 0.5rem;
    border: 1px solid var(--color-zinc-200, #e4e4e7);
    padding: 0.25rem 0.625rem;
    font-size: 0.875rem;
    background: white;
    transition: border-color 0.15s;
  }
  .btn:hover {
    border-color: #a1a1aa;
  }
  :global(.dark) .btn {
    background: #27272a;
    border-color: #3f3f46;
  }
</style>
