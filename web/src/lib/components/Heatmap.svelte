<script lang="ts">
  import { monthGrid } from '../api';

  let {
    month,
    counts,
    selected,
    minDate,
    maxDate,
    onpick
  }: {
    month: string;
    counts: Record<string, number>;
    selected: string;
    minDate: string;
    maxDate: string;
    onpick: (date: string) => void;
  } = $props();

  const cells = $derived(monthGrid(month));
  const today = fmtToday();

  function fmtToday(): string {
    const d = new Date();
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
  }

  // 强度分档（克制的中性色阶，浅色/深色分别定义）。
  function level(n: number): string {
    if (!n) return 'bg-zinc-100 dark:bg-zinc-800/70';
    if (n <= 2) return 'bg-zinc-200 dark:bg-zinc-700';
    if (n <= 5) return 'bg-zinc-300 dark:bg-zinc-600';
    return 'bg-zinc-400 dark:bg-zinc-500';
  }

  function cellClass(date: string | null): string {
    if (!date) return 'invisible';
    const n = counts[date] ?? 0;
    const isFuture = date > today;
    const outOfRange = (minDate && date < minDate) || (maxDate && date > maxDate);
    const isSelected = date === selected;
    let cls = `flex h-6 w-6 items-center justify-center rounded text-[10px] tabular-nums ${level(n)}`;
    if (isSelected) cls += ' ring-2 ring-rose-500 ring-offset-1 dark:ring-offset-zinc-900';
    if (isFuture || outOfRange) cls += ' opacity-30';
    return cls;
  }

  function onclick(date: string | null) {
    if (!date) return;
    if (date > today) return;
    if (minDate && date < minDate) return;
    onpick(date);
  }

  const [y, m] = $derived(month.split('-').map(Number));
  const monthTitle = $derived(`${y} 年 ${m} 月`);
</script>

<div class="rounded-xl border border-zinc-200 p-3 dark:border-zinc-700">
  <div class="mb-2 flex items-center justify-between text-xs text-zinc-500 dark:text-zinc-400">
    <span class="font-medium text-zinc-700 dark:text-zinc-300">{monthTitle}</span>
    <span class="tabular-nums">{Object.values(counts).reduce((a, b) => a + b, 0)} 条</span>
  </div>
  <div class="grid grid-cols-7 gap-1">
    {#each ['一', '二', '三', '四', '五', '六', '日'] as w (w)}
      <div class="text-center text-[10px] text-zinc-400 dark:text-zinc-500">{w}</div>
    {/each}
    {#each cells as cell, i (i)}
      <button
        class={cellClass(cell)}
        disabled={!cell || cell > today || (minDate !== '' && cell < minDate)}
        onclick={() => onclick(cell)}
        title={cell ? `${cell} · ${counts[cell] ?? 0} 条` : ''}
      >
        {cell ? Number(cell.slice(8)) : ''}
      </button>
    {/each}
  </div>
</div>
