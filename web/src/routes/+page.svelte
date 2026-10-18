<script lang="ts">
  import { resolve } from '$app/paths';
  import { onMount } from 'svelte';
  import EventCard from '#lib/components/EventCard.svelte';
  import Heatmap from '#lib/components/Heatmap.svelte';
  import {
    fetchCountsRange,
    fetchEvents,
    fetchEventsRange,
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
  import { initUI, disposeUI, setView, ui, VIEW_LABELS, type ViewMode } from '#lib/ui.svelte.ts';

  let date = $state(todayStr());
  let events = $state<Event[]>([]);
  let sources = $state<SourceState[]>([]);
  let range = $state({ min: '', max: '' });
  let loading = $state(false);
  let error = $state('');
  let syncing = $state('');

  // 日历视图：月份与每日条数。
  let heatMonth = $state(monthOf(todayStr()));
  let heatCounts = $state<Record<string, number>>({});

  // 时间线视图：以当前日期为终点向前开一个窗口，「加载更早」把窗口再往前推。
  const TL_STEP = 30;
  let tlDays = $state(TL_STEP);
  let tlEvents = $state<Event[]>([]);
  let tlLoading = $state(false);
  let tlTruncated = $state(false);

  onMount(() => {
    initUI();
    (async () => {
      try {
        [range, sources] = await Promise.all([fetchRange(), fetchSources()]);
      } catch (e) {
        error = String(e);
      }
      await load(date);
    })();
    return disposeUI;
  });

  const tlFrom = $derived(shiftDate(date, -(tlDays - 1)));

  $effect(() => {
    if (ui.view !== 'calendar') return;
    const month = heatMonth;
    void range; // 数据边界变化时（首次加载完）重取
    fetchCountsRange(monthStart(month), monthEnd(month))
      .then((c) => (heatCounts = c))
      .catch(() => (heatCounts = {}));
  });

  $effect(() => {
    if (ui.view !== 'timeline') return;
    const from = tlFrom;
    const to = date;
    void tlDays;
    tlLoading = true;
    fetchEventsRange(from, to, 1000)
      .then((list) => {
        tlEvents = list;
        // 服务端 limit 上限就是 1000，撞上了说明窗口里还有没取到的，如实标出来。
        tlTruncated = list.length >= 1000;
      })
      .catch((e) => (error = String(e)))
      .finally(() => (tlLoading = false));
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

  function pickView(v: ViewMode) {
    setView(v);
  }

  async function sync(source: string) {
    syncing = source;
    try {
      await triggerSync(source);
      // 同步是异步的，等 3 秒后刷新当前视图的数据。
      setTimeout(async () => {
        await load(date);
        sources = await fetchSources();
        if (ui.view === 'timeline') tlEvents = await fetchEventsRange(tlFrom, date, 1000);
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
    ...events
      .filter((e) => e.time_precision !== 'day')
      .sort((a, b) => a.occurred_at - b.occurred_at)
  ]);
  const incomeTotal = $derived(sumBy(events, 'income'));
  const expenseTotal = $derived(sumBy(events, 'expense'));
  const enabledSources = $derived(sources.filter((s) => s.enabled));

  function sumBy(list: Event[], kind: 'income' | 'expense'): number {
    return list
      .filter(
        (e) =>
          e.event_type === 'transaction' &&
          (kind === 'income' ? e.detail?.type === 'income' : e.detail?.type !== 'income')
      )
      .reduce((acc, e) => acc + (e.amount_cents ?? 0), 0);
  }

  // 时间线按日分组，新的日子在前。
  const tlGroups = $derived.by(() => {
    const map = new Map<string, Event[]>();
    for (const e of [...tlEvents].sort((a, b) => b.occurred_at - a.occurred_at)) {
      const list = map.get(e.local_date);
      if (list) list.push(e);
      else map.set(e.local_date, [e]);
    }
    return [...map.entries()].map(([day, list]) => ({
      day,
      // 组内按时刻升序读起来才顺（日子之间倒序、日子内部正序）。
      list: list.sort((a, b) => a.occurred_at - b.occurred_at),
      income: sumBy(list, 'income'),
      expense: sumBy(list, 'expense')
    }));
  });
</script>

<svelte:head>
  <title>观复 · {date}</title>
</svelte:head>

<div class="mx-auto max-w-3xl px-4 py-6">
  <!-- 顶栏：日期切换 + 当日汇总 -->
  <header class="mb-4 flex flex-wrap items-center gap-3">
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
      {#if incomeTotal > 0}<span class="text-emerald-600 dark:text-emerald-400"
          >+¥{(incomeTotal / 100).toFixed(2)}</span
        >{/if}
      {#if expenseTotal > 0}<span class="text-rose-600 dark:text-rose-400"
          >−¥{(expenseTotal / 100).toFixed(2)}</span
        >{/if}
      <span class="text-zinc-400">{events.length} 条</span>
    </div>
  </header>

  <!-- 视图切换：当日 / 日历 / 时间线（偏好记在本地，刷新后保持） -->
  <div class="mb-4 flex rounded-lg border border-zinc-200 p-0.5 dark:border-zinc-700">
    {#each ['day', 'calendar', 'timeline'] as v (v)}
      <button
        class="flex-1 rounded-md px-2 py-1 text-sm transition-colors {ui.view === v
          ? 'bg-zinc-900 font-medium text-white dark:bg-zinc-100 dark:text-zinc-900'
          : 'text-zinc-600 hover:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800'}"
        onclick={() => pickView(v as ViewMode)}
        aria-pressed={ui.view === v}
      >
        {VIEW_LABELS[v as ViewMode]}
      </button>
    {/each}
  </div>

  <!-- 源状态行：离线/报错一目了然，可手动触发同步 -->
  {#if enabledSources.length > 0}
    <!--
      刻意不折行：源数量会随配置涨到 11 个，折行会把右侧动作挤到第二行去。
      源 chips 单独横向滚动（滚条藏起来），动作组 shrink-0 钉在右侧。
    -->
    <div class="mb-4 flex items-center gap-2 text-xs text-zinc-500 dark:text-zinc-400">
      <div
        class="flex min-w-0 flex-1 [scrollbar-width:none] items-center gap-2 overflow-x-auto [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden"
      >
        {#each enabledSources as s (s.name)}
          <button
            class="flex shrink-0 items-center gap-1 rounded-full border border-zinc-200 px-2 py-0.5 transition-colors hover:border-zinc-400 dark:border-zinc-700"
            onclick={() => sync(s.name)}
            disabled={syncing === s.name}
            title={s.last_error ||
              `游标 ${s.cursor || '—'} · 上次同步 ${s.last_sync_at ? new Date(s.last_sync_at * 1000).toLocaleString() : '从未'}`}
          >
            <span class="h-1.5 w-1.5 rounded-full {s.last_error ? 'bg-rose-500' : 'bg-emerald-500'}"
            ></span>
            {s.name}{syncing === s.name ? ' …' : ''}
          </button>
        {/each}
      </div>
      <button
        class="shrink-0 rounded-full border border-zinc-200 px-2 py-0.5 hover:border-zinc-400 dark:border-zinc-700"
        onclick={() => sync('all')}
      >
        同步全部
      </button>
      {#if range.min}
        <!-- 一键导出：库里有数据才出现（空库导出是个只会骗人下载的空文件）。 -->
        <!-- 下载链接指向 Go 后端的 /api 接口，不是 Kit 路由，resolve() 在这里没有意义 -->
        <!-- eslint-disable svelte/no-navigation-without-resolve -->
        <a
          class="shrink-0 rounded-full border border-zinc-200 px-2 py-0.5 hover:border-zinc-400 dark:border-zinc-700"
          href="/api/v1/export?from={range.min}&to={range.max}"
          download
          title="导出 {range.min} ~ {range.max} 的全部事件（JSON）"
        >
          导出
        </a>
        <!-- eslint-enable svelte/no-navigation-without-resolve -->
      {/if}
      <a
        class="shrink-0 rounded-full border border-zinc-200 px-2 py-0.5 hover:border-zinc-400 dark:border-zinc-700"
        href={resolve('/settings')}>设置</a
      >
    </div>
  {/if}

  {#if error}
    <div
      class="mb-4 rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300"
    >
      {error}
    </div>
  {/if}

  {#if ui.view === 'calendar'}
    <!-- 日历视图：整月热力图，点格子进当日 -->
    <div class="mb-4 flex items-start gap-2">
      <button
        class="btn mt-3"
        onclick={() => (heatMonth = shiftMonth(heatMonth, -1))}
        aria-label="上个月">←</button
      >
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
      <button
        class="btn mt-3"
        onclick={() => (heatMonth = shiftMonth(heatMonth, 1))}
        aria-label="下个月">→</button
      >
    </div>
    <p class="text-xs text-zinc-400 dark:text-zinc-500">
      点任意一天进入当日视图；颜色深浅按当天事件条数。
    </p>
  {:else if ui.view === 'timeline'}
    <!-- 时间线视图：跨日连续流，日子倒序、日内时刻正序 -->
    <div class="mb-3 flex items-center gap-2 text-xs text-zinc-500 dark:text-zinc-400">
      <span class="tabular-nums">{tlFrom} ~ {date}（{tlDays} 天）</span>
      {#if tlLoading}<span>加载中…</span>{/if}
      <button
        class="ml-auto shrink-0 rounded-full border border-zinc-200 px-2 py-0.5 hover:border-zinc-400 disabled:opacity-50 dark:border-zinc-700"
        onclick={() => (tlDays += TL_STEP)}
        disabled={tlLoading}
      >
        加载更早 {TL_STEP} 天
      </button>
    </div>
    {#if tlTruncated}
      <div
        class="mb-3 rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:bg-amber-950/40 dark:text-amber-300"
      >
        这个窗口内的事件超过 1000 条上限，列表可能不完整——请改用日历逐日查看。
      </div>
    {/if}
    {#if !tlLoading && tlGroups.length === 0}
      <div class="py-16 text-center text-sm text-zinc-400">
        这段时间没有数据。{#if range.min}<span class="mt-1 block"
            >数据范围 {range.min} ~ {range.max}</span
          >{/if}
      </div>
    {:else}
      <div class="space-y-6">
        {#each tlGroups as g (g.day)}
          <section>
            <div
              class="mb-1 flex items-baseline gap-2 border-b border-zinc-200 pb-1 dark:border-zinc-800"
            >
              <h2 class="text-sm font-medium tabular-nums">{g.day} 周{fmtWeekday(g.day)}</h2>
              <span class="text-xs text-zinc-400 tabular-nums">{g.list.length} 条</span>
              <span class="ml-auto flex gap-2 text-xs tabular-nums">
                {#if g.income > 0}<span class="text-emerald-600 dark:text-emerald-400"
                    >+¥{(g.income / 100).toFixed(2)}</span
                  >{/if}
                {#if g.expense > 0}<span class="text-rose-600 dark:text-rose-400"
                    >−¥{(g.expense / 100).toFixed(2)}</span
                  >{/if}
              </span>
            </div>
            <div class="relative pl-1">
              <div class="timeline-rail"></div>
              {#each g.list as event (event.id)}
                <EventCard {event} />
              {/each}
            </div>
          </section>
        {/each}
      </div>
    {/if}
  {:else}
    <!-- 当日视图：这一天的时间轴 -->
    {#if loading}
      <div class="py-16 text-center text-sm text-zinc-400">加载中…</div>
    {:else if sorted.length === 0}
      <div class="py-16 text-center text-sm text-zinc-400">
        这一天还没有数据。
        {#if range.min}<span class="mt-1 block">数据范围 {range.min} ~ {range.max}</span>{/if}
      </div>
    {:else}
      <div class="relative pl-1">
        <div class="timeline-rail"></div>
        {#each sorted as event (event.id)}
          <EventCard {event} />
        {/each}
      </div>
    {/if}
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
