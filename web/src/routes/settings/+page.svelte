<script lang="ts">
  import { onMount } from 'svelte';
  import {
    AUTH_LABELS,
    fetchSources,
    fmtAgo,
    sourceLabel,
    type AuthKind,
    type SourceState
  } from '#lib/api.ts';
  import {
    fetchSettings,
    savePolicy,
    SECRET_SOURCE_LABELS,
    type EndpointView,
    type SettingsPayload
  } from '#lib/settings.ts';
  import TopBar from '#lib/components/TopBar.svelte';
  import NotifyEditor from '#lib/components/settings/NotifyEditor.svelte';
  import SourceEditor from '#lib/components/settings/SourceEditor.svelte';
  import { setTheme, ui, effectiveDark, THEME_LABELS, type Theme } from '#lib/ui.svelte.ts';

  let sources = $state<SourceState[]>([]);
  let conf = $state<SettingsPayload | null>(null);
  let error = $state('');
  let loading = $state(true);
  let editing = $state<string | null>(null);
  let policyBusy = $state(false);

  onMount(() => {
    void load();
  });

  async function load() {
    loading = true;
    error = '';
    try {
      const [s, c] = await Promise.all([fetchSources(), fetchSettings()]);
      sources = s;
      conf = c;
    } catch (e) {
      error = String(e);
    } finally {
      loading = false;
    }
  }

  // /api/v1/sources 给同步状态（上次同步/错误/间隔），/api/v1/settings 给配置，
  // 按源名合并——两边职责不同，前端不猜哪边更权威。
  function stateOf(name: string): SourceState | undefined {
    return sources.find((s) => s.name === name);
  }

  const sourceEndpoints = $derived((conf?.endpoints ?? []).filter((e) => e.kind === 'source'));
  const notifyEndpoints = $derived((conf?.endpoints ?? []).filter((e) => e.kind === 'notify'));
  const configuredCount = $derived(sources.filter((s) => s.configured).length);
  const failing = $derived(sources.filter((s) => s.configured && s.last_error));

  function replaceEndpoint(e: EndpointView) {
    if (!conf) return;
    conf = { ...conf, endpoints: conf.endpoints.map((x) => (x.name === e.name ? e : x)) };
    // 配置变了，同步状态里的 configured/interval 也要跟着刷新。
    void fetchSources()
      .then((s) => (sources = s))
      .catch(() => {});
  }

  async function togglePolicy() {
    if (!conf) return;
    policyBusy = true;
    error = '';
    const next = { notify_on_sync_failure: !conf.policy.notify_on_sync_failure };
    try {
      const p = await savePolicy(next);
      conf = { ...conf, policy: p };
    } catch (e) {
      error = String(e);
    } finally {
      policyBusy = false;
    }
  }

  function statusOf(name: string): { label: string; cls: string; dot: string } {
    const s = stateOf(name);
    if (!s?.configured) return { label: '未配置', cls: 'text-ink-3', dot: 'var(--gf-line-strong)' };
    if (s.last_error) return { label: '同步出错', cls: 'text-neg', dot: 'var(--gf-neg)' };
    if (s.enabled) return { label: '运行中', cls: 'text-pos', dot: 'var(--gf-pos)' };
    return { label: '已停用', cls: 'text-ink-3', dot: 'var(--gf-ink-3)' };
  }

  const fmtInterval = (secs: number): string =>
    secs > 0 ? `${Math.round(secs / 60)} 分钟` : '默认';

  const secretSourceLabel = $derived(SECRET_SOURCE_LABELS[conf?.secret_source ?? ''] ?? '未知');
</script>

<svelte:head>
  <title>观复 · 设置</title>
</svelte:head>

<TopBar current="设置" />

<div class="px-4 py-5 md:px-6 lg:px-8">
  {#if error}
    <div class="mb-4 rounded-lg bg-chip px-3 py-2 text-[12.5px] text-neg">{error}</div>
  {/if}

  <section class="mb-6 max-w-4xl">
    <h2 class="mb-2 text-[13px] font-semibold text-ink">外观</h2>
    <div class="flex flex-wrap items-center gap-3 rounded-xl border border-line bg-surface p-4">
      <div class="flex rounded-[10px] bg-chip p-[3px]" role="group" aria-label="主题">
        {#each ['system', 'light', 'dark'] as t (t)}
          <button
            type="button"
            class="rounded-[7px] px-3 py-1.5 text-[12.5px] transition-colors
              {ui.theme === t
              ? 'bg-surface font-medium text-ink shadow-[0_1px_3px_rgba(27,29,34,0.08)]'
              : 'text-ink-2 hover:text-ink'}"
            onclick={() => setTheme(t as Theme)}
            aria-pressed={ui.theme === t}
          >
            {THEME_LABELS[t as Theme]}
          </button>
        {/each}
      </div>
      <span class="text-[11px] text-ink-3">
        当前{effectiveDark() ? '深色' : '浅色'}{ui.theme === 'system' ? '（跟随系统）' : ''}
      </span>
    </div>
    <p class="mt-2 text-[11px] text-ink-3">
      偏好存在这台设备的浏览器里，不写进数据库；选「跟随系统」时窗口深浅色变化会即时跟上。
    </p>
  </section>

  <section class="mb-6 max-w-5xl">
    <div class="mb-2 flex items-baseline gap-2">
      <h2 class="text-[13px] font-semibold text-ink">数据源</h2>
      {#if !loading}
        <span class="tnum text-[11px] text-ink-3">
          已配置 {configuredCount}/{sourceEndpoints.length}
        </span>
      {/if}
    </div>

    <!-- 优先级说明放在表格正上方：用户改配置前先看到规则，不用去翻文档。 -->
    <div
      class="mb-2.5 rounded-xl border border-line bg-surface-2 p-3 text-[11.5px] leading-relaxed text-ink-2"
    >
      <p class="font-medium text-ink">配置优先级：页面 &gt; 环境变量</p>
      <ul class="mt-1 list-disc space-y-0.5 pl-4">
        <li>
          在页面保存过的端点，<span class="text-ink">地址 / 认证方式 / 凭据 / 启用 / 间隔</span
          >全部以页面为准；页面里留空就是清空，不会回退到环境变量。
        </li>
        <li>没保存过的端点完全读环境变量，改 env 重启即生效。</li>
        <li>「恢复环境变量」= 删除该端点的页面配置。</li>
        <li>
          保存后立即生效：该源的适配器按新配置重建并重新调度，<span class="text-ink"
            >不需要重启进程</span
          >。
        </li>
      </ul>
      <p class="mt-1.5 text-ink-3">
        页面写入的凭据以 AES-GCM 密文落库（密钥：{secretSourceLabel}），接口只回末 4
        位；明文不会经过浏览器。
      </p>
    </div>

    {#if loading}
      <div class="py-10 text-center text-[12.5px] text-ink-3">加载中…</div>
    {:else if sourceEndpoints.length === 0}
      <div
        class="rounded-xl border border-line bg-surface py-10 text-center text-[12.5px] text-ink-3"
      >
        没有任何数据源。这是配置层的问题（env 源清单为空），检查启动参数。
      </div>
    {:else}
      <div class="overflow-x-auto rounded-xl border border-line bg-surface">
        <table class="w-full text-left text-[12px] md:min-w-[860px]">
          <thead class="border-b border-line text-[11px] text-ink-3">
            <tr>
              <th class="px-3 py-2.5 font-medium">源</th>
              <th class="px-3 py-2.5 font-medium">状态</th>
              <th class="hidden px-3 py-2.5 font-medium md:table-cell">端点</th>
              <th class="hidden px-3 py-2.5 font-medium lg:table-cell">认证</th>
              <th class="hidden px-3 py-2.5 font-medium lg:table-cell">凭据</th>
              <th class="hidden px-3 py-2.5 text-right font-medium md:table-cell">间隔</th>
              <th class="hidden px-3 py-2.5 text-right font-medium sm:table-cell">上次同步</th>
              <th class="px-3 py-2.5 text-right font-medium"></th>
            </tr>
          </thead>
          <tbody>
            {#each sourceEndpoints as e (e.name)}
              {@const st = statusOf(e.name)}
              {@const s = stateOf(e.name)}
              <tr class="border-t border-line">
                <td class="px-3 py-2">
                  <span class="font-medium text-ink">{sourceLabel(e.name)}</span>
                  <!-- 窄屏只留中文名 + 来源徽标：7 列在 390px 上会把「编辑」挤出屏幕。 -->
                  <span class="ml-1.5 hidden text-ink-3 sm:inline">{e.name}</span>
                  <span class="ml-1.5 tag">{e.origin === 'page' ? '页面' : 'env'}</span>
                </td>
                <td class="px-3 py-2 {st.cls}" title={s?.last_error || undefined}>
                  <span class="inline-flex items-center gap-1.5">
                    <span class="h-1.5 w-1.5 rounded-full" style:background={st.dot}></span>
                    {st.label}
                  </span>
                </td>
                <td
                  class="hidden max-w-[220px] truncate px-3 py-2 text-ink-3 md:table-cell"
                  title={e.base_url || ''}
                >
                  {e.base_url || '—'}
                </td>
                <td class="hidden px-3 py-2 text-ink-3 lg:table-cell">
                  {AUTH_LABELS[e.auth as AuthKind]}
                </td>
                <td class="hidden px-3 py-2 text-ink-3 lg:table-cell">
                  {#if e.auth === 'none'}
                    不需要
                  {:else if e.secret_set}
                    <span class="text-pos">{e.secret_hint}</span>
                  {:else}
                    <span class="text-ink-3">未设置</span>
                  {/if}
                </td>
                <td class="tnum hidden px-3 py-2 text-right text-ink-3 md:table-cell">
                  {fmtInterval(e.interval_secs || s?.interval_secs || 0)}
                </td>
                <td class="tnum hidden px-3 py-2 text-right text-ink-3 sm:table-cell">
                  {fmtAgo(s?.last_sync_at ?? 0)}
                </td>
                <td class="px-3 py-2 text-right">
                  <button
                    type="button"
                    class="btn"
                    aria-expanded={editing === e.name}
                    onclick={() => (editing = editing === e.name ? null : e.name)}
                  >
                    {editing === e.name ? '收起' : '编辑'}
                  </button>
                </td>
              </tr>
              {#if editing === e.name}
                <tr>
                  <td colspan="8" class="p-0">
                    <SourceEditor
                      endpoint={e}
                      authModes={conf?.auth_modes ?? []}
                      onsaved={replaceEndpoint}
                      onclose={() => (editing = null)}
                    />
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>

      {#if failing.length > 0}
        <details
          class="mt-2 rounded-xl border border-line bg-surface px-3 py-2 text-[12px] text-ink-2"
        >
          <summary class="cursor-pointer text-[11px] text-ink-3 select-none">
            展开 {failing.length} 个源的最新错误
          </summary>
          <ul class="mt-2 space-y-1">
            {#each failing as s (s.name)}
              <li>
                <span class="font-medium text-ink">{sourceLabel(s.name)}</span>：<span
                  class="text-neg">{s.last_error}</span
                >
              </li>
            {/each}
          </ul>
        </details>
      {/if}
    {/if}
  </section>

  <section class="max-w-5xl">
    <div class="mb-2 flex items-baseline gap-2">
      <h2 class="text-[13px] font-semibold text-ink">通知渠道</h2>
      <span class="text-[11px] text-ink-3">同步失败、手动测试都会走这里</span>
    </div>

    <div class="grid grid-cols-1 gap-3 xl:grid-cols-2">
      {#each notifyEndpoints as e (e.name)}
        <NotifyEditor endpoint={e} authModes={conf?.auth_modes ?? []} onsaved={replaceEndpoint} />
      {/each}
    </div>

    <div
      class="mt-3 flex flex-wrap items-center gap-3 rounded-xl border border-line bg-surface p-3"
    >
      <button
        type="button"
        role="switch"
        aria-checked={conf?.policy.notify_on_sync_failure ?? false}
        aria-label="同步失败时推送"
        class="relative h-5 w-9 shrink-0 rounded-full transition-colors
          {conf?.policy.notify_on_sync_failure ? 'bg-accent' : 'bg-line-strong'}"
        onclick={togglePolicy}
        disabled={policyBusy}
      >
        <span
          class="absolute top-0.5 h-4 w-4 rounded-full bg-surface transition-[left]
            {conf?.policy.notify_on_sync_failure ? 'left-[18px]' : 'left-0.5'}"
        ></span>
      </button>
      <div class="min-w-0 flex-1">
        <p class="text-[12.5px] font-medium text-ink">同步失败时推送</p>
        <p class="text-[11px] leading-relaxed text-ink-3">
          同一数据源由正常转为失败时发一条；同一源 6 小时内不重复。默认关闭——通知是出站请求，
          不能因为配了渠道就自动开始发。
        </p>
      </div>
    </div>

    <p class="mt-2 text-[11px] leading-relaxed text-ink-3">
      说明：本应用没有内置登录。设置页可以写入凭据，请只在受信网络内访问；
      要放到公网，请在前置反向代理上加认证。凭据加密密钥若换了（改
      <code class="rounded bg-chip px-1">GUANFU_SECRET_KEY</code> 或删除密钥文件），旧密文会解不开， 页面会明确报错，重新填一次即可。
    </p>
  </section>
</div>
