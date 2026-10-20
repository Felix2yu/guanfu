<script lang="ts">
  // 通知渠道（Apprise）：状态行 + 就地展开的表单，和数据源用同一套开合逻辑。
  // 单渠道常驻展开会让这张卡比同页其它卡长出三四倍，收起后行高就一致了。
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
  import Row from './Row.svelte';
  import Switch from '../Switch.svelte';

  let {
    endpoint,
    authModes,
    open,
    ontoggle,
    onsaved,
    onDirty = () => {}
  }: {
    endpoint: EndpointView;
    authModes: AuthKind[];
    open: boolean;
    ontoggle: () => void;
    onsaved: (e: EndpointView) => void;
    onDirty?: (dirty: boolean) => void;
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

  let baseline = $state({
    baseUrl: init.base_url,
    auth: init.auth,
    enabled: init.enabled,
    key: init.extras?.key ?? '',
    targets: init.extras?.targets ?? ''
  });
  const dirty = $derived(
    baseUrl !== baseline.baseUrl ||
      auth !== baseline.auth ||
      enabled !== baseline.enabled ||
      key !== baseline.key ||
      targets !== baseline.targets ||
      secret !== '' ||
      clearSecret
  );

  let busy = $state<'' | 'save' | 'test' | 'reset'>('');
  let error = $state('');
  let result = $state<NotifyResult | null>(null);
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
        : '粘贴凭据（无鉴权可留空）'
  );

  const summary = $derived(
    [
      baseUrl || '未配置地址',
      key
        ? `key: ${key}`
        : targets
          ? `目标 ${targets.split(/[\s,]+/).filter(Boolean).length} 条`
          : '未填目标'
    ].join(' · ')
  );

  const status = $derived(
    !endpoint.base_url
      ? { label: '未配置', cls: 'text-ink-3' }
      : endpoint.enabled
        ? { label: '已启用', cls: 'text-pos' }
        : { label: '已停用', cls: 'text-ink-3' }
  );

  function applyEndpoint(e: EndpointView) {
    baseUrl = e.base_url;
    auth = e.auth;
    enabled = e.enabled;
    key = e.extras?.key ?? '';
    targets = e.extras?.targets ?? '';
    baseline = {
      baseUrl: e.base_url,
      auth: e.auth,
      enabled: e.enabled,
      key: e.extras?.key ?? '',
      targets: e.extras?.targets ?? ''
    };
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
    p.extras = { key: key.trim(), targets: targets.trim() };
    return p;
  }

  async function save(): Promise<EndpointView | null> {
    confirmReset = false;
    clearTimeout(resetTimer);
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

  // 删除页面配置不可撤销：第一次点击只“上膛”，6 秒内再点才真正执行。
  function armReset() {
    confirmReset = true;
    clearTimeout(resetTimer);
    resetTimer = setTimeout(() => (confirmReset = false), 6000);
  }

  async function onReset() {
    busy = 'reset';
    confirmReset = false;
    clearTimeout(resetTimer);
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

<Row title="Apprise" desc={summary}>
  <span class="tag">{endpoint.origin === 'page' ? '页面' : 'env'}</span>
  <span class="fs-11 {status.cls}">{status.label}</span>
  <button type="button" class="btn" aria-expanded={open} onclick={ontoggle}>
    {open ? '收起' : '编辑'}
  </button>
</Row>

{#if open}
  <div class="bg-surface-2 px-4 py-3">
    <div class="grid grid-cols-1 gap-x-4 gap-y-2.5 sm:grid-cols-2 xl:grid-cols-3">
      <Field label="地址" hint={endpoint.env_base_url ? 'env: ' + endpoint.env_base_url : ''}>
        <input
          class="field"
          bind:value={baseUrl}
          placeholder="http://host:8000"
          spellcheck="false"
        />
      </Field>

      <Field label="认证方式" hint="自建实例通常无需凭据">
        <select class="field" bind:value={auth}>
          {#each authModes as m (m)}
            <option value={m}>{AUTH_LABELS[m]}</option>
          {/each}
        </select>
      </Field>

      <Field label="凭据" hint="随认证方式发送">
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

      <Field label="配置 Key" hint="服务端已保存的配置名">
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

      <Field label="启用">
        <div class="flex h-[30px] items-center gap-2">
          <Switch checked={enabled} label="参与推送" onchange={(v) => (enabled = v)} />
          <span class="fs-12 text-ink-2">{enabled ? '参与推送' : '不发送'}</span>
        </div>
      </Field>
    </div>

    {#if endpoint.error}
      <p class="mt-2 rounded-md bg-chip px-2 py-1.5 fs-115 text-neg">
        现有配置有问题：{endpoint.error}
      </p>
    {/if}

    <div class="mt-3 flex flex-wrap items-center gap-2">
      <button type="button" class="btn btn-primary" onclick={onSave} disabled={busy !== ''}>
        {busy === 'save' ? '保存中…' : '保存'}
      </button>
      <button type="button" class="btn" onclick={onSaveAndTest} disabled={busy !== ''}>
        {busy === 'test' ? '发送中…' : '保存并测试'}
      </button>
      <button
        type="button"
        class="ml-auto btn text-neg"
        onclick={() => (confirmReset ? onReset() : armReset())}
        disabled={busy !== '' || endpoint.origin === 'env'}
        title={confirmReset ? '再点一次立即删除页面配置' : '删除页面配置，回到环境变量'}
      >
        {busy === 'reset' ? '恢复中…' : confirmReset ? '确认恢复？' : '恢复环境变量'}
      </button>
      <button type="button" class="btn" onclick={ontoggle}>收起</button>
    </div>

    {#if flash}
      <p class="mt-2 fs-115 text-pos">{flash}</p>
    {/if}
    {#if error}
      <p class="mt-2 fs-115 text-neg" role="alert">{error}</p>
    {/if}
    {#if result}
      <div class="mt-2 rounded-md border border-line bg-surface px-2.5 py-2 fs-115">
        <p class={result.ok ? 'text-pos' : 'text-neg'}>
          {result.ok ? '发送成功' : '发送失败'}
          {#if result.status}
            <span class="tnum ml-1.5 text-ink-3">HTTP {result.status} · {result.duration_ms}ms</span
            >
          {/if}
          {#if result.error}<span class="ml-1.5">{result.error}</span>{/if}
        </p>
        {#if result.response}
          <p class="mt-1 break-all text-ink-3">{result.response}</p>
        {/if}
      </div>
    {/if}
  </div>
{/if}
