<script lang="ts">
  import { monthGrid } from '../api.ts';
  import Icon from './Icon.svelte';

  let {
    month,
    counts,
    selected,
    minDate,
    maxDate,
    onpick,
    onShift
  }: {
    month: string;
    counts: Record<string, number>;
    selected: string;
    minDate: string;
    maxDate: string;
    onpick: (date: string) => void;
    onShift: (delta: number) => void;
  } = $props();

  const cells = $derived(monthGrid(month));
  const today = fmtToday();

  function fmtToday(): string {
    const d = new Date();
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
  }

  // 强度按「本月最大值」分档，而不是写死 0/2/5 条——
  // 数据一多，写死的档位会整月糊成同一块黑，热力图就失去了信息量。
  const maxCount = $derived(Math.max(1, ...Object.values(counts)));

  function levelStyle(n: number): { background: string; color: string } {
    if (!n) return { background: 'var(--gf-chip)', color: 'var(--gf-ink-3)' };
    const r = n / maxCount;
    // 档位越低越接近纸色；配字颜色跟着反相，深色模式下自动反过来。
    const mix = r <= 0.25 ? 12 : r <= 0.5 ? 30 : r <= 0.75 ? 50 : 68;
    return {
      background: `color-mix(in oklab, var(--gf-ink) ${mix}%, var(--gf-chip))`,
      color: mix >= 30 ? 'var(--gf-accent-ink)' : 'var(--gf-ink-2)'
    };
  }

  function cellClass(date: string | null): string {
    if (!date) return 'invisible';
    const isSelected = date === selected;
    const dim =
      date > today || (minDate && date < minDate) || (maxDate && date > maxDate)
        ? 'opacity-35'
        : '';
    const ring = isSelected ? 'ring-2 ring-ink ring-offset-2 ring-offset-surface' : '';
    return `flex aspect-square w-full items-center justify-center rounded-md fs-11 tabular-nums transition-colors ${dim} ${ring}`;
  }

  function onclick(date: string | null) {
    if (!date) return;
    if (date > today) return;
    if (minDate && date < minDate) return;
    onpick(date);
  }

  const [y, m] = $derived(month.split('-').map(Number));
  const monthTitle = $derived(`${y} 年 ${m} 月`);
  const total = $derived(Object.values(counts).reduce((a, b) => a + b, 0));
</script>

<div class="w-full rounded-xl border border-line bg-surface p-4">
  <div class="mb-3 flex flex-wrap items-center gap-x-2 gap-y-1.5">
    <button
      type="button"
      class="flex h-7 w-7 items-center justify-center rounded-md border border-line text-ink-2 transition-colors hover:border-line-strong hover:text-ink"
      onclick={() => onShift(-1)}
      aria-label="上个月"
    >
      <Icon name="chevron-left" size={14} />
    </button>
    <h2 class="tnum fs-14 font-semibold">{monthTitle}</h2>
    <button
      type="button"
      class="flex h-7 w-7 items-center justify-center rounded-md border border-line text-ink-2 transition-colors hover:border-line-strong hover:text-ink"
      onclick={() => onShift(1)}
      aria-label="下个月"
    >
      <Icon name="chevron-right" size={14} />
    </button>
    <span class="tnum ml-1 fs-11 text-ink-3">{total} 条</span>
    <span class="ml-auto flex items-center gap-1.5 fs-10 text-ink-3">
      少
      {#each [0, maxCount * 0.2, maxCount * 0.6, maxCount] as lv (lv)}
        <span class="h-2.5 w-2.5 rounded-[3px]" style:background={levelStyle(lv).background}></span>
      {/each}
      多
    </span>
  </div>
  <div class="grid grid-cols-7 gap-1.5">
    {#each ['一', '二', '三', '四', '五', '六', '日'] as w (w)}
      <div class="pb-1 text-center fs-10 text-ink-3">{w}</div>
    {/each}
    {#each cells as cell, i (i)}
      <button
        type="button"
        class={cellClass(cell)}
        style:background={cell ? levelStyle(counts[cell] ?? 0).background : undefined}
        style:color={cell ? levelStyle(counts[cell] ?? 0).color : undefined}
        disabled={!cell || cell > today || (minDate !== '' && cell < minDate)}
        onclick={() => onclick(cell)}
        title={cell ? `${cell} · ${counts[cell] ?? 0} 条` : ''}
      >
        {cell ? Number(cell.slice(8)) : ''}
      </button>
    {/each}
  </div>
</div>
