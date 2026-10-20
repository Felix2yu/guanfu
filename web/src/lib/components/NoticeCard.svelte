<script lang="ts">
  // 通知卡：从同步状态推导出来的提醒，点一条即标记已读（存本机）。
  import { fmtAgo } from '../api.ts';
  import type { Notice } from '../notices.ts';

  let {
    notices,
    read,
    onRead,
    onReadAll,
    emptyText = '还没有同步记录，先跑一次同步'
  }: {
    notices: Notice[];
    read: Record<string, boolean>;
    onRead: (id: string) => void;
    onReadAll: () => void;
    emptyText?: string;
  } = $props();

  const unread = $derived(notices.filter((n) => !read[n.id]).length);
</script>

<div class="flex flex-col rounded-xl border border-line bg-surface">
  <div class="flex items-center gap-2 border-b border-line px-4 py-3">
    <h2 class="fs-14 font-semibold">通知</h2>
    {#if unread > 0}
      <span class="tnum rounded-md bg-chip px-1.5 py-0.5 fs-11 text-ink-2">{unread} 未读</span>
    {/if}
    <button
      type="button"
      class="ml-auto rounded-md px-1.5 py-1 fs-11 text-ink-3 transition-colors hover:text-ink disabled:opacity-40"
      onclick={onReadAll}
      disabled={unread === 0}
    >
      全部标为已读
    </button>
  </div>

  <div class="flex flex-col p-1.5">
    {#if notices.length === 0}
      <p class="px-2.5 py-6 text-center fs-12 text-ink-3">{emptyText}</p>
    {:else}
      {#each notices as n (n.id)}
        {@const isRead = read[n.id]}
        <button
          type="button"
          class="flex items-start gap-2.5 rounded-lg px-2.5 py-2 text-left transition-colors hover:bg-chip"
          onclick={() => onRead(n.id)}
          title={isRead ? '已读' : '点击标记已读'}
        >
          <span
            class="mt-[5px] h-1.5 w-1.5 shrink-0 rounded-full"
            style:background={isRead
              ? 'var(--gf-line-strong)'
              : n.tone === 'error'
                ? 'var(--gf-neg)'
                : n.tone === 'ok'
                  ? 'var(--gf-pos)'
                  : 'var(--gf-ink-3)'}
          ></span>
          <span class="min-w-0 flex-1">
            <span
              class="block fs-125 leading-snug {isRead
                ? 'text-ink-3'
                : n.tone === 'error'
                  ? 'text-neg'
                  : 'text-ink'}"
            >
              {n.text}
            </span>
            {#if n.at}
              <span class="mt-0.5 block fs-11 text-ink-3">{fmtAgo(n.at)}</span>
            {/if}
          </span>
        </button>
      {/each}
    {/if}
  </div>

  <p class="border-t border-line px-4 py-2 fs-11 text-ink-3">由同步状态推导 · 已读只记在这台设备</p>
</div>
