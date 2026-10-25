<script lang="ts">
  import { onMount } from 'svelte';
  import {
    AUTH_LABELS,
    AUTH_SHORT_LABELS,
    fetchSources,
    fmtAgo,
    importEvents,
    sourceLabel,
    type AuthKind,
    type ImportResult,
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
  import Segmented from '#lib/components/Segmented.svelte';
  import Switch from '#lib/components/Switch.svelte';
  import NotifyEditor from '#lib/components/settings/NotifyEditor.svelte';
  import Row from '#lib/components/settings/Row.svelte';
  import Section from '#lib/components/settings/Section.svelte';
  import SourceEditor from '#lib/components/settings/SourceEditor.svelte';
  import {
    setTextSize,
    setTheme,
    ui,
    TEXT_SIZE_LABELS,
    TEXT_SIZE_ORDER,
    THEME_LABELS,
    type TextSize,
    type Theme
  } from '#lib/ui.svelte.ts';

  const THEME_OPTIONS = (['system', 'light', 'dark'] as Theme[]).map((t) => ({
    value: t,
    label: THEME_LABELS[t]
  }));
  const SIZE_OPTIONS = TEXT_SIZE_ORDER.map((s) => ({ value: s, label: TEXT_SIZE_LABELS[s] }));

  let sources = $state<SourceState[]>([]);
  let conf = $state<SettingsPayload | null>(null);
  let error = $state('');
  let loading = $state(true);
  // 数据源和通知渠道共用一个展开位：同时开两处，未保存的改动归谁就说不清了。
  let editing = $state<string | null>(null);
  let editorDirty = $state(false);
  let policyBusy = $state(false);
  // 开关自己持一份状态：保存失败时要能 true→false 走一次真实变更，
  // 直接把 checked 指回 conf 不会触发 DOM 写回（新旧值相同），开关会停在错的那边。
  let policyOn = $state(false);

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
      policyOn = c.policy.notify_on_sync_failure;
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

  function refreshSources() {
    void fetchSources()
      .then((s) => (sources = s))
      .catch(() => {});
  }

  function replaceEndpoint(e: EndpointView) {
    if (!conf) return;
    conf = { ...conf, endpoints: conf.endpoints.map((x) => (x.name === e.name ? e : x)) };
    // 配置变了，同步状态里的 configured/interval 也要跟着刷新。
    refreshSources();
  }

  function closeEditor() {
    if (editorDirty && !window.confirm('有未保存的修改，收起后将丢失。确定收起？')) return;
    editing = null;
    editorDirty = false;
  }

  function toggleEditor(name: string) {
    if (editing === name) {
      closeEditor();
      return;
    }
    if (
      editing &&
      editorDirty &&
      !window.confirm('当前编辑有未保存的修改，切换后将丢失。确定切换？')
    ) {
      return;
    }
    editing = name;
    editorDirty = false;
  }

  async function togglePolicy(on: boolean) {
    if (!conf) return;
    policyOn = on;
    policyBusy = true;
    error = '';
    try {
      const p = await savePolicy({ notify_on_sync_failure: on });
      conf = { ...conf, policy: p };
      policyOn = p.notify_on_sync_failure;
    } catch (e) {
      error = String(e);
      policyOn = conf.policy.notify_on_sync_failure;
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

  const fmtInterval = (secs: number): string => {
    if (secs <= 0) return '默认';
    const mins = Math.round(secs / 60);
    // 整小时折成「N 小时」：360 分钟在右对齐的窄列里既长又容易被拆行。
    if (mins >= 60 && mins % 60 === 0) return `${mins / 60} 小时`;
    return `${mins} 分钟`;
  };

  const secretSourceLabel = $derived(SECRET_SOURCE_LABELS[conf?.secret_source ?? ''] ?? '未知');

  // 数据导入：上传 /api/v1/export 下载的 JSON，按 (source, external_id) 幂等合并。
  let importing = $state(false);
  let importResult = $state<ImportResult | null>(null);

  async function onImportFile(e: { currentTarget: HTMLInputElement }) {
    const file = e.currentTarget.files?.[0];
    e.currentTarget.value = ''; // 清掉选择，同名文件重选也能再次触发
    if (!file) return;
    importing = true;
    importResult = null;
    error = '';
    try {
      importResult = await importEvents(JSON.parse(await file.text()));
    } catch (err) {
      error = String(err);
    } finally {
      importing = false;
    }
  }
</script>

<svelte:head>
  <title>观复 · 设置</title>
</svelte:head>

<TopBar current="设置" />

<div class="mx-auto w-full max-w-[1080px] px-4 py-5 md:px-6 lg:px-8">
  {#if error}
    <div class="mb-4 rounded-lg bg-chip px-3 py-2 fs-125 text-neg" role="alert">{error}</div>
  {/if}

  <Section title="外观" meta="存在这台设备的浏览器里，不写进数据库">
    <Row title="主题" desc="「跟随系统」随窗口深浅色即时切换">
      <Segmented
        label="主题"
        options={THEME_OPTIONS}
        value={ui.theme}
        onchange={(v) => setTheme(v as Theme)}
      />
    </Row>
    <Row title="字号" desc="只缩放文字，间距、圆角、图标尺寸保持原样">
      <Segmented
        label="文字大小"
        options={SIZE_OPTIONS}
        value={ui.textSize}
        onchange={(v) => setTextSize(v as TextSize)}
      />
    </Row>
  </Section>

  <Section
    title="数据源"
    meta={loading
      ? ''
      : `已配置 ${configuredCount}/${sourceEndpoints.length} · 优先级 页面 > 环境变量`}
  >
    {#if loading}
      <div class="px-4 py-10 text-center fs-125 text-ink-3">加载中…</div>
    {:else if sourceEndpoints.length === 0}
      <div class="px-4 py-10 text-center fs-125 text-ink-3">
        没有任何数据源。这是配置层的问题（env 源清单为空），检查启动参数。
      </div>
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full text-left fs-12 md:min-w-[880px]">
          <!-- 数据行整体不换行：CJK 在 auto 表格里任意断行，「运行中」「44 分钟前」
               会被拆得长长短短；放行溢出交给外层 overflow-x-auto 滚动。
               编辑器行（colspan）不在此约束内，表单文本照常折行。 -->
          <thead class="border-b border-line fs-11 whitespace-nowrap text-ink-3">
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
              <tr class="border-t border-line whitespace-nowrap">
                <td class="px-3 py-2.5">
                  <span class="font-medium text-ink">{sourceLabel(e.name)}</span>
                  <!-- 窄屏只留中文名 + 来源徽标：7 列在 390px 上会把「编辑」挤出屏幕。 -->
                  <span class="ml-1.5 hidden text-ink-3 sm:inline">{e.name}</span>
                  <span class="ml-1.5 tag">{e.origin === 'page' ? '页面' : 'env'}</span>
                </td>
                <td class="px-3 py-2.5 {st.cls}" title={s?.last_error || undefined}>
                  <span class="inline-flex items-center gap-1.5">
                    <span class="h-1.5 w-1.5 rounded-full" style:background={st.dot}></span>
                    {st.label}
                  </span>
                </td>
                <td
                  class="hidden max-w-[220px] truncate px-3 py-2.5 text-ink-3 md:table-cell"
                  title={e.base_url || ''}
                >
                  {e.base_url || '—'}
                </td>
                <td
                  class="hidden px-3 py-2.5 text-ink-3 lg:table-cell"
                  title={AUTH_LABELS[e.auth as AuthKind]}
                >
                  {AUTH_SHORT_LABELS[e.auth as AuthKind] ?? e.auth}
                </td>
                <td class="hidden px-3 py-2.5 lg:table-cell">
                  {#if e.auth === 'none'}
                    <span class="text-ink-3">不需要</span>
                  {:else if e.secret_set}
                    <span class="text-pos">{e.secret_hint}</span>
                  {:else}
                    <span class="text-ink-3">未设置</span>
                  {/if}
                </td>
                <td class="tnum hidden px-3 py-2.5 text-right text-ink-3 md:table-cell">
                  {fmtInterval(e.interval_secs || s?.interval_secs || 0)}
                </td>
                <td class="tnum hidden px-3 py-2.5 text-right text-ink-3 sm:table-cell">
                  {fmtAgo(s?.last_sync_at ?? 0)}
                </td>
                <td class="px-3 py-2.5 text-right">
                  <button
                    type="button"
                    class="btn"
                    aria-expanded={editing === e.name}
                    onclick={() => toggleEditor(e.name)}
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
                      onclose={closeEditor}
                      onDirty={(d) => (editorDirty = d)}
                      onsync={refreshSources}
                    />
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>

      {#if failing.length > 0}
        <details class="px-4 py-2.5 fs-12 text-ink-2">
          <summary class="cursor-pointer fs-11 text-ink-3 select-none">
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

      <!-- 长说明折进卡片末尾：改配置前先给一行结论（标题旁的优先级），细节按需展开。 -->
      <details class="px-4 py-2.5 fs-115 leading-relaxed text-ink-2">
        <summary class="cursor-pointer fs-11 text-ink-3 select-none">
          配置说明：优先级 · 生效时机 · 凭据加密
        </summary>
        <ul class="mt-2 list-disc space-y-1 pl-4">
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
          <li>
            页面写入的凭据以 AES-GCM 密文落库（密钥：{secretSourceLabel}），接口只回末 4
            位；明文不会经过浏览器。
          </li>
        </ul>
      </details>
    {/if}
  </Section>

  <Section title="数据维护" meta="迁移实例 · 误清后回填">
    <Row
      title="导入导出 JSON"
      desc="上传「导出 JSON」下载的文件；按 source+external_id 幂等合并，不会删除现有数据"
    >
      <div class="flex flex-wrap items-center gap-2">
        <label class="btn cursor-pointer" class:opacity-50={importing}>
          {importing ? '导入中…' : '选择文件导入'}
          <input
            type="file"
            accept="application/json,.json"
            class="hidden"
            disabled={importing}
            onchange={onImportFile}
          />
        </label>
        {#if importResult}
          <span
            class="fs-12 {importResult.skipped.length > 0 ? 'text-ink-2' : 'text-pos'}"
            role="status"
          >
            共 {importResult.total} 条 · 更新 {importResult.changed} 条{importResult.skipped.length
              ? ` · 跳过 ${importResult.skipped.length} 条`
              : ''}
          </span>
        {/if}
      </div>
    </Row>
    {#if importResult && importResult.skipped.length > 0}
      <ul class="space-y-1 px-4 pb-3 fs-11 text-ink-3">
        {#each importResult.skipped.slice(0, 10) as sk, i (i)}
          <li>{sk.source || '（无源）'} · {sk.external_id || '（无 ID）'}：{sk.reason}</li>
        {/each}
        {#if importResult.skipped.length > 10}
          <li>… 其余 {importResult.skipped.length - 10} 条从略</li>
        {/if}
      </ul>
    {/if}
  </Section>

  <Section title="通知渠道" meta="同步失败、手动测试都走这里" foot={footNote}>
    {#if conf}
      {#each notifyEndpoints as e (e.name)}
        <NotifyEditor
          endpoint={e}
          authModes={conf.auth_modes ?? []}
          open={editing === e.name}
          ontoggle={() => toggleEditor(e.name)}
          onsaved={replaceEndpoint}
          onDirty={(d) => (editorDirty = d)}
        />
      {/each}
      <Row title="同步失败时推送" desc="正常转为失败时发一条，同一源 6 小时内不重复">
        <Switch
          checked={policyOn}
          label="同步失败时推送"
          disabled={policyBusy}
          onchange={togglePolicy}
        />
      </Row>
    {:else}
      <div class="px-4 py-10 text-center fs-125 text-ink-3">加载中…</div>
    {/if}
  </Section>
</div>

{#snippet footNote()}
  <p>
    本应用没有内置登录，设置页可以写入凭据，请只在受信网络内访问；要放到公网，请在前置反向代理上加认证。加密密钥若换了（改
    <code class="rounded bg-chip px-1">GUANFU_SECRET_KEY</code> 或删除密钥文件），旧密文会解不开，页面会明确报错，重新填一次即可。
  </p>
{/snippet}
