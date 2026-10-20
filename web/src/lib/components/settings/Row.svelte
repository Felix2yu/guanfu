<script lang="ts">
  import type { Snippet } from 'svelte';

  // 一行一项：左侧名称 + 一行说明，右侧控件。min-h 固定住行高，
  // 说明缺省时这一行也不会比邻居矮一截。
  let {
    title,
    desc = '',
    children = undefined
  }: {
    title: string;
    desc?: string;
    children?: Snippet;
  } = $props();
</script>

<div class="flex min-h-[46px] flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5">
  <div class="min-w-0 flex-1">
    <p class="fs-125 font-medium text-ink">{title}</p>
    {#if desc}<p class="mt-0.5 fs-11 leading-relaxed text-ink-3">{desc}</p>{/if}
  </div>
  {#if children}
    <div class="flex shrink-0 items-center gap-2">{@render children()}</div>
  {/if}
</div>
