<script lang="ts">
  import { onMount } from 'svelte';
  import {
    fetchCountsRange,
    fetchEvents,
    fetchEventsRange,
    fetchRange,
    fetchSources,
    fmtMoney,
    fmtWeekday,
    monthEnd,
    monthOf,
    monthStart,
    shiftDate,
    shiftMonth,
    sourceLabel,
    todayStr,
    triggerSync,
    TYPE_LABEL,
    waitSyncDone,
    type Event,
    type SourceState
  } from '#lib/api.ts';
  import EventCard from '#lib/components/EventCard.svelte';
  import Heatmap from '#lib/components/Heatmap.svelte';
  import Icon from '#lib/components/Icon.svelte';
  import IngestDialog from '#lib/components/IngestDialog.svelte';
  import Overview from '#lib/components/Overview.svelte';
  import SearchBox from '#lib/components/SearchBox.svelte';
  import TopBar from '#lib/components/TopBar.svelte';
  import { setView, ui, VIEW_LABELS, type ViewMode } from '#lib/ui.svelte.ts';

  let date = $state(todayStr());
  let events = $state<Event[]>([]);
  let sources = $state<SourceState[]>([]);
  let range = $state({ min: '', max: '' });
  let loading = $state(false);
  let error = $state('');
  let syncing = $state('');
  // 总览页自己持有数据；同步完成后靠这个计数通知它重取。
  let refreshTick = $state(0);

  // 日历视图：月份与每日条数。
  let heatMonth = $state(monthOf(todayStr()));
  let heatCounts = $state<Record<string, number>>({});

  // 时间线视图：以当前日期为终点向前开一个窗口，「加载更早」把窗口再往前推。
  const TL_STEP = 30;
  let tlDays = $state(TL_STEP);
  let tlEvents = $state<Event[]>([]);
  let tlLoading = $state(false);
  let tlTruncated = $state(false);

  // 类型筛选：当日与时间线共用；空串 = 全部。切视图/换日期时清掉——
  // 两个视图、两天的类型集合不同，残留一个不存在的类型只会得到空列表。
  let typeFilter = $state('');

  // 「补一笔」手工补录对话框。
  let showIngest = $state(false);

  onMount(() => {
    (async () => {
      try {
        [range, sources] = await Promise.all([fetchRange(), fetchSources()]);
      } catch (e) {
        error = String(e);
      }
      await load(date);
    })();
  });

  const tlFrom = $derived(shiftDate(date, -(tlDays - 1)));

  $effect(() => {
    if (ui.view !== 'calendar') return;
    const month = heatMonth;
    void range; // 数据边界变化时（首次加载完）重取
    void refreshTick; // 同步完成后（refreshTick++）重取：新增事件要立即反映到格子
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
    typeFilter = '';
    heatMonth = monthOf(d);
    await load(d);
  }

  function dayOffset(days: number) {
    void goto(shiftDate(date, days));
  }

  function onDateInput(e: { currentTarget: HTMLInputElement }) {
    const v = e.currentTarget.value;
    if (v) void goto(v);
  }

  function pickView(v: ViewMode) {
    setView(v);
    typeFilter = '';
  }

  // 轮询等目标源真正跑完（见 api.ts waitSyncDone 的宽限说明），
  // 顺带把最新源状态写回页面：同步进行中 chips/状态就能实时跳动。
  async function sync(source: string) {
    syncing = source;
    try {
      await triggerSync(source);
      await waitSyncDone(source, { onUpdate: (list) => (sources = list) });
      await load(date);
      // range 只在挂载时拉过一次；同步可能带来更早/更晚的数据，边界要跟着刷新。
      range = await fetchRange().catch(() => range);
      sources = await fetchSources().catch(() => sources);
      if (ui.view === 'timeline')
        tlEvents = await fetchEventsRange(tlFrom, date, 1000).catch(() => tlEvents);
      refreshTick += 1; // 通知总览与日历热力图重取
    } catch (e) {
      error = String(e);
    } finally {
      // 无论哪一步失败都必须解锁，否则所有同步按钮会永久禁用。
      syncing = '';
    }
  }

  // 补录成功后的刷新：落在当前日期就重载当日，落在别的日期就跳过去看。
  async function afterIngest(d: string) {
    showIngest = false;
    if (d !== date) {
      await goto(d);
    } else {
      await load(date);
    }
    range = await fetchRange().catch(() => range);
    refreshTick += 1;
  }

  // 全天事件置顶展示（日记/健康汇总等无具体时刻），其余按时刻排列。
  const sorted = $derived.by(() => {
    const list = typeFilter ? events.filter((e) => e.event_type === typeFilter) : events;
    return [
      ...list.filter((e) => e.time_precision === 'day'),
      ...list
        .filter((e) => e.time_precision !== 'day')
        .sort((a, b) => a.occurred_at - b.occurred_at)
    ];
  });
  const incomeTotal = $derived(sumBy(events, 'income'));
  const expenseTotal = $derived(sumBy(events, 'expense'));
  const enabledSources = $derived(sources.filter((s) => s.enabled));
  const sourceErrors = $derived(enabledSources.filter((s) => s.last_error).length);

  // 类型筛选的候选集：跟着当前视图的数据走。
  const dayTypes = $derived([...new Set(events.map((e) => e.event_type))]);
  const tlTypes = $derived([...new Set(tlEvents.map((e) => e.event_type))]);
  const activeTypes = $derived(ui.view === 'timeline' ? tlTypes : dayTypes);

  function typeChipCls(active: boolean): string {
    return `rounded-full border px-2 py-0.5 transition-colors ${
      active
        ? 'border-accent bg-accent text-accent-ink'
        : 'border-line bg-surface text-ink-2 hover:border-line-strong hover:text-ink'
    }`;
  }

  function sumBy(list: Event[], kind: 'income' | 'expense'): number {
    return list
      .filter((e) => {
        if (e.event_type !== 'transaction' || !e.amount_cents) return false;
        const t = e.detail?.type;
        // 转账/报销是内部资金划转，与后端 stats 口径一致：不计入收支。
        if (t === 'transfer' || t === 'reimburse') return false;
        return kind === 'income' ? t === 'income' : t !== 'income';
      })
      .reduce((acc, e) => acc + (e.amount_cents ?? 0), 0);
  }

  // 时间线按日分组，新的日子在前。
  const tlGroups = $derived.by(() => {
    // 用普通对象而非 Map：'YYYY-MM-DD' 不是整数下标键，Object.entries 会保留插入顺序，
    // 免掉 svelte/prefer-svelte-reactivity 的告警（这里也不需要响应式容器）。
    const list = typeFilter ? tlEvents.filter((e) => e.event_type === typeFilter) : tlEvents;
    const byDay: Record<string, Event[]> = {};
    for (const e of [...list].sort((a, b) => b.occurred_at - a.occurred_at)) {
      (byDay[e.local_date] ??= []).push(e);
    }
    return Object.entries(byDay).map(([day, list]) => ({
      day,
      // 组内按时刻升序读起来才顺（日子之间倒序、日子内部正序）。
      list: list.sort((a, b) => a.occurred_at - b.occurred_at),
      income: sumBy(list, 'income'),
      expense: sumBy(list, 'expense')
    }));
  });
</script>

<svelte:head>
  <title>观复 · {ui.view === 'overview' ? '总览' : VIEW_LABELS[ui.view]}</title>
</svelte:head>

{#if ui.view === 'overview'}
  <Overview {sources} {syncing} onSync={sync} {refreshTick} {date} />
{:else}
  <TopBar current={VIEW_LABELS[ui.view]}>
    {#if ui.view === 'calendar'}
      <button
        type="button"
        class="flex h-9 items-center gap-1.5 rounded-[9px] border border-line bg-surface px-2.5 fs-125 text-ink-2 transition-colors hover:border-line-strong hover:text-ink disabled:opacity-50"
        onclick={() => sync('all')}
        disabled={syncing !== ''}
      >
        <Icon name="sync" size={14} cls={syncing ? 'animate-spin' : ''} />
        {syncing === 'all' ? '同步中' : '同步全部'}
      </button>
      {#if range.min}
        <!-- eslint-disable svelte/no-navigation-without-resolve -->
        <a
          class="flex h-9 items-center gap-1.5 rounded-[9px] bg-accent px-2.5 fs-125 font-medium text-accent-ink transition-opacity hover:opacity-90"
          href="/api/v1/export?from={range.min}&to={range.max}"
          download
          title="导出 {range.min} ~ {range.max} 的全部事件（JSON）"
        >
          <Icon name="download" size={14} />
          导出 JSON
        </a>
        <!-- eslint-enable svelte/no-navigation-without-resolve -->
      {/if}
    {:else}
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="flex h-9 items-center gap-1.5 rounded-[9px] border border-line bg-surface px-2.5 fs-125 text-ink-2 transition-colors hover:border-line-strong hover:text-ink"
          onclick={() => (showIngest = true)}
          aria-label="补一笔（手工录入事件）"
          title="补一笔（手工录入事件）"
        >
          <Icon name="plus" size={14} />
          <span class="hidden sm:inline">补一笔</span>
        </button>
        <SearchBox
          onpick={(d: string) => {
            pickView('day');
            void goto(d);
          }}
        />
        <div class="flex items-center gap-1">
        <button
          type="button"
          class="flex h-9 w-9 items-center justify-center rounded-[9px] border border-line bg-surface text-ink-2 transition-colors hover:border-line-strong hover:text-ink"
          onclick={() => dayOffset(-1)}
          aria-label="前一天"
        >
          <Icon name="chevron-left" size={16} />
        </button>
        <input
          type="date"
          class="tnum h-9 rounded-[9px] border border-line bg-surface px-2 fs-125 text-ink"
          value={date}
          onchange={onDateInput}
          min={range.min || undefined}
          max={range.max || undefined}
          aria-label="选择日期"
        />
        <button
          type="button"
          class="flex h-9 w-9 items-center justify-center rounded-[9px] border border-line bg-surface text-ink-2 transition-colors hover:border-line-strong hover:text-ink"
          onclick={() => dayOffset(1)}
          aria-label="后一天"
        >
          <Icon name="chevron-right" size={16} />
        </button>
        {#if date !== todayStr()}
          <button
            type="button"
            class="h-9 rounded-[9px] border border-line bg-surface px-2.5 fs-125 text-ink-2 transition-colors hover:border-line-strong hover:text-ink"
            onclick={() => goto(todayStr())}>今天</button
          >
        {/if}
        </div>
      </div>
    {/if}
  </TopBar>

  <div
    class="mx-auto w-full px-4 py-4 md:px-6 lg:px-8 {ui.view === 'calendar'
      ? 'max-w-[520px]'
      : 'max-w-[1080px]'}"
  >
    {#if error}
      <div class="mb-3 rounded-lg bg-chip px-3 py-2 fs-125 text-neg">{error}</div>
    {/if}

    <!-- 当日汇总：只有「当日」视图需要；日历是月视图、时间线是区间，
         头顶挂单日收支只会让人以为那是整个窗口的合计 -->
    {#if ui.view === 'day'}
      <div class="mb-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h1 class="tnum fs-20 font-semibold tracking-[-0.01em]">{date}</h1>
        <span class="fs-13 text-ink-2">周{fmtWeekday(date)}</span>
        <span class="tnum ml-auto flex items-baseline gap-3 fs-13">
          {#if incomeTotal > 0}<span class="text-pos">+{fmtMoney(incomeTotal)}</span>{/if}
          {#if expenseTotal > 0}<span class="text-neg">−{fmtMoney(expenseTotal)}</span>{/if}
          <span class="text-ink-3">{events.length} 条</span>
        </span>
      </div>
    {/if}

    <!-- 类型筛选：当日/时间线共用，只列当前数据里实际出现的类型；多于一种才有筛选价值。 -->
    {#if ui.view !== 'calendar' && activeTypes.length > 1}
      <div class="mb-3 flex flex-wrap items-center gap-1.5 fs-11">
        <span class="text-ink-3">类型</span>
        <button
          type="button"
          class={typeChipCls(typeFilter === '')}
          onclick={() => (typeFilter = '')}>全部</button
        >
        {#each activeTypes as t (t)}
          <button
            type="button"
            class={typeChipCls(typeFilter === t)}
            onclick={() => (typeFilter = typeFilter === t ? '' : t)}
          >
            {TYPE_LABEL[t] ?? t}
          </button>
        {/each}
      </div>
    {/if}

    <!-- 源状态 chips：桌面/平板一行横滚，动作钉在右侧不折行。
         手机放不下这一排，退化成「N 个源 · M 个异常」摘要，逐个同步在总览的状态卡里做。
         日历视图同理，同步/导出入口在顶栏。 -->
    {#if enabledSources.length > 0 && ui.view !== 'calendar'}
      <div class="mb-3 flex items-center gap-2 fs-11 text-ink-3">
        <div
          class="no-scrollbar hidden min-w-0 flex-1 items-center gap-1.5 overflow-x-auto md:flex"
        >
          {#each enabledSources as s (s.name)}
            <button
              type="button"
              class="flex shrink-0 items-center gap-1.5 rounded-full border border-line bg-surface px-2 py-1 transition-colors hover:border-line-strong disabled:opacity-50"
              onclick={() => sync(s.name)}
              disabled={syncing !== ''}
              title={s.last_error ||
                `上次同步 ${s.last_sync_at ? new Date(s.last_sync_at * 1000).toLocaleString('zh-CN', { hour12: false }) : '从未'}`}
            >
              <span
                class="h-1.5 w-1.5 rounded-full"
                style:background={s.last_error ? 'var(--gf-neg)' : 'var(--gf-pos)'}
              ></span>
              {sourceLabel(s.name)}{syncing === s.name ? ' …' : ''}
            </button>
          {/each}
        </div>

        <span class="md:hidden">
          {enabledSources.length} 个源{#if sourceErrors > 0}<span class="text-neg"
              >· {sourceErrors} 个异常</span
            >{/if}
        </span>

        <div class="ml-auto flex shrink-0 items-center gap-2">
          <button
            type="button"
            class="rounded-full border border-line bg-surface px-2 py-1 transition-colors hover:border-line-strong hover:text-ink disabled:opacity-50"
            onclick={() => sync('all')}
            disabled={syncing !== ''}
          >
            同步全部
          </button>
          {#if range.min}
            <!-- 下载链接指向 Go 后端的 /api 接口，不是 Kit 路由，resolve() 在这里没有意义 -->
            <!-- eslint-disable svelte/no-navigation-without-resolve -->
            <a
              class="rounded-full border border-line bg-surface px-2 py-1 transition-colors hover:border-line-strong hover:text-ink"
              href="/api/v1/export?from={range.min}&to={range.max}"
              download
              title="导出 {range.min} ~ {range.max} 的全部事件（JSON）"
            >
              导出 JSON
            </a>
            <!-- eslint-enable svelte/no-navigation-without-resolve -->
          {/if}
        </div>
      </div>
    {/if}

    {#if ui.view === 'calendar'}
      <!-- 日历视图：整月热力图，点格子进当日 -->
      <Heatmap
        month={heatMonth}
        counts={heatCounts}
        selected={date}
        minDate={range.min}
        maxDate={range.max}
        onpick={(d: string) => {
          pickView('day');
          void goto(d);
        }}
        onShift={(delta: number) => (heatMonth = shiftMonth(heatMonth, delta))}
      />
      <p class="mt-2 fs-11 text-ink-3">点任意一天进入当日视图；颜色深浅按当天事件条数。</p>
    {:else if ui.view === 'timeline'}
      <!-- 时间线视图：跨日连续流，日子倒序、日内时刻正序 -->
      <div class="mb-3 flex items-center gap-2 fs-11 text-ink-3">
        <span class="tnum">{tlFrom} ~ {date}（{tlDays} 天）</span>
        {#if tlLoading}<span>加载中…</span>{/if}
        <button
          type="button"
          class="ml-auto shrink-0 rounded-full border border-line bg-surface px-2 py-1 transition-colors hover:border-line-strong hover:text-ink disabled:opacity-50"
          onclick={() => (tlDays += TL_STEP)}
          disabled={tlLoading}
        >
          加载更早 {TL_STEP} 天
        </button>
      </div>
      {#if tlTruncated}
        <div class="mb-3 rounded-lg bg-chip px-3 py-2 fs-11 text-neg">
          这个窗口内的事件超过 1000 条上限，列表可能不完整——请改用日历逐日查看。
        </div>
      {/if}
      {#if !tlLoading && tlGroups.length === 0}
        <div class="py-16 text-center fs-125 text-ink-3">
          这段时间没有数据。{#if range.min}<span class="mt-1 block"
              >数据范围 {range.min} ~ {range.max}</span
            >{/if}
        </div>
      {:else}
        <div class="space-y-5">
          {#each tlGroups as g (g.day)}
            <section>
              <div class="mb-1 flex items-baseline gap-2 border-b border-line pb-1.5">
                <h2 class="tnum fs-13 font-medium">{g.day} 周{fmtWeekday(g.day)}</h2>
                <span class="tnum fs-11 text-ink-3">{g.list.length} 条</span>
                <span class="tnum ml-auto flex gap-2 fs-11">
                  {#if g.income > 0}<span class="text-pos">+{fmtMoney(g.income)}</span>{/if}
                  {#if g.expense > 0}<span class="text-neg">−{fmtMoney(g.expense)}</span>{/if}
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
        <div class="py-16 text-center fs-125 text-ink-3">加载中…</div>
      {:else if sorted.length === 0}
        <div class="py-16 text-center fs-125 text-ink-3">
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
{/if}

{#if showIngest}
  <IngestDialog {date} onclose={() => (showIngest = false)} onsaved={afterIngest} />
{/if}
