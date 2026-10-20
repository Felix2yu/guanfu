<script lang="ts">
  // 数据源同步状态：一行一个源，点行即单独同步。
  // 「未配置」与「同步失败」要能一眼分开——前者是没接线，后者是线断了。
  import { fmtAgo, sourceLabel, type SourceState } from '../api.ts';
  import Icon from './Icon.svelte';

  let {
    sources,
    syncing = '',
    onSync
  }: {
    sources: SourceState[];
    syncing?: string;
    onSync: (name: string) => void;
  } = $props();

  function dotColor(s: SourceState): string {
    if (!s.configured) return 'var(--gf-line-strong)';
    if (s.last_error) return 'var(--gf-neg)';
    if (s.enabled) return 'var(--gf-pos)';
    return 'var(--gf-ink-3)';
  }

  function statusText(s: SourceState): string {
    if (!s.configured) return '未配置';
    if (s.last_error) return s.last_error.split('\n')[0];
    if (!s.enabled) return '已停用';
    return fmtAgo(s.last_sync_at);
  }

  const configured = $derived(sources.filter((s) => s.configured));
  const failing = $derived(configured.filter((s) => s.last_error).length);
</script>

<div class="flex flex-col rounded-xl border border-line bg-surface">
  <div class="flex items-center gap-2 border-b border-line px-4 py-3">
    <h2 class="fs-14 font-semibold">数据源</h2>
    <span class="tnum rounded-md bg-chip px-1.5 py-0.5 fs-11 text-ink-2">
      {configured.length}/{sources.length}
    </span>
    {#if failing > 0}
      <span class="tnum rounded-md bg-chip px-1.5 py-0.5 fs-11 text-neg">{failing} 个失败</span>
    {/if}
    <button
      type="button"
      class="ml-auto flex items-center gap-1 rounded-md border border-line px-2 py-1 fs-11 text-ink-2 transition-colors hover:border-line-strong hover:text-ink disabled:opacity-50"
      onclick={() => onSync('all')}
      disabled={syncing !== ''}
    >
      <Icon name="sync" size={12} cls={syncing ? 'animate-spin' : ''} />
      {syncing === 'all' ? '同步中' : '同步全部'}
    </button>
  </div>

  <div class="flex flex-col p-1.5">
    {#each sources as s (s.name)}
      <button
        type="button"
        class="flex items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-left transition-colors hover:bg-chip disabled:cursor-default disabled:opacity-60"
        onclick={() => s.configured && onSync(s.name)}
        disabled={!s.configured || syncing !== ''}
        title={s.last_error || (s.configured ? '点击单独同步' : '端点或凭据未配置')}
      >
        <span class="h-1.5 w-1.5 shrink-0 rounded-full" style:background={dotColor(s)}></span>
        <span class="min-w-0 flex-1 truncate fs-125 {s.configured ? 'text-ink' : 'text-ink-3'}">
          {sourceLabel(s.name)}
          <span class="text-ink-3">· {s.name}</span>
        </span>
        <span
          class="max-w-[52%] shrink-0 truncate fs-11 tabular-nums {s.last_error
            ? 'text-neg'
            : 'text-ink-3'}"
        >
          {syncing === s.name ? '同步中…' : statusText(s)}
        </span>
      </button>
    {/each}
  </div>
</div>
