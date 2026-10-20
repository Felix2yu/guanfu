<script lang="ts">
  // 分段选择器：总览的时间范围、设置页的主题/字号共用一份。
  // 之前每页各抄一遍圆角和内边距，同一页里已经跑出 px-2.5 / px-3 两种尺寸。
  let {
    label,
    options,
    value,
    onchange
  }: {
    label: string;
    options: { value: string; label: string }[];
    value: string;
    onchange: (v: string) => void;
  } = $props();
</script>

<div class="flex rounded-[10px] bg-chip p-[3px]" role="group" aria-label={label}>
  {#each options as o (o.value)}
    <button
      type="button"
      class="rounded-[7px] px-3 py-1.5 fs-125 transition-colors
        {value === o.value
        ? 'bg-surface font-medium text-ink shadow-[0_1px_3px_rgba(27,29,34,0.08)]'
        : 'text-ink-2 hover:text-ink'}"
      aria-pressed={value === o.value}
      onclick={() => onchange(o.value)}
    >
      {o.label}
    </button>
  {/each}
</div>
