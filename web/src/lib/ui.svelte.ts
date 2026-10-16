// 界面偏好（主题 / 视图）：存 localStorage，不落库。
//
// 为什么不放后端：这两项是"这台设备怎么看"的偏好，不是数据。放服务端就得建表、
// 建写接口，还会把无鉴权的 /api/v1 多开一个写入口。

export type Theme = 'system' | 'light' | 'dark';
export type ViewMode = 'day' | 'calendar' | 'timeline';

const THEME_KEY = 'guanfu.theme';
const VIEW_KEY = 'guanfu.view';

export const THEME_LABELS: Record<Theme, string> = {
  system: '跟随系统',
  light: '浅色',
  dark: '深色'
};

export const VIEW_LABELS: Record<ViewMode, string> = {
  day: '当日',
  calendar: '日历',
  timeline: '时间线'
};

export const ui = $state({
  theme: 'system' as Theme,
  view: 'day' as ViewMode,
  // 系统当前是不是深色（跟随系统时用于显示"当前实际生效的是哪套"）。
  systemDark: false
});

let media: MediaQueryList | undefined;

function isTheme(v: string | null): v is Theme {
  return v === 'system' || v === 'light' || v === 'dark';
}

function isView(v: string | null): v is ViewMode {
  return v === 'day' || v === 'calendar' || v === 'timeline';
}

function applyTheme(): void {
  if (typeof document === 'undefined') return;
  const dark = ui.theme === 'dark' || (ui.theme === 'system' && ui.systemDark);
  document.documentElement.classList.toggle('dark', dark);
}

/** 在 onMount 里调用一次：读回偏好并接管系统深浅色变化。 */
export function initUI(): void {
  if (typeof window === 'undefined') return;
  try {
    const t = localStorage.getItem(THEME_KEY);
    if (isTheme(t)) ui.theme = t;
    const v = localStorage.getItem(VIEW_KEY);
    if (isView(v)) ui.view = v;
  } catch {
    // 隐私模式/被禁用：用默认值即可，不该让页面起不来。
  }
  media = window.matchMedia('(prefers-color-scheme: dark)');
  ui.systemDark = media.matches;
  media.addEventListener('change', onSystemChange);
  applyTheme();
}

export function disposeUI(): void {
  media?.removeEventListener('change', onSystemChange);
}

function onSystemChange(e: MediaQueryListEvent): void {
  ui.systemDark = e.matches;
  applyTheme();
}

export function setTheme(t: Theme): void {
  ui.theme = t;
  try {
    localStorage.setItem(THEME_KEY, t);
  } catch {}
  applyTheme();
}

export function setView(v: ViewMode): void {
  ui.view = v;
  try {
    localStorage.setItem(VIEW_KEY, v);
  } catch {}
}

/** 当前实际生效的深浅色（跟随系统时给出系统值）。
    注意必须是函数：模块不能导出 derived state（构建期 rolldown 直接报错）。 */
export function effectiveDark(): boolean {
  return ui.theme === 'dark' || (ui.theme === 'system' && ui.systemDark);
}
