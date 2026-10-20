// 界面偏好（主题 / 视图 / 字号）：存 localStorage，不落库。
//
// 为什么不放后端：这几项是"这台设备怎么看"的偏好，不是数据。放服务端就得建表、
// 建写接口，还会把无鉴权的 /api/v1 多开一个写入口。

export type Theme = 'system' | 'light' | 'dark';
export type ViewMode = 'overview' | 'day' | 'calendar' | 'timeline';
export type TextSize = 'small' | 'normal' | 'large' | 'xlarge';

const THEME_KEY = 'guanfu.theme';
const VIEW_KEY = 'guanfu.view';
const TEXT_SIZE_KEY = 'guanfu.textSize';

export const THEME_LABELS: Record<Theme, string> = {
  system: '跟随系统',
  light: '浅色',
  dark: '深色'
};

export const VIEW_LABELS: Record<ViewMode, string> = {
  overview: '总览',
  day: '当日',
  calendar: '日历',
  timeline: '时间线'
};

/** 侧栏/标签栏的顺序：总览在最前，设置单独走路由。 */
export const VIEW_ORDER: ViewMode[] = ['overview', 'day', 'calendar', 'timeline'];

export const TEXT_SIZE_LABELS: Record<TextSize, string> = {
  small: '小',
  normal: '标准',
  large: '大',
  xlarge: '特大'
};

export const TEXT_SIZE_ORDER: TextSize[] = ['small', 'normal', 'large', 'xlarge'];

export const ui = $state({
  theme: 'system' as Theme,
  view: 'overview' as ViewMode,
  textSize: 'normal' as TextSize,
  // 系统当前是不是深色（跟随系统时据此决定实际明暗）。
  systemDark: false
});

let media: MediaQueryList | undefined;

function isTheme(v: string | null): v is Theme {
  return v === 'system' || v === 'light' || v === 'dark';
}

function isView(v: string | null): v is ViewMode {
  return v === 'overview' || v === 'day' || v === 'calendar' || v === 'timeline';
}

function isTextSize(v: string | null): v is TextSize {
  return v === 'small' || v === 'normal' || v === 'large' || v === 'xlarge';
}

function applyTheme(): void {
  if (typeof document === 'undefined') return;
  const dark = ui.theme === 'dark' || (ui.theme === 'system' && ui.systemDark);
  document.documentElement.classList.toggle('dark', dark);
}

function applyTextSize(): void {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.textSize = ui.textSize;
}

/** 在 onMount 里调用一次：读回偏好并接管系统深浅色变化。 */
export function initUI(): void {
  if (typeof window === 'undefined') return;
  try {
    const t = localStorage.getItem(THEME_KEY);
    if (isTheme(t)) ui.theme = t;
    const v = localStorage.getItem(VIEW_KEY);
    if (isView(v)) ui.view = v;
    const s = localStorage.getItem(TEXT_SIZE_KEY);
    if (isTextSize(s)) ui.textSize = s;
  } catch {
    // 隐私模式/被禁用：用默认值即可，不该让页面起不来。
  }
  media = window.matchMedia('(prefers-color-scheme: dark)');
  ui.systemDark = media.matches;
  media.addEventListener('change', onSystemChange);
  applyTheme();
  applyTextSize();
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
  } catch {
    // 隐私模式下 localStorage 会抛，主题已在内存里生效，忽略
  }
  applyTheme();
}

export function setView(v: ViewMode): void {
  ui.view = v;
  try {
    localStorage.setItem(VIEW_KEY, v);
  } catch {
    // 同上：存不下去只是刷新后回到默认视图
  }
}

export function setTextSize(s: TextSize): void {
  ui.textSize = s;
  try {
    localStorage.setItem(TEXT_SIZE_KEY, s);
  } catch {
    // 同上：存不下去只是刷新后回到标准字号
  }
  applyTextSize();
}
