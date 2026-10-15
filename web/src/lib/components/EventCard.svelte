<script lang="ts">
  import { fmtAmount, fmtTime, SOURCE_META, TYPE_LABEL, type Event, type MediaRef } from '../api';

  let { event }: { event: Event } = $props();

  const meta = $derived(SOURCE_META[event.source] ?? { label: event.source, dot: 'bg-zinc-400', text: 'text-zinc-500' });
  const typeLabel = $derived(TYPE_LABEL[event.event_type] ?? event.event_type);
  const isDay = $derived(event.time_precision === 'day');
  // 优先服务端下发的本地时刻（GUANFU_TZ），仅在缺失时回退浏览器时区。
  const timeText = $derived(event.local_time ?? fmtTime(event.occurred_at));
  const amountText = $derived(
    event.amount_cents ? `${event.detail?.type === 'income' ? '+' : '−'}¥${fmtAmount(event.amount_cents)}` : ''
  );

  // 封面（video_watch）：取第一张带代理路径的媒体；加载失败则隐藏。
  const cover = $derived(
    event.event_type === 'video_watch'
      ? (event.media ?? []).find((m: MediaRef) => m.kind === 'cover' && m.proxy_path)
      : undefined
  );
  let coverFailed = $state(false);

  // 演出评分（performance）：整星展示。
  const rating = $derived(event.event_type === 'performance' ? Number(event.detail?.rating ?? 0) : 0);

  // 健康日报（health_day）：指标 chips，最多 4 个（与服务端摘要一致）。
  const healthChips = $derived.by(() => {
    if (event.event_type !== 'health_day' || !event.detail) return [];
    return Object.entries(event.detail)
      .map(([key, v]) => ({
        key,
        label: String((v as { label?: string }).label ?? key),
        display: String((v as { display?: string }).display ?? '')
      }))
      .filter((c) => c.display)
      .slice(0, 4);
  });

  // 到访（location_visit）：无地名时展示坐标兜底。
  const coordText = $derived(
    event.event_type === 'location_visit' && !event.location_name && (event.latitude || event.longitude)
      ? `${event.latitude?.toFixed(4)}, ${event.longitude?.toFixed(4)}`
      : ''
  );

  // ---------- 日记（diary） ----------
  const MOOD_EMOJI = ['😞', '🙁', '😐', '🙂', '😄'];
  const diary = $derived.by(() => {
    if (event.event_type !== 'diary' || !event.detail) return null;
    const mood = Number(event.detail.mood ?? 0);
    // 正文前 3 行（服务端已剥 HTML 存 content_text）。
    const lines = String(event.detail.content_text ?? '')
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
      .slice(0, 3)
      .join('\n');
    return { emoji: mood >= 1 && mood <= 5 ? MOOD_EMOJI[mood - 1] : '', lines };
  });

  // ---------- 照片（photo，按天聚合） ----------
  const photos = $derived(
    event.event_type === 'photo' ? (event.media ?? []).filter((m: MediaRef) => m.proxy_path) : []
  );
  // 网格最多展示 32 张，其余经灯箱浏览（懒加载，避免一天上百张全渲染）。
  const photoGrid = $derived(photos.slice(0, 32));
  const photoMore = $derived(photos.length - photoGrid.length);
  let lightbox = $state(-1);

  function openLightbox(i: number) {
    lightbox = i;
  }
  function closeLightbox() {
    lightbox = -1;
  }
  function stepLightbox(delta: number) {
    if (lightbox < 0) return;
    lightbox = (lightbox + delta + photos.length) % photos.length;
  }
  function onKeydown(e: KeyboardEvent) {
    if (lightbox < 0) return;
    if (e.key === 'Escape') closeLightbox();
    else if (e.key === 'ArrowLeft') stepLightbox(-1);
    else if (e.key === 'ArrowRight') stepLightbox(1);
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- 紧凑单行事件卡：左侧源色点 + 时间（全天事件显示「全天」徽标）+ 标题/摘要。 -->
<div
  class="relative flex items-baseline gap-3 rounded-lg px-3 py-2 transition-colors hover:bg-black/[0.03] dark:hover:bg-white/[0.05]"
>
  <span class="mt-[7px] h-2 w-2 shrink-0 rounded-full {meta.dot}"></span>

  <span class="w-11 shrink-0 text-xs tabular-nums text-zinc-500 dark:text-zinc-400">
    {#if isDay}
      <span
        class="rounded bg-zinc-200/70 px-1.5 py-0.5 text-[11px] font-medium text-zinc-600 dark:bg-zinc-700/70 dark:text-zinc-300"
        >全天</span
      >
    {:else}
      {timeText}
    {/if}
  </span>

  {#if cover && !coverFailed}
    <img
      src={cover.proxy_path}
      alt=""
      class="mt-0.5 h-6 w-10 shrink-0 self-center rounded object-cover"
      loading="lazy"
      onerror={() => (coverFailed = true)}
    />
  {/if}

  <div class="min-w-0 flex-1">
    <div class="flex items-baseline gap-2">
      {#if diary?.emoji}
        <span class="shrink-0 text-sm" aria-label="心情">{diary.emoji}</span>
      {/if}
      <span class="truncate font-medium text-zinc-900 dark:text-zinc-100">{event.title}</span>
      {#if amountText}
        <span class="shrink-0 text-sm font-semibold tabular-nums {event.detail?.type === 'income' ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400'}">
          {amountText}
        </span>
      {/if}
      {#if rating > 0}
        <span class="shrink-0 text-xs tracking-tight text-amber-500" aria-label="评分 {rating} 星">
          {'★'.repeat(Math.min(rating, 5))}{#if rating < 5}<span class="text-zinc-300 dark:text-zinc-600">{'★'.repeat(5 - rating)}</span>{/if}
        </span>
      {/if}
      {#if event.location_name}
        <span class="shrink-0 truncate text-xs text-zinc-500 dark:text-zinc-400">📍 {event.location_name}</span>
      {:else if coordText}
        <span class="shrink-0 truncate text-xs tabular-nums text-zinc-400 dark:text-zinc-500">📍 {coordText}</span>
      {/if}
    </div>
    {#if event.summary && event.summary !== event.title}
      <div class="truncate text-xs text-zinc-500 dark:text-zinc-400">{event.summary}</div>
    {/if}
    {#if diary?.lines}
      <div class="mt-1 whitespace-pre-line line-clamp-3 text-xs leading-relaxed text-zinc-600 dark:text-zinc-300">
        {diary.lines}
      </div>
    {/if}
    {#if healthChips.length > 0}
      <div class="mt-1 flex flex-wrap gap-1">
        {#each healthChips as chip (chip.key)}
          <span class="rounded bg-zinc-100 px-1.5 py-0.5 text-[11px] tabular-nums text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300">
            {chip.label} {chip.display}
          </span>
        {/each}
      </div>
    {/if}
    {#if photoGrid.length > 0}
      <!-- 当日照片网格：懒加载 + 点击灯箱；视频素材带 ▶ 标。 -->
      <div class="mt-1.5 grid grid-cols-8 gap-1">
        {#each photoGrid as m, i (m.id)}
          <button
            type="button"
            class="relative aspect-square overflow-hidden rounded bg-zinc-100 dark:bg-zinc-800"
            onclick={() => openLightbox(i)}
            title="查看照片"
          >
            <img
              src={m.proxy_path}
              alt=""
              class="h-full w-full object-cover"
              loading="lazy"
              onerror={(e) => ((e.currentTarget as HTMLImageElement).style.visibility = 'hidden')}
            />
            {#if m.kind === 'video'}
              <span class="absolute bottom-0.5 right-0.5 text-[9px] leading-none text-white drop-shadow">▶</span>
            {/if}
          </button>
        {/each}
        {#if photoMore > 0}
          <button
            type="button"
            class="flex aspect-square items-center justify-center rounded bg-zinc-100 text-[11px] font-medium text-zinc-500 tabular-nums dark:bg-zinc-800 dark:text-zinc-400"
            onclick={() => openLightbox(photoGrid.length)}
          >
            +{photoMore}
          </button>
        {/if}
      </div>
    {/if}
  </div>

  <span class="shrink-0 text-[11px] {meta.text}">
    {#if typeLabel === meta.label}{typeLabel}{:else}{typeLabel} · {meta.label}{/if}
  </span>
</div>

<!-- 照片灯箱：preview 尺寸 + ←/→ 切换 + Esc 关闭。 -->
{#if lightbox >= 0 && photos[lightbox]}
  <div
    class="fixed inset-0 z-50 flex flex-col items-center justify-center bg-black/80 p-4"
    role="dialog"
    aria-modal="true"
    tabindex="-1"
    onkeydown={onKeydown}
    onclick={(e) => {
      if (e.currentTarget === e.target) closeLightbox();
    }}
  >
    <img
      src={(photos[lightbox].proxy_path ?? '') + '?size=preview'}
      alt=""
      class="max-h-[80vh] max-w-full rounded object-contain"
    />
    <div class="mt-3 flex items-center gap-4 text-sm text-white">
      <button class="rounded-full border border-white/40 px-3 py-1 hover:bg-white/10" onclick={() => stepLightbox(-1)}>←</button>
      <span class="tabular-nums">{lightbox + 1} / {photos.length}</span>
      <button class="rounded-full border border-white/40 px-3 py-1 hover:bg-white/10" onclick={() => stepLightbox(1)}>→</button>
      <button class="rounded-full border border-white/40 px-3 py-1 hover:bg-white/10" onclick={closeLightbox}>关闭</button>
    </div>
  </div>
{/if}
