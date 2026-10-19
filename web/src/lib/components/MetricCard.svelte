<script lang="ts">
  // 指标卡：标签 / 大数字 / 一行环比。数字用等宽字形 + 26px，
  // 四张卡并排时纵向能对齐，扫一眼就能比大小。
  import Icon from './Icon.svelte';

  let {
    label,
    value,
    unit = '',
    delta = '',
    tone = 'muted',
    dir = 'flat',
    dot = '',
    hint = ''
  }: {
    label: string;
    value: string;
    unit?: string;
    delta?: string;
    tone?: 'pos' | 'neg' | 'muted';
    dir?: 'up' | 'down' | 'flat';
    dot?: string;
    hint?: string;
  } = $props();

  const toneClass = $derived(
    tone === 'pos' ? 'text-pos' : tone === 'neg' ? 'text-neg' : 'text-ink-3'
  );
</script>

<div
  class="flex flex-col gap-2.5 rounded-xl border border-line bg-surface p-4 transition-colors hover:border-line-strong"
>
  <div class="flex items-center justify-between gap-2">
    <span class="truncate text-[12px] text-ink-2">{label}</span>
    {#if dot}
      <span class="h-1.5 w-1.5 shrink-0 rounded-full" style:background={dot}></span>
    {/if}
  </div>

  <div class="flex items-baseline gap-1">
    <span class="tnum text-[26px] leading-none font-semibold tracking-[-0.01em]">{value}</span>
    {#if unit}<span class="text-[12px] text-ink-3">{unit}</span>{/if}
  </div>

  {#if delta}
    <div class="flex items-center gap-1 text-[12px] {toneClass}">
      <Icon name={dir} size={12} />
      <span class="truncate">{delta}</span>
    </div>
  {:else if hint}
    <div class="truncate text-[12px] text-ink-3">{hint}</div>
  {/if}
</div>
