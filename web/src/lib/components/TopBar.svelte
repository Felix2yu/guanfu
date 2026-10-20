<script lang="ts">
  // 顶栏：左侧面包屑定位「现在在哪」，右侧由调用方塞操作（搜索 / 同步 / 导出 / 日期切换）。
  // 抽出来是为了让总览与三个数据视图的顶栏高度、留白、粘顶行为完全一致。
  import type { Snippet } from 'svelte';

  let { current, children }: { current: string; children?: Snippet } = $props();
</script>

<header
  class="sticky top-0 z-20 flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-line bg-canvas/90 px-4 py-2.5 backdrop-blur md:px-6 lg:px-8"
>
  <div class="flex shrink-0 items-center gap-1.5 fs-13">
    <span class="text-ink-3">观复</span>
    <span class="text-line-strong">/</span>
    <span class="font-medium text-ink">{current}</span>
  </div>
  <div class="ml-auto flex min-w-0 flex-wrap items-center justify-end gap-2">
    {@render children?.()}
  </div>
</header>
