// 通知不是一张表，而是从「同步状态」推出来的：观复本身没有消息中心，
// 唯一值得提醒用户的事就是某个源挂了、或者刚同步进来多少条。
// 与其造一堆假通知，不如把已有状态翻译成人话。
//
// 已读状态存 localStorage：它描述的是「这台设备上这个人看没看过」，
// 不是数据本身，没必要为此多开一个写接口。

import { sourceLabel, type SourceState } from './api.ts';

export interface Notice {
  /** 稳定 id：同一次同步（source + last_sync_at）是同一件事 */
  id: string;
  tone: 'error' | 'ok' | 'info';
  text: string;
  /** unix 秒；0 表示没有具体时刻（如「未配置」这类持续状态） */
  at: number;
}

const READ_KEY = 'guanfu.notices.read';

function firstLine(s: string, max: number): string {
  const line = (s || '').split('\n')[0].trim();
  return line.length > max ? line.slice(0, max - 1) + '…' : line;
}

/** 从数据源状态推导通知列表（按时间倒序，最多 6 条）。 */
export function deriveNotices(sources: SourceState[]): Notice[] {
  const out: Notice[] = [];

  for (const s of sources) {
    if (!s.configured) continue;
    if (s.last_error) {
      out.push({
        id: `${s.name}:${s.last_sync_at}:error`,
        tone: 'error',
        text: `${sourceLabel(s.name)}同步失败 · ${firstLine(s.last_error, 48)}`,
        at: s.last_sync_at
      });
    } else if (s.last_sync_at) {
      out.push({
        id: `${s.name}:${s.last_sync_at}:ok`,
        tone: 'ok',
        text: `${sourceLabel(s.name)}同步完成${s.last_count ? ` · 新增 ${s.last_count} 条` : ''}`,
        at: s.last_sync_at
      });
    }
  }

  const missing = sources.filter((s) => !s.configured);
  if (missing.length > 0) {
    out.push({
      id: 'unconfigured',
      tone: 'info',
      text: `${missing.length} 个数据源未配置：${missing.map((s) => sourceLabel(s.name)).join('、')}`,
      at: 0
    });
  }

  return out.sort((a, b) => b.at - a.at).slice(0, 6);
}

/** 读回已读集合。隐私模式下 localStorage 会抛，直接当全未读。
    用普通对象而不是 Set：Svelte 的响应式对「整体换新」的对象最省事，也不用引 SvelteSet。 */
export function loadRead(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(READ_KEY);
    if (!raw) return {};
    const arr = JSON.parse(raw) as unknown;
    const out: Record<string, boolean> = {};
    if (Array.isArray(arr)) {
      for (const x of arr) if (typeof x === 'string') out[x] = true;
    }
    return out;
  } catch {
    return {};
  }
}

export function saveRead(read: Record<string, boolean>): void {
  try {
    localStorage.setItem(READ_KEY, JSON.stringify(Object.keys(read)));
  } catch {
    // 存不下去只是下次刷新又变回未读，不影响使用
  }
}
