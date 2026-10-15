<script lang="ts">
  import { fmtAmount, fmtTime, SOURCE_META, TYPE_LABEL, type Event } from '../api';

  let { event }: { event: Event } = $props();

  const meta = $derived(SOURCE_META[event.source] ?? { label: event.source, dot: 'bg-zinc-400', text: 'text-zinc-500' });
  const typeLabel = $derived(TYPE_LABEL[event.event_type] ?? event.event_type);
  const isDay = $derived(event.time_precision === 'day');
  const amountText = $derived(
    event.amount_cents ? `${event.detail?.type === 'income' ? '+' : '−'}¥${fmtAmount(event.amount_cents)}` : ''
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
      {fmtTime(event.occurred_at)}
    {/if}
  </span>

  <div class="min-w-0 flex-1">
    <div class="flex items-baseline gap-2">
      <span class="truncate font-medium text-zinc-900 dark:text-zinc-100">{event.title}</span>
      {#if amountText}
        <span class="shrink-0 text-sm font-semibold tabular-nums {event.detail?.type === 'income' ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400'}">
          {amountText}
        </span>
      {/if}
      {#if event.location_name}
        <span class="shrink-0 truncate text-xs text-zinc-500 dark:text-zinc-400">📍 {event.location_name}</span>
      {/if}
    </div>
    {#if event.summary && event.summary !== event.title}
      <div class="truncate text-xs text-zinc-500 dark:text-zinc-400">{event.summary}</div>
    {/if}
  </div>

  <span class="shrink-0 text-[11px] {meta.text}">{typeLabel} · {meta.label}</span>
</div>
