<script lang="ts">
  // 今日打卡：来自慎始当天的 habit_check 事件（一天聚合成一条）。
  // 观复对源站只读，所以这里是「已打卡」清单而不是可勾选的待办——
  // 摆一个点了没反应的复选框，比不摆更糟。
  import { fmtWeekday } from '../api.ts';
  import Icon from './Icon.svelte';

  let {
    habits,
    date,
    loading = false,
    emptyText = '今天还没有打卡记录'
  }: {
    habits: { name: string; count: number }[];
    date: string;
    loading?: boolean;
    emptyText?: string;
  } = $props();
</script>

<div class="flex flex-col rounded-xl border border-line bg-surface">
  <div class="flex items-center gap-2 border-b border-line px-4 py-3">
    <h2 class="text-[14px] font-semibold">今日打卡</h2>
    {#if habits.length > 0}
      <span class="tnum rounded-md bg-chip px-1.5 py-0.5 text-[11px] text-ink-2">
        {habits.length} 项
      </span>
    {/if}
    <span class="ml-auto text-[11px] text-ink-3">{date} 周{fmtWeekday(date)}</span>
  </div>

  <div class="flex flex-1 flex-col gap-1 p-1.5">
    {#if loading}
      <p class="px-2.5 py-6 text-center text-[12px] text-ink-3">加载中…</p>
    {:else if habits.length === 0}
      <p class="px-2.5 py-6 text-center text-[12px] text-ink-3">{emptyText}</p>
    {:else}
      {#each habits as h (h.name)}
        <div class="flex items-center gap-2.5 rounded-lg px-2.5 py-1.5">
          <span
            class="flex h-[18px] w-[18px] shrink-0 items-center justify-center rounded-[5px] bg-accent text-accent-ink"
          >
            <Icon name="check" size={12} />
          </span>
          <span class="min-w-0 flex-1 truncate text-[12.5px]">{h.name}</span>
          {#if h.count > 1}
            <span class="tnum shrink-0 text-[11px] text-ink-3">×{h.count}</span>
          {/if}
        </div>
      {/each}
    {/if}
  </div>

  <p class="border-t border-line px-4 py-2 text-[11px] text-ink-3">来自慎始 · 观复只读展示</p>
</div>
