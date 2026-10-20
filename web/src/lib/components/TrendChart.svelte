<script lang="ts">
  // 收支趋势图。收入与支出分成上下两个面板、各用各的量程：
  // 单笔收入动辄四位数、日常支出是两位数，共用一根 Y 轴会把支出整条压成一条贴底的直线。
  //
  // 宽度用 bind:clientWidth 实测而不是 viewBox 缩放 —— 缩放会把 10px 的轴标签
  // 一起放大缩小，窄屏上糊成一团。
  import { fmtMD, fmtMoney, fmtMoneyCompact, type StatRow } from '../api.ts';

  let { days, currency = 'CNY' }: { days: StatRow[]; currency?: string } = $props();

  const PAD_L = 50;
  const PAD_R = 8;
  const PAD_T = 10;
  const PANEL_H = 78;
  const GAP = 32;
  const AXIS_H = 20;
  const H = PAD_T + PANEL_H * 2 + GAP + AXIS_H;

  let w = $state(0);

  const n = $derived(days.length);

  const income = $derived(days.map((d) => d.totals[currency]?.income_cents ?? 0));
  const expense = $derived(days.map((d) => d.totals[currency]?.expense_cents ?? 0));

  /** 量程取整到 1/2/5×10ⁿ，轴标签才是人能读的数字。 */
  function niceMax(v: number): number {
    if (v <= 0) return 1;
    const exp = Math.pow(10, Math.floor(Math.log10(v)));
    const f = v / exp;
    const nf = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
    return nf * exp;
  }

  const plotW = $derived(Math.max(w - PAD_L - PAD_R, 10));

  function xAt(i: number): number {
    return n <= 1 ? PAD_L + plotW / 2 : PAD_L + (i / (n - 1)) * plotW;
  }

  function yAt(v: number, max: number, top: number): number {
    const bottom = top + PANEL_H;
    // 顶部留 6px 余量，峰值不会顶到面板边缘。
    return bottom - (v / max) * (PANEL_H - 6);
  }

  /** Catmull-Rom → 三次贝塞尔。控制点纵向夹紧，避免过冲画出面板外。 */
  function smoothPath(pts: { x: number; y: number }[], top: number): string {
    if (pts.length === 0) return '';
    if (pts.length === 1) return `M${pts[0].x},${pts[0].y}`;
    const lo = top;
    const hi = top + PANEL_H;
    const clamp = (v: number) => Math.min(Math.max(v, lo), hi);
    let d = `M${pts[0].x},${pts[0].y}`;
    for (let i = 0; i < pts.length - 1; i++) {
      const p0 = pts[i - 1] ?? pts[i];
      const p1 = pts[i];
      const p2 = pts[i + 1];
      const p3 = pts[i + 2] ?? p2;
      const c1x = p1.x + (p2.x - p0.x) / 6;
      const c1y = clamp(p1.y + (p2.y - p0.y) / 6);
      const c2x = p2.x - (p3.x - p1.x) / 6;
      const c2y = clamp(p2.y - (p3.y - p1.y) / 6);
      d += `C${c1x.toFixed(1)},${c1y.toFixed(1)} ${c2x.toFixed(1)},${c2y.toFixed(1)} ${p2.x.toFixed(1)},${p2.y.toFixed(1)}`;
    }
    return d;
  }

  const incomePanel = $derived.by(() => {
    const top = PAD_T;
    const max = niceMax(Math.max(...income, 1));
    const pts = income.map((v, i) => ({ x: xAt(i), y: yAt(v, max, top) }));
    const base = top + PANEL_H;
    return {
      top,
      max,
      line: smoothPath(pts, top),
      area: `${smoothPath(pts, top)}L${xAt(n - 1).toFixed(1)},${base}L${xAt(0).toFixed(1)},${base}Z`,
      peak: pts[income.indexOf(Math.max(...income))] ?? pts[0],
      peakValue: Math.max(...income)
    };
  });

  const expensePanel = $derived.by(() => {
    const top = PAD_T + PANEL_H + GAP;
    const max = niceMax(Math.max(...expense, 1));
    const pts = expense.map((v, i) => ({ x: xAt(i), y: yAt(v, max, top) }));
    const base = top + PANEL_H;
    return {
      top,
      max,
      line: smoothPath(pts, top),
      area: `${smoothPath(pts, top)}L${xAt(n - 1).toFixed(1)},${base}L${xAt(0).toFixed(1)},${base}Z`,
      peak: pts[expense.indexOf(Math.max(...expense))] ?? pts[0],
      peakValue: Math.max(...expense)
    };
  });

  /** X 轴最多 6 个刻度；末位始终显示并右对齐，免得被右缘裁掉。 */
  const xTicks = $derived.by(() => {
    if (n === 0) return [];
    const step = Math.max(1, Math.ceil(n / 6));
    const out: { x: number; label: string; anchor: string }[] = [];
    for (let i = 0; i < n; i += step) {
      out.push({ x: xAt(i), label: fmtMD(days[i].key), anchor: i === 0 ? 'start' : 'middle' });
    }
    const last = n - 1;
    const lastLabel = fmtMD(days[last].key);
    if (out[out.length - 1]?.label !== lastLabel) {
      // 窄屏上最后一个刻度可能贴着前一个，48px 以内就丢掉前一个，别让日期叠字。
      if (out.length > 1 && xAt(last) - out[out.length - 1].x < 48) out.pop();
      out.push({ x: xAt(last), label: lastLabel, anchor: 'end' });
    }
    return out;
  });

  const maxIncome = $derived(Math.max(...income, 0));
  const maxExpense = $derived(Math.max(...expense, 0));

  // 单日 / 双日没有曲线可画，退化成两根横向条形，信息反而更清楚。
  const bars = $derived.by(() => {
    const sums = {
      income: income.reduce((a, b) => a + b, 0),
      expense: expense.reduce((a, b) => a + b, 0)
    };
    const peak = Math.max(sums.income, sums.expense, 1);
    return [
      {
        label: '收入',
        value: sums.income,
        pct: (sums.income / peak) * 100,
        color: 'var(--gf-pos)'
      },
      {
        label: '支出',
        value: sums.expense,
        pct: (sums.expense / peak) * 100,
        color: 'var(--gf-neg)'
      }
    ];
  });
</script>

{#if n === 0}
  <div class="flex h-[214px] items-center justify-center fs-12 text-ink-3">该区间没有数据</div>
{:else if n <= 2}
  <div class="flex h-[214px] flex-col justify-center gap-5 px-1">
    {#each bars as b (b.label)}
      <div class="flex items-center gap-3">
        <span class="w-8 shrink-0 fs-11 text-ink-3">{b.label}</span>
        <div class="h-2 min-w-0 flex-1 overflow-hidden rounded-full bg-chip">
          <div class="h-full rounded-full" style:width="{b.pct}%" style:background={b.color}></div>
        </div>
        <span class="tnum w-24 shrink-0 text-right fs-12 text-ink">{fmtMoney(b.value)}</span>
      </div>
    {/each}
    <p class="fs-11 text-ink-3">
      区间只有一个自然日，趋势图退化为当日收支对比；切换到「近 30 天」可看曲线。
    </p>
  </div>
{:else}
  <div class="w-full" bind:clientWidth={w}>
    {#if w > 0}
      <svg width={w} height={H} role="img" aria-label="收支趋势图">
        <!-- 收入面板 -->
        <text x={PAD_L} y={incomePanel.top + 9} class="fs-10" fill="var(--gf-ink-3)">收入</text>
        {#each [0, 0.5, 1] as t (t)}
          {@const y = yAt(incomePanel.max * t, incomePanel.max, incomePanel.top)}
          <line x1={PAD_L} y1={y} x2={w - PAD_R} y2={y} stroke="var(--gf-line)" stroke-width="1" />
          <text x={PAD_L - 8} y={y + 3.5} text-anchor="end" class="fs-95" fill="var(--gf-ink-3)">
            {fmtMoneyCompact(incomePanel.max * t)}
          </text>
        {/each}
        <path d={incomePanel.area} fill="var(--gf-pos)" fill-opacity="0.12" />
        <path
          d={incomePanel.line}
          fill="none"
          stroke="var(--gf-pos)"
          stroke-width="1.8"
          stroke-linecap="round"
        />
        {#if maxIncome > 0}
          <circle cx={incomePanel.peak.x} cy={incomePanel.peak.y} r="2.6" fill="var(--gf-pos)" />
        {/if}
        <text
          x={w - PAD_R}
          y={incomePanel.top + 9}
          text-anchor="end"
          class="fs-95"
          fill="var(--gf-ink-3)"
        >
          峰值 {fmtMoneyCompact(maxIncome)}
        </text>

        <!-- 支出面板 -->
        <text x={PAD_L} y={expensePanel.top + 9} class="fs-10" fill="var(--gf-ink-3)">支出</text>
        {#each [0, 0.5, 1] as t (t)}
          {@const y = yAt(expensePanel.max * t, expensePanel.max, expensePanel.top)}
          <line x1={PAD_L} y1={y} x2={w - PAD_R} y2={y} stroke="var(--gf-line)" stroke-width="1" />
          <text x={PAD_L - 8} y={y + 3.5} text-anchor="end" class="fs-95" fill="var(--gf-ink-3)">
            {fmtMoneyCompact(expensePanel.max * t)}
          </text>
        {/each}
        <path d={expensePanel.area} fill="var(--gf-neg)" fill-opacity="0.1" />
        <path
          d={expensePanel.line}
          fill="none"
          stroke="var(--gf-neg)"
          stroke-width="1.8"
          stroke-linecap="round"
        />
        {#if maxExpense > 0}
          <circle cx={expensePanel.peak.x} cy={expensePanel.peak.y} r="2.6" fill="var(--gf-neg)" />
        {/if}
        <text
          x={w - PAD_R}
          y={expensePanel.top + 9}
          text-anchor="end"
          class="fs-95"
          fill="var(--gf-ink-3)"
        >
          峰值 {fmtMoneyCompact(maxExpense)}
        </text>

        <!-- X 轴刻度 -->
        {#each xTicks as tick (tick.x + tick.label)}
          <text x={tick.x} y={H - 4} text-anchor={tick.anchor} class="fs-95" fill="var(--gf-ink-3)"
            >{tick.label}</text
          >
        {/each}
      </svg>
    {/if}
  </div>
{/if}
