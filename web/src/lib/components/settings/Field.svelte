<script lang="ts">
  import type { Snippet } from 'svelte';

  // 表单一项：标签 + 控件 + 可选说明。控件本身用 `field` 工具类，
  // 由调用方决定是 input / select / textarea。
  let {
    label,
    hint = '',
    children,
    className = ''
  }: {
    label: string;
    hint?: string;
    children: Snippet;
    className?: string;
  } = $props();
</script>

<label class="block min-w-0 {className}">
  <span class="mb-1 flex flex-wrap items-baseline gap-x-1.5 fs-11 leading-tight">
    <span class="font-medium text-ink-2">{label}</span>
    {#if hint}<span class="text-ink-3">{hint}</span>{/if}
  </span>
  {@render children()}
</label>
