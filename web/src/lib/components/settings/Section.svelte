<script lang="ts">
  import type { Snippet } from 'svelte';

  // 设置页一节 = 标题行（左标题、右操作）+ 一张卡片。
  // 卡片只保留一层边框，项与项靠分隔线区分：每项各套一张卡，
  // 同一页就会出现七八种高度和两种内边距。
  let {
    title,
    meta = '',
    children,
    actions = undefined,
    foot = undefined
  }: {
    title: string;
    meta?: string;
    children: Snippet;
    actions?: Snippet;
    foot?: Snippet;
  } = $props();
</script>

<section class="mb-6">
  <div class="mb-2 flex flex-wrap items-baseline gap-x-2 gap-y-1">
    <h2 class="fs-13 font-semibold text-ink">{title}</h2>
    {#if meta}<span class="tnum fs-11 text-ink-3">{meta}</span>{/if}
    {#if actions}
      <div class="ml-auto flex items-center gap-2">{@render actions()}</div>
    {/if}
  </div>

  <div class="divide-y divide-line overflow-hidden rounded-xl border border-line bg-surface">
    {@render children()}
  </div>

  {#if foot}
    <div class="mt-2 fs-11 leading-relaxed text-ink-3">{@render foot()}</div>
  {/if}
</section>
