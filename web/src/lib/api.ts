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
  // 连接配置（设置页用）。后端只给状态与 env 变量名，永不下发凭据值。
  base_url?: string;
  base_url_env?: string;
  auth?: AuthKind;
  credential_env?: string;
  credential_set?: boolean;
  configured?: boolean;
}

export type AuthKind = 'none' | 'api_key' | 'bearer' | 'basic' | 'jwt' | 'token';

export const AUTH_LABELS: Record<AuthKind, string> = {
  none: '无需凭据',
  api_key: 'API Key 请求头',
  bearer: 'Bearer Token',
  basic: 'Basic（用户名:密码）',
  jwt: 'JWT（可填 用户名:密码 或 API token）',
  token: 'Token（源侧未配则放通）'
};

/** 紧凑场景（表格单元格）用的短标签；完整说明留给编辑器下拉与 title 提示。 */
export const AUTH_SHORT_LABELS: Record<AuthKind, string> = {
  none: '无需凭据',
  api_key: 'API Key',
  bearer: 'Bearer',
  basic: 'Basic',
  jwt: 'JWT / API token',
  token: 'Token'
};

/**
 * 统一请求封装。错误优先取后端给的 message——设置页保存失败时要显示
 * 「地址必须以 http:// 开头」这种具体原因，而不是 `API 400: {...}`。
 */
export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const resp = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  const text = await resp.text();
  let parsed: { code?: number; data?: T; message?: string } | null = null;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    // 网关错误页等非 JSON 响应：下面按状态码报错。
  }
  if (!resp.ok) {
    throw new Error(parsed?.message || `API ${resp.status}: ${text.slice(0, 200)}`);
  }
  if (!parsed) throw new Error(`API 返回非 JSON: ${text.slice(0, 200)}`);
  if (parsed.code !== 0) throw new Error(parsed.message || `API code=${parsed.code}`);
  return parsed.data as T;
}

async function get<T>(path: string): Promise<T> {
  return request<T>('GET', path);
}

export function fetchEvents(date: string): Promise<Event[]> {
  return get<Event[]>(`/api/v1/events?date=${encodeURIComponent(date)}&limit=500`);
}

/** 时间线视图：跨日取一段事件（按 occurred_at 升序）。 */
export function fetchEventsRange(
  from: string,
  to: string,
  limit = 200,
  offset = 0
): Promise<Event[]> {
  const q = new URLSearchParams({ from, to, limit: String(limit), offset: String(offset) });
  return get<Event[]>(`/api/v1/events?${q.toString()}`);
}

export function fetchRange(): Promise<{ min: string; max: string }> {
  return get<{ min: string; max: string }>('/api/v1/range');
}

// ---------- 总览聚合 ----------

/** 单一币种下的收支合计（分）。口径与时间轴顶栏一致：detail.type==income 记收入，其余带金额记支出。 */
export interface MoneyTotals {
  income_cents: number;
  expense_cents: number;
}

/** 一个聚合分组（按天或按来源）。 */
export interface StatRow {
  key: string;
  count: number;
  totals: Record<string, MoneyTotals>;
}

export interface Overview {
  from: string;
  to: string;
  total_count: number;
  totals: Record<string, MoneyTotals>;
  /** 逐日序列（稀疏：只含有事件的日期） */
  days: StatRow[];
  /** 来源分布（按条数倒序） */
  sources: StatRow[];
}

/** 总览页聚合：区间合计、逐日收支、来源分布一次取回。 */
export function fetchOverview(from: string, to: string): Promise<Overview> {
  const q = new URLSearchParams({ from, to });
  return get<Overview>(`/api/v1/overview?${q.toString()}`);
}

// ---------- 时间范围 ----------

export type RangeMode = 'today' | 'week' | 'month' | 'year';

export const RANGE_LABELS: Record<RangeMode, string> = {
  today: '今日',
  week: '本周',
  month: '近 30 天',
  year: '今年'
};

/** 周一起始（中文习惯的「本周」）。 */
export function mondayOf(date: string): string {
  const d = new Date(date + 'T00:00:00');
  d.setDate(d.getDate() - ((d.getDay() + 6) % 7));
  return fmtDate(d);
}

/**
 * 当前范围对应的闭区间。
 * 「本周」按自然周（周一至今）算，不是「最近 7 天」—— 标签写了周就按周给，
 * 否则周一打开会看到上周的数据，跟标签对不上。
 */
export function rangeFor(mode: RangeMode, today = todayStr()): { from: string; to: string } {
  switch (mode) {
    case 'today':
      return { from: today, to: today };
    case 'week':
      return { from: mondayOf(today), to: today };
    case 'month':
      return { from: shiftDate(today, -29), to: today };
    case 'year':
      return { from: `${today.slice(0, 4)}-01-01`, to: today };
  }
}

/** 闭区间天数（含首尾）。 */
export function dayCount(from: string, to: string): number {
  const a = new Date(from + 'T00:00:00').getTime();
  const b = new Date(to + 'T00:00:00').getTime();
  return Math.round((b - a) / 86400000) + 1;
}

/** 逐日补零：把稀疏的聚合结果铺成连续日期，图表才能按天等距绘制。 */
export function fillDays(from: string, to: string, rows: StatRow[]): StatRow[] {
  const byKey = new Map(rows.map((r) => [r.key, r]));
  const out: StatRow[] = [];
  const n = Math.max(dayCount(from, to), 1);
  for (let i = 0; i < n; i++) {
    const date = shiftDate(from, i);
    out.push(byKey.get(date) ?? { key: date, count: 0, totals: {} });
  }
  return out;
}

/** 日历热力图数据源：范围内每日事件总数 { 'YYYY-MM-DD': n } */
export function fetchCountsRange(from: string, to: string): Promise<Record<string, number>> {
  return get<Record<string, number>>(
    `/api/v1/events/counts?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`
  );
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

/** 触发某源同步（source=all 表示全部启用源）。非 2xx 时抛出后端给的具体原因。 */
export function triggerSync(source: string): Promise<void> {
  return request<void>('POST', `/api/v1/sync/${encodeURIComponent(source)}`).then(() => {});
}

// 源展示名与主题色（Tailwind 任意值类名，浅色/深色均克制）。
export const SOURCE_META: Record<string, { label: string; dot: string; text: string }> = {
  huozhi: { label: '记账', dot: 'bg-rose-500', text: 'text-rose-600 dark:text-rose-400' },
  mujian: { label: '演出', dot: 'bg-violet-500', text: 'text-violet-600 dark:text-violet-400' },
  diarum: { label: '日记', dot: 'bg-amber-500', text: 'text-amber-600 dark:text-amber-400' },
  zhichang: {
    label: '健康',
    dot: 'bg-emerald-500',
    text: 'text-emerald-600 dark:text-emerald-400'
  },
  dawarich: { label: '足迹', dot: 'bg-sky-500', text: 'text-sky-600 dark:text-sky-400' },
  immich: { label: '照片', dot: 'bg-fuchsia-500', text: 'text-fuchsia-600 dark:text-fuchsia-400' },
  'bili-history': { label: '观影', dot: 'bg-cyan-500', text: 'text-cyan-600 dark:text-cyan-400' },
  qiansi: { label: '人情', dot: 'bg-orange-500', text: 'text-orange-600 dark:text-orange-400' },
  shenshi: { label: '任务', dot: 'bg-lime-600', text: 'text-lime-700 dark:text-lime-400' },
  liuxia: { label: '天象', dot: 'bg-indigo-500', text: 'text-indigo-600 dark:text-indigo-400' },
  qingye: { label: '园艺', dot: 'bg-teal-600', text: 'text-teal-700 dark:text-teal-400' }
};

/**
 * 数据源固定配色。值是 CSS 变量而不是十六进制：SVG 里直接写 stroke="var(--src-1)"
 * 就能跟着深浅色一起换，不用在 JS 里判断主题。
 */
export const SOURCE_COLOR: Record<string, string> = {
  huozhi: 'var(--src-1)',
  immich: 'var(--src-2)',
  dawarich: 'var(--src-3)',
  zhichang: 'var(--src-4)',
  mujian: 'var(--src-5)',
  diarum: 'var(--src-6)',
  qiansi: 'var(--src-7)',
  'bili-history': 'var(--src-8)',
  shenshi: 'var(--src-9)',
  liuxia: 'var(--src-10)',
  qingye: 'var(--src-11)'
};

/** 源的展示名（「记账 · 货殖」这种两段式由调用方自己拼）。 */
export function sourceLabel(name: string): string {
  return SOURCE_META[name]?.label ?? name;
}

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
  task_done: '任务',
  gift_money: '礼金',
  anniversary: '纪念日',
  review: '复盘',
  habit_check: '打卡',
  sky_forecast: '霞光',
  plant_diary: '园艺',
  plant_care: '养护'
};

export function fmtTime(ts: number): string {
  const d = new Date(ts * 1000);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

export function fmtAmount(cents?: number): string {
  if (!cents) return '';
  return (cents / 100).toFixed(2);
}

/** 金额（分 → ¥12,345.00）。总览卡片与图表轴共用，保证两处位数一致。 */
export function fmtMoney(cents: number): string {
  return (
    '¥' +
    (cents / 100).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  );
}

/** 紧凑金额（轴标签用）：¥980 / ¥1.2k / ¥12.3万。 */
export function fmtMoneyCompact(cents: number): string {
  const yuan = cents / 100;
  if (Math.abs(yuan) >= 10000) return `¥${(yuan / 10000).toFixed(1)}万`;
  if (Math.abs(yuan) >= 1000) return `¥${(yuan / 1000).toFixed(1)}k`;
  return `¥${Math.round(yuan).toLocaleString('zh-CN')}`;
}

/** 整数千分位（事件条数）。 */
export function fmtCount(n: number): string {
  return n.toLocaleString('zh-CN');
}

/** 相对时间：刚刚 / N 分钟前 / N 小时前 / 昨天 HH:MM / MM-DD HH:MM。 */
export function fmtAgo(unix: number, now = Date.now()): string {
  if (!unix) return '从未同步';
  const diff = Math.max(0, now - unix * 1000);
  const min = Math.floor(diff / 60000);
  if (min < 1) return '刚刚';
  if (min < 60) return `${min} 分钟前`;
  const hour = Math.floor(min / 60);
  if (hour < 24) return `${hour} 小时前`;
  const d = new Date(unix * 1000);
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
  if (hour < 48) return `昨天 ${hm}`;
  return `${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${hm}`;
}

/** '2026-10-19' → '10/19'（图表 X 轴）。 */
export function fmtMD(date: string): string {
  return `${Number(date.slice(5, 7))}/${Number(date.slice(8, 10))}`;
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
