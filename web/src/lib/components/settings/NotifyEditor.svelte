<script lang="ts">
  // 通知渠道编辑器（Unraid / Apprise）。
  //
  // 测试发送打的是「已保存」的配置（后端按生效配置构造渠道），
  // 所以按钮写成「保存并测试」——避免用户改了地址点测试、实际测的是旧地址。
  import { AUTH_LABELS, type AuthKind } from '#lib/api.ts';
  import {
    resetEndpoint,
    saveEndpoint,
    testNotify,
    type EndpointInput,
    type EndpointView,
    type NotifyResult
  } from '#lib/settings.ts';
  import { untrack } from 'svelte';
  import Field from './Field.svelte';

  let {
    endpoint,
    authModes,
    onsaved
  }: {
    endpoint: EndpointView;
    authModes: AuthKind[];
    onsaved: (e: EndpointView) => void;
  } = $props();

  // 同 SourceEditor：挂载时取一次 props，保存/重置后用后端返回值回填。
  const init = untrack(() => endpoint);

  let baseUrl = $state(init.base_url);
  let auth = $state<AuthKind>(init.auth);
  let secret = $state('');
  let clearSecret = $state(false);
  let enabled = $state(init.enabled);
  let key = $state(init.extras?.key ?? '');
  let targets = $state(init.extras?.targets ?? '');

  let busy = $state<'' | 'save' | 'test' | 'reset'>('');
  let error = $state('');
  let result = $state<NotifyResult | null>(null);
  let flash = $state('');

  const isApprise = $derived(endpoint.name === 'apprise');

  const secretPlaceholder = $derived(
    clearSecret
      ? '保存后凭据将被清除'
      : endpoint.secret_set
        ? `已设置 ${endpoint.secret_hint}（留空则不改）`
        : endpoint.name === 'unraid'
          ? '粘贴 Unraid API Key'
          : '粘贴凭据（无鉴权可留空）'
  );

  function applyEndpoint(e: EndpointView) {
    baseUrl = e.base_url;
    auth = e.auth;
    enabled = e.enabled;
    key = e.extras?.key ?? '';
    targets = e.extras?.targets ?? '';
    secret = '';
    clearSecret = false;
  }

  function payload(): EndpointInput {
    // notify 不用间隔，原样带回避免整块替换时被清零。
    const p: EndpointInput = {
      base_url: baseUrl.trim(),
      auth,
      enabled,
      interval_secs: endpoint.interval_secs
    };
    if (secret) p.secret = secret;
    else if (clearSecret) p.secret = '';
    if (isApprise) p.extras = { key: key.trim(), targets: targets.trim() };
    return p;
  }

  async function save(): Promise<EndpointView | null> {
    const v = await saveEndpoint(endpoint.name, payload());
    applyEndpoint(v);
    onsaved(v);
    flash = '已保存并生效';
    return v;
  }

  async function onSave() {
    busy = 'save';
    error = '';
    result = null;
    try {
      await save();
    } catch (e) {
      error = String(e);
    } finally {
      busy = '';
    }
  }

  async function onSaveAndTest() {
    busy = 'test';
    error = '';
    result = null;
    flash = '';
    try {
      // 先落库再测：测试发送走的是生效配置，不落库等于测旧值。
      await save();
      result = await testNotify(endpoint.name);
    } catch (e) {
      error = String(e);
    } finally {
      busy = '';
    }
  }

  async function onReset() {
    busy = 'reset';
    error = '';
    result = null;
    try {
      const r = await resetEndpoint(endpoint.name);
      applyEndpoint(r.endpoint);
      onsaved(r.endpoint);
      flash = r.removed ? '已恢复为环境变量配置' : '本来就没有页面配置';
    } catch (e) {
      error = String(e);
    } finally {
      busy = '';
    }
  }
</script>

<div class="rounded-xl border border-line bg-surface p-3">
  <div class="mb-2 flex flex-wrap items-baseline gap-x-2 gap-y-1">
    <h3 class="text-[13px] font-semibold text-ink">
      {endpoint.name === 'unraid' ? 'Unraid' : 'Apprise'}
    </h3>
    <span class="tag">{endpoint.origin === 'page' ? '页面配置' : '环境变量'}</span>
    {#if endpoint.enabled && endpoint.base_url}
      <span class="text-[11px] text-pos">已启用</span>
    {:else}
      <span class="text-[11px] text-ink-3">未启用</span>
    {/if}
    <span class="ml-auto text-[11px] text-ink-3">{endpoint.credential_env}</span>
  </div>

  <p class="mb-2.5 text-[11.5px] leading-relaxed text-ink-3">
    {#if endpoint.name === 'unraid'}
      Unraid 7 的 GraphQL 接口，地址填 <code class="rounded bg-chip px-1">http://tower</code> 或
      <code class="rounded bg-chip px-1">http://tower/graphql</code>；API Key 在 Settings →
      Management Access → API Keys 生成（需要通知写入权限）。通知会进 Unraid
      通知中心，再由它自己的代理分发。
    {:else}
      Apprise API 或 Apprise-Go 的 <code class="rounded bg-chip px-1">/notify</code>。Key
      是服务端已保存的配置名；留空则直接在下方写目标 URL（Apprise URL 形态，如
      <code class="rounded bg-chip px-1">tgram://bot/chat</code>）。
    {/if}
  </p>

  <div class="grid grid-cols-1 gap-x-4 gap-y-2.5 sm:grid-cols-2">
    <Field label="地址" hint={endpoint.env_base_url ? 'env: ' + endpoint.env_base_url : ''}>
      <input class="field" bind:value={baseUrl} placeholder="http://host:port" spellcheck="false" />
    </Field>

    <Field label="认证方式" hint={isApprise ? '自建实例通常无需凭据' : '默认 API Key 请求头'}>
      <select class="field" bind:value={auth}>
        {#each authModes as m (m)}
          <option value={m}>{AUTH_LABELS[m]}</option>
        {/each}
      </select>
    </Field>

    <Field label="凭据" hint={endpoint.name === 'unraid' ? 'X-API-Key' : '随认证方式发送'}>
      <div class="flex items-center gap-1.5">
        <input
          class="field min-w-0 flex-1"
          type="password"
          autocomplete="new-password"
          placeholder={secretPlaceholder}
          bind:value={secret}
          oninput={() => (clearSecret = false)}
        />
        {#if endpoint.secret_set && !clearSecret}
          <button
            type="button"
            class="btn shrink-0"
            onclick={() => {
              clearSecret = true;
              secret = '';
            }}
          >
            清除
          </button>
        {/if}
      </div>
    </Field>

    <Field label="启用">
      <label class="flex h-[30px] items-center gap-2 text-[12.5px] text-ink-2">
        <input
          type="checkbox"
          class="h-3.5 w-3.5 accent-[var(--gf-accent)]"
          bind:checked={enabled}
        />
        {enabled ? '参与推送' : '不发送'}
      </label>
    </Field>

    {#if isApprise}
      <Field label="配置 Key" hint="填了就用服务端已存配置">
        <input class="field" bind:value={key} placeholder="例如 family" spellcheck="false" />
      </Field>
      <Field label="目标 URL" hint="Key 留空时必填，逗号/换行分隔">
        <input
          class="field"
          bind:value={targets}
          placeholder="tgram://bot/chat, mailto://…"
          spellcheck="false"
        />
      </Field>
    {/if}
  </div>

  {#if endpoint.error}
    <p class="mt-2 rounded-md bg-chip px-2 py-1.5 text-[11.5px] text-neg">
      现有配置有问题：{endpoint.error}
    </p>
  {/if}

  <div class="mt-3 flex flex-wrap items-center gap-2">
    <button type="button" class="btn" onclick={onSave} disabled={busy !== ''}>
      {busy === 'save' ? '保存中…' : '保存'}
    </button>
    <button type="button" class="btn btn-primary" onclick={onSaveAndTest} disabled={busy !== ''}>
      {busy === 'test' ? '发送中…' : '保存并测试'}
    </button>
    <button
      type="button"
      class="btn"
      onclick={onReset}
      disabled={busy !== '' || endpoint.origin === 'env'}
    >
      {busy === 'reset' ? '恢复中…' : '恢复环境变量'}
    </button>
  </div>

  {#if flash}
    <p class="mt-2 text-[11.5px] text-pos">{flash}</p>
  {/if}
  {#if error}
    <p class="mt-2 text-[11.5px] text-neg">{error}</p>
  {/if}
  {#if result}
    <div class="mt-2 rounded-md border border-line bg-surface-2 px-2.5 py-2 text-[11.5px]">
      <p class={result.ok ? 'text-pos' : 'text-neg'}>
        {result.ok ? '发送成功' : '发送失败'}
        {#if result.status}
          <span class="tnum ml-1.5 text-ink-3">HTTP {result.status} · {result.duration_ms}ms</span>
        {/if}
        {#if result.error}<span class="ml-1.5">{result.error}</span>{/if}
      </p>
      {#if result.response}
        <p class="mt-1 break-all text-ink-3">{result.response}</p>
      {/if}
    </div>
  {/if}
</div>
