<script lang="ts">
  // 线性图标集合：20×20 视框、1.6 描边、圆头端点，统一 currentColor 上色。
  // 单独抽一个组件是为了让侧栏/顶栏/卡片里的图标尺寸与笔画完全一致
  // （图标一旦各自为政，界面立刻显廉价）。
  import type { IconName } from '../icons.ts';

  let { name, size = 18, cls = '' }: { name: IconName; size?: number; cls?: string } = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 20 20"
  fill="none"
  class={cls}
  aria-hidden="true"
  focusable="false"
>
  {#if name === 'overview'}
    <rect
      x="2.75"
      y="2.75"
      width="6"
      height="6"
      rx="1.6"
      stroke="currentColor"
      stroke-width="1.6"
    />
    <rect
      x="11.25"
      y="2.75"
      width="6"
      height="6"
      rx="1.6"
      stroke="currentColor"
      stroke-width="1.6"
    />
    <rect
      x="2.75"
      y="11.25"
      width="6"
      height="6"
      rx="1.6"
      stroke="currentColor"
      stroke-width="1.6"
    />
    <rect
      x="11.25"
      y="11.25"
      width="6"
      height="6"
      rx="1.6"
      stroke="currentColor"
      stroke-width="1.6"
    />
  {:else if name === 'day'}
    <rect
      x="2.75"
      y="3.75"
      width="14.5"
      height="13.5"
      rx="2.2"
      stroke="currentColor"
      stroke-width="1.6"
    />
    <path d="M2.75 7.75H17.25" stroke="currentColor" stroke-width="1.6" />
    <circle cx="10" cy="12.25" r="1.9" fill="currentColor" />
  {:else if name === 'calendar'}
    <rect
      x="2.75"
      y="3.75"
      width="14.5"
      height="13.5"
      rx="2.2"
      stroke="currentColor"
      stroke-width="1.6"
    />
    <path d="M2.75 7.75H17.25" stroke="currentColor" stroke-width="1.6" />
    <path
      d="M6.25 11.75H13.75M6.25 14.75H11.25"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
    />
  {:else if name === 'timeline'}
    <path d="M6.5 2.75V17.25" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
    <circle cx="6.5" cy="6.25" r="1.9" fill="currentColor" />
    <circle cx="6.5" cy="13.75" r="1.9" fill="currentColor" />
    <path
      d="M10.75 6.25H17.25M10.75 13.75H17.25"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
    />
  {:else if name === 'settings'}
    <circle cx="10" cy="10" r="2.7" stroke="currentColor" stroke-width="1.6" />
    <path
      d="M10 2.6V4.6M10 15.4V17.4M2.6 10H4.6M15.4 10H17.4M4.9 4.9L6.2 6.2M13.8 13.8L15.1 15.1M15.1 4.9L13.8 6.2M6.2 13.8L4.9 15.1"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
    />
  {:else if name === 'search'}
    <circle cx="8.9" cy="8.9" r="5.7" stroke="currentColor" stroke-width="1.6" />
    <path d="M13.1 13.1L17 17" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
  {:else if name === 'sync'}
    <path
      d="M16.4 9.6C16.4 13 13.6 15.8 10.2 15.8C6.8 15.8 4 13 4 9.6C4 6.6 6.2 4.1 9.1 3.5"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
    />
    <path
      d="M16.2 3.2V6.8H12.6"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  {:else if name === 'download'}
    <path
      d="M10 3V12.4M10 12.4L6.4 8.8M10 12.4L13.6 8.8"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
    <path
      d="M3.6 15.2V16.2C3.6 16.9 4.2 17.5 4.9 17.5H15.1C15.8 17.5 16.4 16.9 16.4 16.2V15.2"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
    />
  {:else if name === 'chevron-left'}
    <path
      d="M12.2 4.6L6.8 10L12.2 15.4"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  {:else if name === 'chevron-right'}
    <path
      d="M7.8 4.6L13.2 10L7.8 15.4"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  {:else if name === 'check'}
    <path
      d="M4.2 10.4L8 14.2L15.8 5.8"
      stroke="currentColor"
      stroke-width="1.9"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  {:else if name === 'up'}
    <path
      d="M10 15.6V4.4M10 4.4L6 8.4M10 4.4L14 8.4"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  {:else if name === 'down'}
    <path
      d="M10 4.4V15.6M10 15.6L6 11.6M10 15.6L14 11.6"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  {:else if name === 'flat'}
    <path d="M4.6 10H15.4" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
  {:else if name === 'clock'}
    <circle cx="10" cy="10" r="7.2" stroke="currentColor" stroke-width="1.6" />
    <path
      d="M10 6.2V10.2L12.8 11.8"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
    />
  {/if}
</svg>
