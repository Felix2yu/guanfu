<script lang="ts">
  // 开关：数据源的「启用」和通知的「同步失败时推送」必须是同一个形状。
  // 用 sr-only 的原生 checkbox 承载状态，键盘聚焦和高亮交给 peer-* 伪类。
  let {
    checked,
    label,
    onchange,
    disabled = false
  }: {
    checked: boolean;
    label: string;
    onchange: (v: boolean) => void;
    disabled?: boolean;
  } = $props();
</script>

<label class="flex h-[30px] shrink-0 cursor-pointer items-center">
  <input
    type="checkbox"
    class="peer sr-only"
    {checked}
    {disabled}
    aria-label={label}
    onchange={(e) => onchange(e.currentTarget.checked)}
  />
  <span
    aria-hidden="true"
    class="relative h-[18px] w-[32px] rounded-full transition-colors
      peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2
      peer-focus-visible:outline-[var(--gf-ink-3)]
      {checked ? 'bg-accent' : 'bg-line-strong'}
      {disabled ? 'opacity-50' : ''}"
  >
    <span
      class="absolute top-[2px] h-[14px] w-[14px] rounded-full shadow-sm
        transition-[left,background-color]
        {checked ? 'left-[16px] bg-accent-ink' : 'left-[2px] bg-ink-2'}"
    ></span>
  </span>
</label>
