<script lang="ts">
  // 环图：按来源统计事件条数占比。用 stroke-dasharray 分段，不用 path 弧线——
  // 半径一致、段间无缝隙，改尺寸只需要改 r。
  import { fmtCount } from '../api.ts';

  let {
    items,
    total,
    unit = '条记录',
    size = 140
  }: {
    items: { key: string; label: string; value: number; color: string }[];
    total: number;
    unit?: string;
    size?: number;
  } = $props();

  const R = 52;
  const STROKE = 25;
  const C = 2 * Math.PI * R;

  const segments = $derived.by(() => {
    const sum = Math.max(total, 1);
    let acc = 0;
    return items.map((it) => {
      const len = (it.value / sum) * C;
      const seg = { ...it, len, offset: -acc };
      acc += len;
      return seg;
    });
  });
</script>

<div class="relative shrink-0" style:width="{size}px" style:height="{size}px">
  <svg width={size} height={size} viewBox="0 0 140 140" role="img" aria-label="数据源事件分布环图">
    <g transform="rotate(-90 70 70)">
      <circle cx="70" cy="70" r={R} fill="none" stroke="var(--gf-chip)" stroke-width={STROKE} />
      {#each segments as s (s.key)}
        <circle
          cx="70"
          cy="70"
          r={R}
          fill="none"
          stroke={s.color}
          stroke-width={STROKE}
          stroke-dasharray="{s.len.toFixed(2)} {(C - s.len).toFixed(2)}"
          stroke-dashoffset={s.offset.toFixed(2)}
        />
      {/each}
    </g>
  </svg>
  <div class="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
    <span class="tnum text-[19px] leading-none font-semibold">{fmtCount(total)}</span>
    <span class="mt-1 text-[10px] text-ink-3">{unit}</span>
  </div>
</div>
