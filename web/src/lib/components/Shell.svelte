<script lang="ts">
  // 应用外壳：桌面左侧完整导航（≥1024）、平板图标栏（768–1023）、手机底部标签栏（<768）。
  // 三档用的是同一份导航定义，不各写一套，避免改一处漏两处。
  import { goto } from '$app/navigation';
  import { resolve } from '$app/paths';
  import { page } from '$app/state';
  import type { IconName } from '../icons.ts';
  import { setView, ui, VIEW_LABELS, VIEW_ORDER, type ViewMode } from '../ui.svelte.ts';
  import Icon from './Icon.svelte';

  let { children } = $props();

  const ICONS: Record<ViewMode, IconName> = {
    overview: 'overview',
    day: 'day',
    calendar: 'calendar',
    timeline: 'timeline'
  };

  const onSettings = $derived(page.url.pathname.startsWith('/settings'));

  function go(v: ViewMode) {
    setView(v);
    // 从设置页点导航要回到主页面，只改 view 不会换路由。
    if (onSettings) void goto(resolve('/'));
  }
</script>

<div class="min-h-dvh bg-canvas text-ink">
  <div class="flex min-h-dvh">
    <!-- 侧栏 -->
    <aside
      class="sticky top-0 hidden h-dvh shrink-0 flex-col border-r border-line bg-surface-2 md:flex md:w-[72px] lg:w-[232px]"
    >
      <div class="flex items-center gap-2.5 px-4 py-5 lg:px-5">
        <span
          class="flex h-8 w-8 shrink-0 items-center justify-center rounded-[9px] bg-accent text-accent-ink"
        >
          <svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true">
            <circle cx="10" cy="10" r="5.6" stroke="currentColor" stroke-width="1.8" />
            <circle cx="10" cy="10" r="1.9" fill="currentColor" />
          </svg>
        </span>
        <span class="hidden fs-15 font-semibold tracking-[0.02em] lg:inline">观复</span>
      </div>

      <nav class="flex flex-col gap-1 px-3" aria-label="主导航">
        {#each VIEW_ORDER as v (v)}
          {@const active = !onSettings && ui.view === v}
          <button
            type="button"
            class="flex items-center gap-2.5 rounded-[10px] px-2.5 py-2 fs-13 transition-colors
              {active
              ? 'bg-accent font-medium text-accent-ink'
              : 'text-ink-2 hover:bg-chip hover:text-ink'}"
            onclick={() => go(v)}
            aria-current={active ? 'page' : undefined}
            title={VIEW_LABELS[v]}
          >
            <span class="flex w-5 shrink-0 justify-center"><Icon name={ICONS[v]} size={20} /></span>
            <span class="hidden truncate lg:inline">{VIEW_LABELS[v]}</span>
          </button>
        {/each}
      </nav>

      <div class="mt-auto flex flex-col gap-2 px-3 pb-4">
        <a
          href={resolve('/settings')}
          class="flex items-center gap-2.5 rounded-[10px] px-2.5 py-2 fs-13 transition-colors
            {onSettings
            ? 'bg-accent font-medium text-accent-ink'
            : 'text-ink-2 hover:bg-chip hover:text-ink'}"
          aria-current={onSettings ? 'page' : undefined}
          title="设置"
        >
          <span class="flex w-5 shrink-0 justify-center"><Icon name="settings" size={20} /></span>
          <span class="hidden truncate lg:inline">设置</span>
        </a>
        <p class="hidden px-2.5 fs-11 leading-relaxed text-ink-3 lg:block">自托管 · 数据留在本机</p>
      </div>
    </aside>

    <main class="min-w-0 flex-1 pb-16 md:pb-0">
      {@render children()}
    </main>
  </div>

  <!-- 手机底部标签栏 -->
  <nav
    class="fixed inset-x-0 bottom-0 z-30 flex border-t border-line bg-surface/95 backdrop-blur md:hidden"
    aria-label="主导航"
  >
    {#each VIEW_ORDER as v (v)}
      {@const active = !onSettings && ui.view === v}
      <button
        type="button"
        class="flex flex-1 flex-col items-center gap-0.5 py-2 fs-10
          {active ? 'font-medium text-ink' : 'text-ink-3'}"
        onclick={() => go(v)}
        aria-current={active ? 'page' : undefined}
      >
        <Icon name={ICONS[v]} size={20} />
        {VIEW_LABELS[v]}
      </button>
    {/each}
    <a
      href={resolve('/settings')}
      class="flex flex-1 flex-col items-center gap-0.5 py-2 fs-10
        {onSettings ? 'font-medium text-ink' : 'text-ink-3'}"
      aria-current={onSettings ? 'page' : undefined}
    >
      <Icon name="settings" size={20} />
      设置
    </a>
  </nav>
</div>
