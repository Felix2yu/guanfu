<script lang="ts">
  // 「补一笔」手工补录对话框：无源可归的事件（临时想法、补记一笔开销）走
  // POST /api/v1/ingest/event（source=ingest）。external_id 在打开时生成一次，
  // 提交失败后重试仍用同一个 id——幂等覆盖，不会产生重复事件。
  import { ingestEvent, TYPE_LABEL } from '../api.ts';
  import { untrack } from 'svelte';

  let {
    date,
    onclose,
    onsaved
  }: { date: string; onclose: () => void; onsaved: (date: string) => void } = $props();

  // 手工补录常用的几种类型；记账带金额才进收支合计（sumBy 只认 transaction）。
  const TYPES = ['note', 'transaction', 'task_done', 'review', 'anniversary'] as const;

  // 对话框每次都是 {#if} 挂载的新实例，取 props 初值即够；untrack 避免
  // 「只捕获了初始值」的告警——这里本来就是要初始值。
  let fdate = $state(untrack(() => date));
  let ftime = $state('');
  let type = $state<(typeof TYPES)[number]>('note');
  let title = $state('');
  let summary = $state('');
  let amountYuan = $state('');
  let busy = $state(false);
  let error = $state('');

  const externalId = `ui-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = '';
    if (!title.trim()) {
      error = '标题必填';
      return;
    }
    let amount_cents: number | undefined;
    if (amountYuan.trim() !== '') {
      const n = Number(amountYuan);
      if (!Number.isFinite(n) || n < 0) {
        error = '金额需是非负数字（元）';
        return;
      }
      amount_cents = Math.round(n * 100);
    }
    busy = true;
    try {
      await ingestEvent({
        external_id: externalId,
        event_type: type,
        date: fdate,
        time: ftime || undefined,
        title: title.trim(),
        summary: summary.trim() || undefined,
        amount_cents
      });
      onsaved(fdate);
    } catch (err) {
      error = String(err);
    } finally {
      busy = false;
    }
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') onclose();
  }
</script>

<svelte:window onkeydown={onKeydown} />

<div
  class="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 pt-[10vh]"
  role="dialog"
  aria-modal="true"
  aria-label="补一笔"
  tabindex="-1"
  onclick={(e) => {
    if (e.currentTarget === e.target) onclose();
  }}
  onkeydown={onKeydown}
>
  <form
    class="w-full max-w-sm rounded-xl border border-line bg-surface p-4 shadow-lg"
    onsubmit={submit}
  >
    <div class="mb-3 flex items-center justify-between">
      <h2 class="fs-15 font-semibold text-ink">补一笔</h2>
      <button type="button" class="btn h-7 px-2 fs-12" onclick={onclose} aria-label="关闭">
        ✕
      </button>
    </div>

    <div class="grid grid-cols-2 gap-2.5">
      <label class="block">
        <span class="mb-1 block fs-11 text-ink-3">日期</span>
        <input class="field tnum" type="date" bind:value={fdate} required />
      </label>
      <label class="block">
        <span class="mb-1 block fs-11 text-ink-3">时刻（留空 = 全天）</span>
        <input class="field tnum" type="time" bind:value={ftime} />
      </label>
      <label class="col-span-2 block">
        <span class="mb-1 block fs-11 text-ink-3">标题</span>
        <input class="field" bind:value={title} placeholder="要记的事" required />
      </label>
      <label class="block">
        <span class="mb-1 block fs-11 text-ink-3">类型</span>
        <select class="field" bind:value={type}>
          {#each TYPES as t (t)}
            <option value={t}>{TYPE_LABEL[t] ?? t}</option>
          {/each}
        </select>
      </label>
      <label class="block">
        <span class="mb-1 block fs-11 text-ink-3">金额（元，可空）</span>
        <input
          class="field tnum"
          type="number"
          inputmode="decimal"
          min="0"
          step="0.01"
          bind:value={amountYuan}
          placeholder="0.00"
        />
      </label>
      <label class="col-span-2 block">
        <span class="mb-1 block fs-11 text-ink-3">备注（可空）</span>
        <textarea class="field min-h-16 resize-y" bind:value={summary} rows="2"></textarea>
      </label>
    </div>

    {#if error}
      <p class="mt-2 fs-115 text-neg" role="alert">{error}</p>
    {/if}

    <div class="mt-3 flex justify-end gap-2">
      <button type="button" class="btn" onclick={onclose} disabled={busy}>取消</button>
      <button type="submit" class="btn btn-primary" disabled={busy || !title.trim()}>
        {busy ? '保存中…' : '保存'}
      </button>
    </div>
  </form>
</div>
