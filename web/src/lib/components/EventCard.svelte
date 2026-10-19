<script lang="ts">
  import {
    fmtAmount,
    fmtTime,
    SOURCE_COLOR,
    SOURCE_META,
    TYPE_LABEL,
    type Event,
    type MediaRef
  } from '../api.ts';

  let { event }: { event: Event } = $props();

  const meta = $derived(
    SOURCE_META[event.source] ?? { label: event.source, dot: 'bg-zinc-400', text: 'text-ink-3' }
  );
  // 源色点与总览环图用同一套变量，时间轴上认颜色=在环图里认颜色。
  const dotColor = $derived(SOURCE_COLOR[event.source] ?? 'var(--gf-ink-3)');
  const typeLabel = $derived(TYPE_LABEL[event.event_type] ?? event.event_type);
  const isDay = $derived(event.time_precision === 'day');
  // 优先服务端下发的本地时刻（GUANFU_TZ），仅在缺失时回退浏览器时区。
  const timeText = $derived(event.local_time ?? fmtTime(event.occurred_at));
  const amountText = $derived(
    event.amount_cents
      ? `${event.detail?.type === 'income' ? '+' : '−'}¥${fmtAmount(event.amount_cents)}`
      : ''
  );

  // 封面（video_watch）：取第一张带代理路径的媒体；加载失败则隐藏。
  const cover = $derived(
    event.event_type === 'video_watch'
      ? (event.media ?? []).find((m: MediaRef) => m.kind === 'cover' && m.proxy_path)
      : undefined
  );
  let coverFailed = $state(false);

  // 演出评分（performance）：整星展示。
  const rating = $derived(
    event.event_type === 'performance' ? Number(event.detail?.rating ?? 0) : 0
  );

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

  // 习惯打卡（habit_check）：习惯名 chips，次数 >1 显示 ×n。
  const habitChips = $derived.by(() => {
    if (event.event_type !== 'habit_check' || !event.detail) return [];
    const list = event.detail.habits;
    if (!Array.isArray(list)) return [];
    return (list as { name?: string; count?: number }[])
      .filter((h) => h.name)
      .map((h) => ({ name: String(h.name), count: Number(h.count ?? 1) }));
  });

  // 到访（location_visit）：无地名时展示坐标兜底。
  const coordText = $derived(
    event.event_type === 'location_visit' &&
      !event.location_name &&
      (event.latitude || event.longitude)
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

  // ---------- 照片（photo 按天聚合 / plant_diary 单张配图） ----------
  const photos = $derived(
    event.event_type === 'photo' || event.event_type === 'plant_diary'
      ? (event.media ?? []).filter((m: MediaRef) => m.proxy_path)
      : []
  );
  // 园艺日记一天通常只有一两张，用大格；照片聚合日可能几十张，用小格密排。
  const photoCols = $derived(event.event_type === 'plant_diary' ? 'grid-cols-4' : 'grid-cols-8');
  // 网格最多展示 32 张，其余经灯箱浏览（懒加载，避免一天上百张全渲染）。
  const photoGrid = $derived(photos.slice(0, 32));
  const photoMore = $derived(photos.length - photoGrid.length);
  // 回源失败的格子整格不渲染（visibility:hidden 会留下一个空格子，比少一张更刺眼）。
  // 下标与 photoGrid 对齐；photoMore 仍按原始数量算，它描述的是"还有几张在灯箱里"。
  let failed = $state<Record<number, boolean>>({});
  const visiblePhotos = $derived(photoGrid.map((m, i) => ({ m, i })).filter((t) => !failed[t.i]));
  let lightbox = $state(-1);

  function openLightbox(i: number) {
    lightbox = i;
  }
  function closeLightbox() {
    lightbox = -1;
  }
  function stepLightbox(delta: number) {
    if (lightbox < 0) return;
    const n = photos.length;
    let next = lightbox;
    // 跳过取不到的那张；全都取不到就没什么可看的，直接关。
    for (let k = 0; k < n; k++) {
      next = (next + delta + n) % n;
      if (!failed[next]) {
        lightbox = next;
        return;
      }
    }
    closeLightbox();
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
  class="relative flex items-baseline gap-3 rounded-lg px-3 py-2 transition-colors hover:bg-chip"
>
  <span class="mt-[7px] h-2 w-2 shrink-0 rounded-full" style:background={dotColor}></span>

  <span class="w-11 shrink-0 text-[11px] text-ink-3 tabular-nums">
    {#if isDay}
      <span class="rounded bg-chip px-1.5 py-0.5 text-[10px] font-medium text-ink-2">全天</span>
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
      <span class="truncate font-medium text-ink">{event.title}</span>
      {#if amountText}
        <span
          class="shrink-0 text-[13px] font-semibold tabular-nums {event.detail?.type === 'income'
            ? 'text-pos'
            : 'text-neg'}"
        >
          {amountText}
        </span>
      {/if}
      {#if rating > 0}
        <span class="shrink-0 text-[11px] tracking-tight text-ink-2" aria-label="评分 {rating} 星">
          {'★'.repeat(Math.min(rating, 5))}{#if rating < 5}<span class="text-line-strong"
              >{'★'.repeat(5 - rating)}</span
            >{/if}
        </span>
      {/if}
      {#if event.location_name}
        <span class="shrink-0 truncate text-[11px] text-ink-3">📍 {event.location_name}</span>
      {:else if coordText}
        <span class="shrink-0 truncate text-[11px] text-ink-3 tabular-nums">📍 {coordText}</span>
      {/if}
    </div>
    {#if event.summary && event.summary !== event.title}
      <div class="truncate text-[11px] text-ink-3">{event.summary}</div>
    {/if}
    {#if diary?.lines}
      <div class="mt-1 line-clamp-3 text-[11px] leading-relaxed whitespace-pre-line text-ink-2">
        {diary.lines}
      </div>
    {/if}
    {#if healthChips.length > 0}
      <div class="mt-1 flex flex-wrap gap-1">
        {#each healthChips as chip (chip.key)}
          <span class="rounded bg-chip px-1.5 py-0.5 text-[10px] text-ink-2 tabular-nums">
            {chip.label}
            {chip.display}
          </span>
        {/each}
      </div>
    {/if}
    {#if habitChips.length > 0}
      <div class="mt-1 flex flex-wrap gap-1">
        {#each habitChips as chip (chip.name)}
          <span class="rounded bg-chip px-1.5 py-0.5 text-[10px] text-ink-2">
            {chip.name}{#if chip.count > 1}<span class="ml-0.5 text-ink-3 tabular-nums"
                >×{chip.count}</span
              >{/if}
          </span>
        {/each}
      </div>
    {/if}
    {#if visiblePhotos.length > 0 || photoMore > 0}
      <!-- 当日照片网格：懒加载 + 点击灯箱；视频素材带 ▶ 标；取不到的那张整格不出现。 -->
      <div class="mt-1.5 grid {photoCols} gap-1">
        {#each visiblePhotos as t (t.m.id)}
          <button
            type="button"
            class="relative aspect-square overflow-hidden rounded bg-chip"
            onclick={() => openLightbox(t.i)}
            title="查看照片"
          >
            <img
              src={t.m.proxy_path}
              alt=""
              class="h-full w-full object-cover"
              loading="lazy"
              onerror={() => (failed[t.i] = true)}
            />
            {#if t.m.kind === 'video'}
              <span
                class="absolute right-0.5 bottom-0.5 text-[9px] leading-none text-white drop-shadow"
                >▶</span
              >
            {/if}
          </button>
        {/each}
        {#if photoMore > 0}
          <button
            type="button"
            class="flex aspect-square items-center justify-center rounded bg-chip text-[11px] font-medium text-ink-2 tabular-nums"
            onclick={() => openLightbox(photoGrid.length)}
          >
            +{photoMore}
          </button>
        {/if}
      </div>
    {/if}
  </div>

  <span class="shrink-0 text-[11px] text-ink-3">
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
    {#if failed[lightbox]}
      <!-- 灯箱里这张也取不到：给一句人话，不摆破图图标。 -->
      <div
        class="flex max-h-[80vh] items-center rounded bg-zinc-800/70 px-8 py-16 text-sm text-zinc-300"
      >
        这张图没取到，源站返回了错误
      </div>
    {:else}
      <img
        src={(photos[lightbox].proxy_path ?? '') + '?size=preview'}
        alt=""
        class="max-h-[80vh] max-w-full rounded object-contain"
        onerror={() => (failed[lightbox] = true)}
      />
    {/if}
    <div class="mt-3 flex items-center gap-4 text-sm text-white">
      <button
        class="rounded-full border border-white/40 px-3 py-1 hover:bg-white/10"
        onclick={() => stepLightbox(-1)}>←</button
      >
      <span class="tabular-nums">{lightbox + 1} / {photos.length}</span>
      <button
        class="rounded-full border border-white/40 px-3 py-1 hover:bg-white/10"
        onclick={() => stepLightbox(1)}>→</button
      >
      <button
        class="rounded-full border border-white/40 px-3 py-1 hover:bg-white/10"
        onclick={closeLightbox}>关闭</button
      >
    </div>
  </div>
{/if}
