// Pure logic of Board Studio: no React, no SDK runtime, so it unit-tests without a Mesh host.
import copy from "./copy.json";

type Dict = Record<string, string>;
const dicts = copy as unknown as Record<string, Dict>;

/** Copy for a locale ("ru-RU" → ru); English for anything else, and for a key a locale lacks. */
export function say(locale: string | undefined, key: string, vars: Record<string, string | number> = {}): string {
  const lang = (locale ?? "en").slice(0, 2).toLowerCase();
  const raw = dicts[lang]?.[key] ?? dicts.en![key] ?? key;
  return raw.replace(/\{(\w+)\}/g, (_, k: string) => String(vars[k] ?? ""));
}

export const PRIORITIES = ["none", "low", "medium", "high", "urgent"] as const;
export type Prio = (typeof PRIORITIES)[number];

/** The next priority up; urgent stays urgent (the quick action is disabled there). */
export function raised(p: string): Prio {
  const i = PRIORITIES.indexOf(p as Prio);
  return PRIORITIES[Math.min(PRIORITIES.length - 1, Math.max(0, i) + 1)]!;
}

/** `YYYY-MM-DD` one day after `date` (an ISO date or datetime); today + 1 when there is none. */
export function nextDay(date: string | null | undefined, now: Date = new Date()): string {
  const base = date ? new Date(date) : now;
  const d = new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth(), base.getUTCDate() + 1));
  return d.toISOString().slice(0, 10);
}

export interface Lane {
  key: string;
  name: string;
  count: number;
}
export const NOBODY = "none";

interface HasAssignee {
  assignee: { id: string; name: string } | null;
}

/** People with cards on the board, busiest first, "unassigned" last; the filter strip is built from it. */
export function lanes(cards: HasAssignee[]): Lane[] {
  const by = new Map<string, Lane>();
  for (const c of cards) {
    const key = c.assignee?.id ?? NOBODY;
    const lane = by.get(key) ?? { key, name: c.assignee?.name ?? "", count: 0 };
    lane.count += 1;
    by.set(key, lane);
  }
  return [...by.values()].sort((a, b) => (a.key === NOBODY ? 1 : b.key === NOBODY ? -1 : b.count - a.count || a.name.localeCompare(b.name)));
}

/** Is `card` dimmed by the focus filter? No filter (null) dims nothing. */
export function dimmed(focus: string | null | undefined, assigneeId: string | null | undefined): boolean {
  if (!focus) return false;
  return (assigneeId ?? NOBODY) !== focus;
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  return (parts.length === 1 ? parts[0]!.slice(0, 2) : parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase();
}

/** WIP state of a band: ok, at the limit, or over it. No limit = no state. */
export function wip(count: number, limit: number | null | undefined): "none" | "ok" | "full" | "over" {
  if (!limit || limit <= 0) return "none";
  return count > limit ? "over" : count === limit ? "full" : "ok";
}

interface Dated {
  id: string;
  title: string;
  due_date?: string | null;
}

const DAY = 86_400_000;

/** Overdue first (oldest first), then what is due within `horizonDays` (soonest first). Tasks without a date are not focus. */
export function focus<T extends Dated>(tasks: T[], horizonDays: number, now: number = Date.now()): { overdue: T[]; soon: T[] } {
  const dated = tasks.filter((t) => !!t.due_date).map((t) => ({ t, at: new Date(t.due_date!).getTime() })).filter((x) => !Number.isNaN(x.at));
  const byDate = (a: { at: number }, b: { at: number }) => a.at - b.at;
  const startOfToday = now - (now % DAY);
  return {
    overdue: dated.filter((x) => x.at < startOfToday).sort(byDate).map((x) => x.t),
    soon: dated.filter((x) => x.at >= startOfToday && x.at < startOfToday + (horizonDays + 1) * DAY).sort(byDate).map((x) => x.t),
  };
}

/** The `focus_horizon_days` setting as a whole number in 1..30; 7 when absent or nonsense. */
export function horizon(v: unknown): number {
  const n = typeof v === "number" ? Math.round(v) : NaN;
  return Number.isFinite(n) && n >= 1 && n <= 30 ? n : 7;
}
