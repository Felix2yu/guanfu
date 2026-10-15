// 观复 API 客户端。与 Go 后端 /api/v1 对应（internal/api/server.go）。

export interface MediaRef {
  id: string;
  kind: string;
  width?: number;
  height?: number;
  proxy_path?: string;
}

export interface Event {
  id: number;
  source: string;
  external_id: string;
  event_type: string;
  time_precision: 'second' | 'day';
  occurred_at: number;
  local_date: string;
  /** 服务端按 GUANFU_TZ 计算的本地时刻（HH:MM），前端直接用，不信任浏览器时区 */
  local_time?: string;
  title: string;
  summary?: string;
  amount_cents?: number;
  currency?: string;
  latitude?: number;
  longitude?: number;
  location_name?: string;
  media?: MediaRef[];
  detail?: Record<string, unknown>;
  source_url?: string;
}

export interface SourceState {
  name: string;
  enabled: boolean;
  interval_secs: number;
  cursor: string;
  last_sync_at: number;
  last_error: string;
  last_count: number;
}

async function get<T>(path: string): Promise<T> {
  const resp = await fetch(path, { headers: { Accept: 'application/json' } });
  if (!resp.ok) throw new Error(`API ${resp.status}: ${await resp.text()}`);
  const body = (await resp.json()) as { code: number; data: T; message?: string };
  if (body.code !== 0) throw new Error(body.message || `API code=${body.code}`);
  return body.data;
}

export function fetchEvents(date: string): Promise<Event[]> {
  return get<Event[]>(`/api/v1/events?date=${encodeURIComponent(date)}&limit=500`);
}

export function fetchRange(): Promise<{ min: string; max: string }> {
  return get<{ min: string; max: string }>('/api/v1/range');
}

/** 日历热力图数据源：范围内每日事件总数 { 'YYYY-MM-DD': n } */
export function fetchCountsRange(from: string, to: string): Promise<Record<string, number>> {
  return get<Record<string, number>>(`/api/v1/events/counts?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`);
}

/** 月份工具：'YYYY-MM' ↔ 该月首尾日期 */
export function monthOf(date: string): string {
  return date.slice(0, 7);
}

export function monthStart(month: string): string {
  return month + '-01';
}

export function monthEnd(month: string): string {
  const [y, m] = month.split('-').map(Number);
  const last = new Date(y, m, 0).getDate();
  return `${month}-${String(last).padStart(2, '0')}`;
}

export function shiftMonth(month: string, delta: number): string {
  const [y, m] = month.split('-').map(Number);
  const d = new Date(y, m - 1 + delta, 1);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
}

/** 月历格子：周一起始，返回当月各日日期串（含前置 null 占位）。 */
export function monthGrid(month: string): (string | null)[] {
  const [y, m] = month.split('-').map(Number);
  const first = new Date(y, m - 1, 1);
  const days = new Date(y, m, 0).getDate();
  const lead = (first.getDay() + 6) % 7; // 周一=0
  const cells: (string | null)[] = Array.from({ length: lead }, () => null);
  for (let d = 1; d <= days; d++) {
    cells.push(`${month}-${String(d).padStart(2, '0')}`);
  }
  while (cells.length % 7 !== 0) cells.push(null);
  return cells;
}

export function fetchSources(): Promise<SourceState[]> {
  return get<SourceState[]>('/api/v1/sources');
}

export function triggerSync(source: string): Promise<void> {
  return fetch(`/api/v1/sync/${encodeURIComponent(source)}`, { method: 'POST' }).then(() => {});
}

// 源展示名与主题色（Tailwind 任意值类名，浅色/深色均克制）。
export const SOURCE_META: Record<string, { label: string; dot: string; text: string }> = {
  huozhi: { label: '记账', dot: 'bg-rose-500', text: 'text-rose-600 dark:text-rose-400' },
  mujian: { label: '演出', dot: 'bg-violet-500', text: 'text-violet-600 dark:text-violet-400' },
  diarum: { label: '日记', dot: 'bg-amber-500', text: 'text-amber-600 dark:text-amber-400' },
  zhichang: { label: '健康', dot: 'bg-emerald-500', text: 'text-emerald-600 dark:text-emerald-400' },
  dawarich: { label: '足迹', dot: 'bg-sky-500', text: 'text-sky-600 dark:text-sky-400' },
  immich: { label: '照片', dot: 'bg-fuchsia-500', text: 'text-fuchsia-600 dark:text-fuchsia-400' },
  'bili-history': { label: '观影', dot: 'bg-cyan-500', text: 'text-cyan-600 dark:text-cyan-400' },
  qiansi: { label: '人情', dot: 'bg-orange-500', text: 'text-orange-600 dark:text-orange-400' },
  shenshi: { label: '任务', dot: 'bg-lime-600', text: 'text-lime-700 dark:text-lime-400' }
};

// 事件类型中文标签。
export const TYPE_LABEL: Record<string, string> = {
  transaction: '记账',
  performance: '演出',
  diary: '日记',
  health_day: '健康',
  health_sleep: '睡眠',
  health_workout: '运动',
  photo: '照片',
  location_visit: '到访',
  location_day: '足迹',
  video_watch: '观影',
  social_event: '人情',
  task_done: '任务'
};

export function fmtTime(ts: number): string {
  const d = new Date(ts * 1000);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

export function fmtAmount(cents?: number): string {
  if (!cents) return '';
  return (cents / 100).toFixed(2);
}

export function todayStr(): string {
  return fmtDate(new Date());
}

export function fmtDate(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

export function shiftDate(date: string, days: number): string {
  const d = new Date(date + 'T00:00:00');
  d.setDate(d.getDate() + days);
  return fmtDate(d);
}

export function fmtWeekday(date: string): string {
  const d = new Date(date + 'T00:00:00');
  return ['日', '一', '二', '三', '四', '五', '六'][d.getDay()];
}
