<script lang="ts">
  // 顶栏全库搜索：输入防抖后走 /api/v1/events?q=（标题/摘要子串，最近在前），
  // 点结果跳到那天（事件本身在当日时间轴上看）。仅桌面/平板显示（手机空间不够）。
  import { searchEvents, sourceLabel, TYPE_LABEL, type Event } from '../api.ts';
  import Icon from './Icon.svelte';

  let { onpick }: { onpick: (date: string) => void } = $props();

  let q = $state('');
  let open = $state(false);
  let results = $state<Event[]>([]);
  let loading = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  function onInput(e: { currentTarget: HTMLInputElement }) {
    const value = e.currentTarget.value.trim();
    q = value;
    if (timer) clearTimeout(timer);
    if (value.length < 2) {
      results = [];
      loading = false;
      open = false;
      return;
    }
    loading = true;
    open = true;
    timer = setTimeout(async () => {
      try {
        const list = await searchEvents(value, 20);
        if (value === q) results = list; // 防抖窗口内又输入了就丢弃这批结果
      } catch {
        if (value === q) results = [];
      } finally {
        if (value === q) loading = false;
      }
    }, 300);
  }

  function pick(ev: Event) {
    open = false;
    onpick(ev.local_date);
  }
</script>

<div class="relative hidden md:block">
  <div
    class="flex h-9 items-center gap-1.5 rounded-[9px] border border-line bg-surface px-2.5 transition-colors focus-within:border-line-strong"
  >
    <Icon name="search" size={14} cls="shrink-0 text-ink-3" />
    <input
      class="w-28 bg-transparent fs-125 text-ink outline-none placeholder:text-ink-3 lg:w-36"
      placeholder="搜索事件"
      value={q}
      oninput={onInput}
      onfocus={() => {
        if (q.length >= 2) open = true;
      }}
      onblur={() => (open = false)}
      onkeydown={(e) => {
        if (e.key === 'Escape') open = false;
      }}
      aria-label="搜索事件（标题与摘要）"
    />
    {#if loading}<span class="fs-11 text-ink-3">…</span>{/if}
  </div>

  {#if open}
    <div
      class="absolute right-0 z-30 mt-1 w-96 max-w-[calc(100vw-2rem)] overflow-hidden rounded-lg border border-line bg-surface shadow-lg"
    >
      {#if loading && results.length === 0}
        <div class="px-3 py-4 text-center fs-125 text-ink-3">搜索中…</div>
      {:else if results.length === 0}
        <div class="px-3 py-4 text-center fs-125 text-ink-3">没有匹配「{q}」的事件</div>
      {:else}
        <ul class="max-h-80 overflow-y-auto py-1">
          {#each results as ev (ev.id)}
            <li>
              <!-- onmousedown preventDefault：让 blur 晚于 click，面板关闭不会吃掉点击。 -->
              <button
                type="button"
                class="flex w-full items-baseline gap-2 px-3 py-1.5 text-left transition-colors hover:bg-chip"
                onmousedown={(e) => e.preventDefault()}
                onclick={() => pick(ev)}
              >
                <span class="tnum shrink-0 whitespace-nowrap fs-11 text-ink-3">
                  {ev.local_date.slice(5)}{ev.local_time ? ` ${ev.local_time}` : ''}
                </span>
                <span class="min-w-0 flex-1 truncate fs-125 text-ink"
                  >{ev.title || TYPE_LABEL[ev.event_type] || ev.event_type}</span
                >
                <span class="shrink-0 fs-11 text-ink-3">{sourceLabel(ev.source)}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</div>
