<script lang="ts">
  import { onMount } from 'svelte';
  import { fetchSources, AUTH_LABELS, type SourceState } from '#lib/api.ts';
  import { initUI, disposeUI, setTheme, ui, effectiveDark, THEME_LABELS, type Theme } from '#lib/ui.svelte.ts';

  let sources = $state<SourceState[]>([]);
  let error = $state('');
  let loading = $state(true);

  onMount(() => {
    initUI();
    fetchSources()
      .then((s) => (sources = s))
      .catch((e) => (error = String(e)))
      .finally(() => (loading = false));
    return disposeUI;
  });

  const fmtInterval = (secs: number): string => (secs > 0 ? `${Math.round(secs / 60)} 分钟` : '默认');

  const fmtTime = (unix: number): string =>
    unix ? new Date(unix * 1000).toLocaleString('zh-CN', { hour12: false }) : '从未';

  function statusOf(s: SourceState): { label: string; cls: string } {
    if (!s.configured) return { label: '未配置', cls: 'text-zinc-400 dark:text-zinc-500' };
    if (s.last_error) return { label: '同步出错', cls: 'text-rose-600 dark:text-rose-400' };
    if (s.enabled) return { label: '运行中', cls: 'text-emerald-600 dark:text-emerald-400' };
    return { label: '已停用', cls: 'text-zinc-400 dark:text-zinc-500' };
  }
</script>

<svelte:head>
  <title>观复 · 设置</title>
</svelte:head>

<div class="mx-auto max-w-3xl px-4 py-6">
  <header class="mb-5 flex items-center gap-3">
    <a class="btn" href="/">←</a>
    <h1 class="text-lg font-semibold">设置</h1>
  </header>

  {#if error}
    <div class="mb-4 rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">{error}</div>
  {/if}

  <section class="mb-8">
    <h2 class="mb-2 text-sm font-medium text-zinc-700 dark:text-zinc-300">外观</h2>
    <div class="flex items-center gap-3">
      <div class="flex rounded-lg border border-zinc-200 p-0.5 dark:border-zinc-700">
        {#each ['system', 'light', 'dark'] as t (t)}
          <button
            class="rounded-md px-3 py-1 text-sm transition-colors {ui.theme === t
              ? 'bg-zinc-900 font-medium text-white dark:bg-zinc-100 dark:text-zinc-900'
              : 'text-zinc-600 hover:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800'}"
            onclick={() => setTheme(t as Theme)}
            aria-pressed={ui.theme === t}
          >
            {THEME_LABELS[t as Theme]}
          </button>
        {/each}
      </div>
      <span class="text-xs text-zinc-400 dark:text-zinc-500">
        当前{effectiveDark() ? '深色' : '浅色'}{ui.theme === 'system' ? '（跟随系统）' : ''}
      </span>
    </div>
    <p class="mt-2 text-xs text-zinc-400 dark:text-zinc-500">
      偏好存在这台设备的浏览器里，不写进数据库；选「跟随系统」时窗口深浅色变化会即时跟上。
    </p>
  </section>

  <section>
    <h2 class="mb-2 text-sm font-medium text-zinc-700 dark:text-zinc-300">数据源</h2>
    {#if loading}
      <div class="py-8 text-center text-sm text-zinc-400">加载中…</div>
    {:else if sources.length === 0}
      <div class="py-8 text-center text-sm text-zinc-400">
        还没有任何源。在环境变量里填 <code class="rounded bg-zinc-100 px-1 dark:bg-zinc-800">&lt;源名&gt;_BASE_URL</code> 后重启即可。
      </div>
    {:else}
      <div class="overflow-x-auto rounded-xl border border-zinc-200 dark:border-zinc-700">
        <table class="w-full min-w-[680px] text-left text-xs">
          <thead class="bg-zinc-50 text-zinc-500 dark:bg-zinc-800/60 dark:text-zinc-400">
            <tr>
              <th class="px-3 py-2 font-medium">源</th>
              <th class="px-3 py-2 font-medium">状态</th>
              <th class="px-3 py-2 font-medium">端点</th>
              <th class="px-3 py-2 font-medium">认证</th>
              <th class="px-3 py-2 font-medium">凭据</th>
              <th class="px-3 py-2 text-right font-medium">间隔</th>
              <th class="px-3 py-2 text-right font-medium">上次同步</th>
            </tr>
          </thead>
          <tbody>
            {#each sources as s (s.name)}
              {@const st = statusOf(s)}
              <tr class="border-t border-zinc-100 dark:border-zinc-800">
                <td class="px-3 py-2 font-medium text-zinc-800 dark:text-zinc-200">{s.name}</td>
                <td class="px-3 py-2 {st.cls}" title={s.last_error || undefined}>
                  {st.label}{#if s.last_error}<span class="ml-1 text-zinc-400">· 见提示</span>{/if}
                </td>
                <td class="max-w-[220px] truncate px-3 py-2 tabular-nums text-zinc-500 dark:text-zinc-400" title={s.base_url || ''}>
                  {s.base_url || '—'}
                </td>
                <td class="px-3 py-2 text-zinc-500 dark:text-zinc-400">{AUTH_LABELS[s.auth ?? 'bearer']}</td>
                <td class="px-3 py-2 tabular-nums text-zinc-500 dark:text-zinc-400">
                  {#if s.auth === 'none'}
                    不需要
                  {:else if s.credential_set}
                    <span class="text-emerald-600 dark:text-emerald-400">已配置</span>
                    <span class="ml-1 text-zinc-400">{s.credential_env}</span>
                  {:else}
                    <span class="text-zinc-400 dark:text-zinc-500">未配置</span>
                    <span class="ml-1 text-zinc-400">{s.credential_env}</span>
                  {/if}
                </td>
                <td class="px-3 py-2 text-right tabular-nums text-zinc-500 dark:text-zinc-400">{fmtInterval(s.interval_secs)}</td>
                <td class="px-3 py-2 text-right tabular-nums text-zinc-500 dark:text-zinc-400">
                  {fmtTime(s.last_sync_at)}{#if s.last_count}<span class="ml-1 text-zinc-400">+{s.last_count}</span>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if sources.some((s) => s.last_error)}
        <details class="mt-2 text-xs text-zinc-500 dark:text-zinc-400">
          <summary class="cursor-pointer select-none">展开各源的最新错误</summary>
          <ul class="mt-1 space-y-1 pl-4">
            {#each sources.filter((s) => s.last_error) as s (s.name)}
              <li><span class="font-medium text-zinc-700 dark:text-zinc-300">{s.name}</span>：{s.last_error}</li>
            {/each}
          </ul>
        </details>
      {/if}
    {/if}

    <div class="mt-4 rounded-xl border border-zinc-200 bg-zinc-50 p-3 text-xs leading-relaxed text-zinc-600 dark:border-zinc-700 dark:bg-zinc-800/40 dark:text-zinc-400">
      <p class="mb-1 font-medium text-zinc-700 dark:text-zinc-300">端点与凭据在哪里改</p>
      <p>
        这一页只读。凭据一律走环境变量（<code class="rounded bg-white px-1 dark:bg-zinc-900">&lt;源名&gt;_BASE_URL</code> /
        <code class="rounded bg-white px-1 dark:bg-zinc-900">&lt;源名&gt;_API_KEY</code>），不写进数据库、不进镜像层——
        库里存一份密钥等于把全家桶的钥匙和抄本放在同一个抽屉里。改完 <code class="rounded bg-white px-1 dark:bg-zinc-900">.env</code>
        或 <code class="rounded bg-white px-1 dark:bg-zinc-900">docker run -e</code> 后重启容器生效。
      </p>
      <p class="mt-1">
        表里「未配置」指的是该源的端点或凭据没在环境里给到；「同步出错」的完整文案在上方「展开各源的最新错误」里。
      </p>
    </div>
  </section>
</div>

<style>
  .btn {
    border-radius: 0.5rem;
    border: 1px solid var(--color-zinc-200, #e4e4e7);
    padding: 0.25rem 0.625rem;
    font-size: 0.875rem;
    background: white;
    transition: border-color 0.15s;
  }
  .btn:hover {
    border-color: #a1a1aa;
  }
  :global(.dark) .btn {
    background: #27272a;
    border-color: #3f3f46;
  }
</style>
