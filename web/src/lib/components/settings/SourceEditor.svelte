<script lang="ts">
  // 单个数据源的内联编辑器。
  //
  // 凭据三态：输入框有值 = 设置新值；点「清除」= 显式清空；两者都没有 = 保留原值。
  // 页面永远拿不到明文（后端只回掩码），所以不能把已存值回填到输入框里。
  import { AUTH_LABELS, triggerSync, type AuthKind } from '#lib/api.ts';
  import {
    resetEndpoint,
    saveEndpoint,
    verifyEndpoint,
    type EndpointInput,
    type EndpointView,
    type ProbeResult
  } from '#lib/settings.ts';
  import { untrack } from 'svelte';
  import Field from './Field.svelte';
  import Icon from '../Icon.svelte';

  let {
    endpoint,
    authModes,
    onsaved,
    onclose,
    onDirty = () => {},
    onsync = () => {}
  }: {
    endpoint: EndpointView;
    authModes: AuthKind[];
    onsaved: (e: EndpointView) => void;
    onclose: () => void;
    onDirty?: (dirty: boolean) => void;
    onsync?: () => void;
  } = $props();

  // 表单是「编辑副本」：只在挂载时取一次 props，之后由用户输入驱动。
  // untrack 是必要的——直接读 prop 初始化会被 Svelte 判成"只捕获了初始值"，
  // 而且保存/重置后我们本来就要用后端返回值显式回填（见 applyEndpoint）。
  const init = untrack(() => endpoint);

  let baseUrl = $state(init.base_url);
  let auth = $state<AuthKind>(init.auth);
  let secret = $state('');
  let clearSecret = $state(false);
  let enabled = $state(init.enabled);
  let intervalMin = $state(init.interval_secs > 0 ? Math.round(init.interval_secs / 60) : 0);

  let baseline = $state({
    baseUrl: init.base_url,
    auth: init.auth,
    enabled: init.enabled,
    intervalMin: init.interval_secs > 0 ? Math.round(init.interval_secs / 60) : 0
  });
  const dirty = $derived(
    baseUrl !== baseline.baseUrl ||
      auth !== baseline.auth ||
      enabled !== baseline.enabled ||
      intervalMin !== baseline.intervalMin ||
      secret !== '' ||
      clearSecret
  );

  let busy = $state<'' | 'save' | 'test' | 'reset' | 'sync'>('');
  let error = $state('');
  let probe = $state<ProbeResult | null>(null);
  let flash = $state('');

  let confirmReset = $state(false);
  let resetTimer: ReturnType<typeof setTimeout> | undefined;

  $effect(() => {
    onDirty(dirty);
  });

  const secretPlaceholder = $derived(
    clearSecret
      ? '保存后凭据将被清除'
      : endpoint.secret_set
        ? `已设置 ${endpoint.secret_hint}（留空则不改）`
        : '粘贴凭据'
  );

  // applyEndpoint 用后端返回的规范化结果回填（例如地址末尾斜杠被裁掉、
  // 认证方式回落到默认值），避免输入框里留着一个"看起来已保存"的旧值。
  function applyEndpoint(e: EndpointView) {
    baseUrl = e.base_url;
    auth = e.auth;
    enabled = e.enabled;
    intervalMin = e.interval_secs > 0 ? Math.round(e.interval_secs / 60) : 0;
    baseline = {
      baseUrl: e.base_url,
      auth: e.auth,
      enabled: e.enabled,
      intervalMin: e.interval_secs > 0 ? Math.round(e.interval_secs / 60) : 0
    };
    secret = '';
    clearSecret = false;
  }

  function payload(): EndpointInput {
    const p: EndpointInput = {
      base_url: baseUrl.trim(),
      auth,
      enabled,
      interval_secs: Math.max(0, Math.round(intervalMin)) * 60
    };
    // 只有用户动过凭据才发这个字段；否则后端保留原值。
    if (secret) p.secret = secret;
    else if (clearSecret) p.secret = '';
    return p;
  }

  async function save() {
    busy = 'save';
    confirmReset = false;
    clearTimeout(resetTimer);
    error = '';
    flash = '';
    try {
      const v = await saveEndpoint(endpoint.name, payload());
      applyEndpoint(v);
      flash = '已保存并生效';
      onsaved(v);
    } catch (e) {
      error = String(e);
    } finally {
      busy = '';
    }
  }

  async function test() {
    busy = 'test';
    confirmReset = false;
    clearTimeout(resetTimer);
    error = '';
    probe = null;
    try {
      probe = await verifyEndpoint(endpoint.name, payload());
    } catch (e) {
      error = String(e);
    } finally {
      busy = '';
    }
  }

  async function syncNow() {
    error = '';
    flash = '';
    if (dirty) {
      error = '有未保存的修改，请先保存再同步';
      return;
    }
    busy = 'sync';
    try {
      await triggerSync(endpoint.name);
      flash = '已触发同步，3 秒后刷新状态';
      setTimeout(() => {
        busy = '';
        onsync();
      }, 3000);
    } catch (e) {
      error = String(e);
      busy = '';
    }
  }

  // 删除页面配置不可撤销：第一次点击只“上膛”，6 秒内再点才真正执行。
  function armReset() {
    confirmReset = true;
    clearTimeout(resetTimer);
    resetTimer = setTimeout(() => (confirmReset = false), 6000);
  }

  async function reset() {
    busy = 'reset';
    confirmReset = false;
    clearTimeout(resetTimer);
    error = '';
    flash = '';
    try {
      const r = await resetEndpoint(endpoint.name);
      applyEndpoint(r.endpoint);
      probe = null;
      onsaved(r.endpoint);
      flash = r.removed ? '已恢复为环境变量配置' : '本来就没有页面配置';
    } catch (e) {
      error = String(e);
    } finally {
      busy = '';
    }
  }
</script>

<div class="border-t border-line bg-surface-2 px-3 py-3">
  <div class="grid grid-cols-1 gap-x-4 gap-y-2.5 sm:grid-cols-2 lg:grid-cols-4">
    <Field
      label="端点地址"
      hint={endpoint.env_base_url ? 'env: ' + endpoint.env_base_url : ''}
      className="sm:col-span-2"
    >
      <input class="field" bind:value={baseUrl} placeholder="http://host:port" spellcheck="false" />
    </Field>

    <Field label="认证方式" hint="默认 {AUTH_LABELS[endpoint.default_auth]}">
      <select class="field" bind:value={auth}>
        {#each authModes as m (m)}
          <option value={m}>{AUTH_LABELS[m]}</option>
        {/each}
      </select>
    </Field>

    <Field label="凭据" hint={endpoint.credential_env}>
      <div class="flex items-center gap-1.5">
        <input
          class="field min-w-0 flex-1"
          type="password"
          autocomplete="new-password"
          placeholder={secretPlaceholder}
          bind:value={secret}
          oninput={() => (clearSecret = false)}
        />
        {#if endpoint.secret_set}
          {#if clearSecret}
            <button type="button" class="btn-text shrink-0" onclick={() => (clearSecret = false)}>
              撤销
            </button>
          {:else}
            <button
              type="button"
              class="btn-text shrink-0 btn-text-danger"
              onclick={() => {
                clearSecret = true;
                secret = '';
              }}
            >
              清除
            </button>
          {/if}
        {/if}
      </div>
    </Field>

    <Field label="同步间隔" hint="0 = 用适配器默认">
      <div class="flex items-center gap-1.5">
        <input
          class="tnum field w-20"
          type="number"
          min="0"
          max="43200"
          step="5"
          bind:value={intervalMin}
        />
        <span class="fs-11 text-ink-3">分钟</span>
      </div>
    </Field>

    <Field label="启用">
      <div class="flex h-[30px] items-center">
        <input type="checkbox" class="peer sr-only" bind:checked={enabled} />
        <span
          aria-hidden="true"
          class="relative h-[18px] w-[32px] rounded-full transition-colors peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-[var(--gf-ink-3)] {enabled
            ? 'bg-[var(--gf-accent)]'
            : 'bg-[var(--gf-line-strong)]'}"
        >
          <span
            class="absolute top-[2px] h-[14px] w-[14px] rounded-full shadow-sm transition-[left,background-color] {enabled
              ? 'bg-[var(--gf-accent-ink)]'
              : 'bg-[var(--gf-ink-2)]'}"
            style:left={enabled ? '16px' : '2px'}
          ></span>
        </span>
      </div>
    </Field>
  </div>

  {#if endpoint.error}
    <p class="mt-2 rounded-md bg-chip px-2 py-1.5 fs-115 text-neg">
      现有配置有问题：{endpoint.error}
    </p>
  {/if}

  <div class="mt-3 flex flex-wrap items-center gap-2">
    <button type="button" class="btn btn-primary" onclick={save} disabled={busy !== ''}>
      {busy === 'save' ? '保存中…' : '保存'}
    </button>
    <button
      type="button"
      class="btn"
      onclick={test}
      disabled={busy !== '' || !baseUrl.trim()}
      title={baseUrl.trim() ? '' : '先填端点地址'}
    >
      <span class="hidden sm:inline">{busy === 'test' ? '测试中…' : '测试连通'}</span>
      <span class="sm:hidden">{busy === 'test' ? '测试中' : '测试'}</span>
    </button>
    <button
      type="button"
      class="btn"
      onclick={syncNow}
      disabled={busy !== '' || !baseUrl.trim() || !enabled}
      title={!baseUrl.trim()
        ? '先填端点地址'
        : !enabled
          ? '启用后才能同步'
          : dirty
            ? '先保存修改再同步'
            : '立即触发一次同步'}
      aria-label="立刻同步"
    >
      <Icon name="sync" size={13} cls={busy === 'sync' ? 'animate-spin' : ''} />
      <span class="hidden sm:inline">{busy === 'sync' ? '同步中…' : '立刻同步'}</span>
    </button>
    <button
      type="button"
      class="ml-auto btn text-neg"
      onclick={() => (confirmReset ? reset() : armReset())}
      disabled={busy !== '' || endpoint.origin === 'env'}
      title={confirmReset ? '再点一次立即删除页面配置' : '删除页面配置，回到环境变量'}
    >
      <span class="hidden sm:inline">
        {busy === 'reset' ? '恢复中…' : confirmReset ? '确认恢复？' : '恢复环境变量'}
      </span>
      <span class="sm:hidden">
        {busy === 'reset' ? '恢复中' : confirmReset ? '确认恢复' : '恢复'}
      </span>
    </button>
    <button type="button" class="btn" onclick={onclose}>收起</button>
  </div>

  {#if flash}
    <p class="mt-2 fs-115 text-pos">{flash}</p>
  {/if}
  {#if error}
    <p class="mt-2 fs-115 text-neg">{error}</p>
  {/if}
  {#if probe}
    <!-- 结果卡片按内容宽度收缩：一句话结论横跨四列看着像块空条。 -->
    <div class="mt-2 max-w-full rounded-md border border-line bg-surface px-2.5 py-2 fs-115">
      <p class="flex flex-wrap items-baseline gap-x-1.5">
        <span class="font-medium {probe.ok ? 'text-pos' : 'text-neg'}">
          {probe.ok ? '连通' : '不通'}
        </span>
        <span class={probe.ok ? 'text-pos' : 'text-neg'}>{probe.message}</span>
        {#if probe.status}
          <span class="tnum text-ink-3">HTTP {probe.status} · {probe.duration_ms}ms</span>
        {/if}
      </p>
      {#if probe.response}
        <p class="mt-1 break-all text-ink-3">{probe.response}</p>
      {/if}
    </div>
  {/if}
</div>
