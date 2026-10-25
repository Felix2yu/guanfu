// 图标名联合类型。单独放一个模块：Svelte 组件的实例脚本里不能 `export`，
// 类型又是编译期产物，混在组件里既报错风险也读不出好处。
export type IconName =
  | 'overview'
  | 'day'
  | 'calendar'
  | 'timeline'
  | 'settings'
  | 'search'
  | 'sync'
  | 'download'
  | 'chevron-left'
  | 'chevron-right'
  | 'check'
  | 'up'
  | 'down'
  | 'flat'
  | 'clock'
  | 'external'
  | 'plus';
