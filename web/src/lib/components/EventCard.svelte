<script lang="ts">
  import { fmtAmount, fmtTime, SOURCE_META, TYPE_LABEL, type Event, type MediaRef } from '../api';

  let { event }: { event: Event } = $props();

  const meta = $derived(SOURCE_META[event.source] ?? { label: event.source, dot: 'bg-zinc-400', text: 'text-zinc-500' });
  const typeLabel = $derived(TYPE_LABEL[event.event_type] ?? event.event_type);
  const isDay = $derived(event.time_precision === 'day');
  // 优先服务端下发的本地时刻（GUANFU_TZ），仅在缺失时回退浏览器时区。
  const timeText = $derived(event.local_time ?? fmtTime(event.occurred_at));
  const amountText = $derived(
    event.amount_cents ? `${event.detail?.type === 'income' ? '+' : '−'}¥${fmtAmount(event.amount_cents)}` : ''
  );

  // 封面（video_watch）：取第一张带代理路径的媒体；加载失败则隐藏。
  const cover = $derived(
    event.event_type === 'video_watch'
      ? (event.media ?? []).find((m: MediaRef) => m.kind === 'cover' && m.proxy_path)
      : undefined
  );
  let coverFailed = $state(false);

  // 演出评分（performance）：整星展示。
  const rating = $derived(event.event_type === 'performance' ? Number(event.detail?.rating ?? 0) : 0);

  // 健康日报（health_day）：指标 chips，最多 4 个（与服务端摘要一致）。
  const healthChips = $derived.by(() => {
    if (event.event_type !== 'health_day' || !event.detail) return [];
    return Object.entries(event.detail)
      .map(([key, v]) => ({
        key,
        label: String((v as { label?: string }).label ?? key),
        display: String((v as { display?: string }).display ?? '')
      }))
      .filter((c) => c.display)
      .slice(0, 4);
  });

  // 到访（location_visit）：无地名时展示坐标兜底。
  const coordText = $derived(
    event.event_type === 'location_visit' && !event.location_name && (event.latitude || event.longitude)
      ? `${event.latitude?.toFixed(4)}, ${event.longitude?.toFixed(4)}`
      : ''
  );
</script>

<!-- 紧凑单行事件卡：左侧源色点 + 时间（全天事件显示「全天」徽标）+ 标题/摘要。 -->
<div
  class="relative flex items-baseline gap-3 rounded-lg px-3 py-2 transition-colors hover:bg-black/[0.03] dark:hover:bg-white/[0.05]"
>
  <span class="mt-[7px] h-2 w-2 shrink-0 rounded-full {meta.dot}"></span>

  <span class="w-11 shrink-0 text-xs tabular-nums text-zinc-500 dark:text-zinc-400">
    {#if isDay}
      <span
        class="rounded bg-zinc-200/70 px-1.5 py-0.5 text-[11px] font-medium text-zinc-600 dark:bg-zinc-700/70 dark:text-zinc-300"
        >全天</span
      >
    {:else}
      {timeText}
    {/if}
  </span>

  {#if cover && !coverFailed}
    <img
      src={cover.proxy_path}
      alt=""
      class="mt-0.5 h-6 w-10 shrink-0 self-center rounded object-cover"
      loading="lazy"
      onerror={() => (coverFailed = true)}
    />
  {/if}

  <div class="min-w-0 flex-1">
    <div class="flex items-baseline gap-2">
      <span class="truncate font-medium text-zinc-900 dark:text-zinc-100">{event.title}</span>
      {#if amountText}
        <span class="shrink-0 text-sm font-semibold tabular-nums {event.detail?.type === 'income' ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400'}">
          {amountText}
        </span>
      {/if}
      {#if rating > 0}
        <span class="shrink-0 text-xs tracking-tight text-amber-500" aria-label="评分 {rating} 星">
          {'★'.repeat(Math.min(rating, 5))}{#if rating < 5}<span class="text-zinc-300 dark:text-zinc-600">{'★'.repeat(5 - rating)}</span>{/if}
        </span>
      {/if}
      {#if event.location_name}
        <span class="shrink-0 truncate text-xs text-zinc-500 dark:text-zinc-400">📍 {event.location_name}</span>
      {:else if coordText}
        <span class="shrink-0 truncate text-xs tabular-nums text-zinc-400 dark:text-zinc-500">📍 {coordText}</span>
      {/if}
    </div>
    {#if event.summary && event.summary !== event.title}
      <div class="truncate text-xs text-zinc-500 dark:text-zinc-400">{event.summary}</div>
    {/if}
    {#if healthChips.length > 0}
      <div class="mt-1 flex flex-wrap gap-1">
        {#each healthChips as chip (chip.key)}
          <span class="rounded bg-zinc-100 px-1.5 py-0.5 text-[11px] tabular-nums text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300">
            {chip.label} {chip.display}
          </span>
        {/each}
      </div>
    {/if}
  </div>

  <span class="shrink-0 text-[11px] {meta.text}">
    {#if typeLabel === meta.label}{typeLabel}{:else}{typeLabel} · {meta.label}{/if}
  </span>
</div>
