// The whole setup of the statistics view, one value: what the page URL carries (`?m=&g=&s=&p=&w=&t=&n=&f.<key>=`),
// what a dashboard instance stores in its settings (the same keys), and what the API is asked (§3.1, §3.2).
// Anything malformed falls back to the default instead of failing the page: links are typed and pasted by hand.
import type { FilterKey, QuotaWindow, TokenKind, UsageDimension, UsageMetric } from "./types";

/** `quota` is the «Subscription limits» view: another endpoint, the same page. */
export type StatsView = UsageMetric | "quota";
export type PresetPeriod = "1d" | "7d" | "30d";
/** A preset, or whole days `from..to` (inclusive, the query's time zone). */
export type StatsPeriod = PresetPeriod | { from: string; to: string };

export interface StatsState {
  m: StatsView;
  t: TokenKind;
  g: UsageDimension;
  s: UsageDimension | null;
  p: StatsPeriod;
  w: QuotaWindow;
  /** Rows before «Others» (API `top`, 1..20). */
  n: number;
  f: Partial<Record<FilterKey, string[]>>;
}

export const VIEWS: readonly StatsView[] = ["cost", "tokens", "quota", "turns", "duration", "cards_done"];
export const TOKEN_KINDS: readonly TokenKind[] = ["total", "in", "out", "cache_read", "cache_write"];
export const DIMENSIONS: readonly UsageDimension[] = ["model", "provider", "account", "agent", "project", "workspace"];
export const WINDOWS: readonly QuotaWindow[] = ["5h", "week", "month", "budget"];
export const PRESETS: readonly PresetPeriod[] = ["1d", "7d", "30d"];
export const FILTER_KEYS: readonly FilterKey[] = ["project", "agent", "model", "provider", "account", "billing_mode", "workspace"];
/** Rows before «Others» when nothing is asked: five, as in the mockup, so the palette never repeats a colour. */
export const DEFAULT_TOP = 5;
export const MAX_TOP = 20;
export const TZ = "Europe/Moscow";

export const DEFAULT_STATE: StatsState = { m: "cost", t: "total", g: "model", s: null, p: "7d", w: "week", n: DEFAULT_TOP, f: {} };

const ISO_DAY = /^\d{4}-\d{2}-\d{2}$/;
const isDay = (v: string) => ISO_DAY.test(v) && !Number.isNaN(Date.parse(`${v}T00:00:00Z`)) && new Date(`${v}T00:00:00Z`).toISOString().slice(0, 10) === v;
const pick = <T extends string>(all: readonly T[], v: unknown, def: T): T => (typeof v === "string" && (all as readonly string[]).includes(v) ? (v as T) : def);

export function parsePeriod(v: unknown): StatsPeriod {
  if (typeof v !== "string") return DEFAULT_STATE.p;
  if ((PRESETS as readonly string[]).includes(v)) return v as PresetPeriod;
  const [from, to = from, extra] = v.split("..");
  if (extra !== undefined) return DEFAULT_STATE.p;
  if (from && to && isDay(from) && isDay(to) && from <= to) return { from, to };
  return DEFAULT_STATE.p;
}

export const periodParam = (p: StatsPeriod): string => (typeof p === "string" ? p : p.from === p.to ? p.from : `${p.from}..${p.to}`);

/** Reads a state from any key → value(s) source: URL search params or an instance's settings. */
export function readState(get: (key: string) => unknown, getAll: (key: string) => string[]): StatsState {
  const m = pick(VIEWS, get("m"), DEFAULT_STATE.m);
  const g = pick(DIMENSIONS, get("g"), DEFAULT_STATE.g);
  const sRaw = get("s");
  const s = typeof sRaw === "string" && (DIMENSIONS as readonly string[]).includes(sRaw) && sRaw !== g ? (sRaw as UsageDimension) : null;
  const nRaw = Number(get("n"));
  const n = Number.isInteger(nRaw) && nRaw >= 1 && nRaw <= MAX_TOP ? nRaw : DEFAULT_TOP;
  const f: StatsState["f"] = {};
  for (const k of FILTER_KEYS) {
    const vals = [...new Set(getAll(`f.${k}`).flatMap((v) => v.split(",")).map((v) => v.trim()).filter(Boolean))];
    if (vals.length) f[k] = vals;
  }
  return { m, t: pick(TOKEN_KINDS, get("t"), DEFAULT_STATE.t), g, s, p: parsePeriod(get("p")), w: pick(WINDOWS, get("w"), DEFAULT_STATE.w), n, f };
}

export function stateFromSearch(sp: URLSearchParams): StatsState {
  return readState((k) => sp.get(k) ?? undefined, (k) => sp.getAll(k));
}

/** An instance's settings (the same keys; a filter is an array or a comma list). */
export function stateFromSettings(settings: Record<string, unknown> | undefined): StatsState {
  const s = settings ?? {};
  return readState(
    (k) => s[k],
    (k) => {
      const v = s[k];
      if (Array.isArray(v)) return v.filter((x): x is string => typeof x === "string");
      return typeof v === "string" ? [v] : [];
    },
  );
}

/** The page URL's query: defaults are left out, so a plain link stays short. Order is stable (copy-paste diffs). */
export function stateToSearch(st: StatsState): URLSearchParams {
  const sp = new URLSearchParams();
  sp.set("m", st.m);
  if (st.m === "tokens" && st.t !== "total") sp.set("t", st.t);
  if (st.m !== "quota") sp.set("g", st.g);
  if (st.m !== "quota" && st.s) sp.set("s", st.s);
  sp.set("p", periodParam(st.p));
  if (st.m === "quota") sp.set("w", st.w);
  if (st.n !== DEFAULT_TOP) sp.set("n", String(st.n));
  for (const k of FILTER_KEYS) for (const v of st.f[k] ?? []) sp.append(`f.${k}`, v);
  return sp;
}

/** The same state as an instance's settings (the dashboard widget). */
export function stateToSettings(st: StatsState): Record<string, unknown> {
  const out: Record<string, unknown> = { m: st.m, g: st.g, p: periodParam(st.p), w: st.w };
  if (st.t !== "total") out.t = st.t;
  if (st.s) out.s = st.s;
  if (st.n !== DEFAULT_TOP) out.n = st.n;
  for (const k of FILTER_KEYS) if (st.f[k]?.length) out[`f.${k}`] = [...st.f[k]!];
  return out;
}

const API_FILTER: Record<FilterKey, string> = {
  project: "project_id",
  agent: "agent_id",
  model: "model",
  provider: "provider",
  account: "account",
  billing_mode: "billing_mode",
  workspace: "workspace_id",
};

export const isUserUsage = (st: StatsState): boolean => st.g === "workspace" || st.s === "workspace" || Boolean(st.f.workspace?.length);

function periodQuery(q: URLSearchParams, p: StatsPeriod) {
  if (typeof p === "string") q.set("period", p);
  else {
    q.set("from", p.from);
    q.set("to", p.to);
  }
  q.set("step", "auto");
  q.set("tz", TZ);
}

/** Query of GET …/analytics/usage (§3.1). A filter repeats its key (`model=a&model=b`). */
export function usageQuery(st: StatsState, extra: Record<string, string> = {}): URLSearchParams {
  const metric: UsageMetric = st.m === "quota" ? "cost" : st.m;
  const q = new URLSearchParams();
  q.set("metric", metric);
  if (metric === "tokens") q.set("tokens", st.t);
  q.set("group_by", st.g);
  if (st.s && st.s !== st.g) q.set("stack_by", st.s);
  periodQuery(q, st.p);
  q.set("top", String(st.n));
  for (const k of FILTER_KEYS) for (const v of st.f[k] ?? []) q.append(API_FILTER[k], v);
  for (const [k, v] of Object.entries(extra)) q.set(k, v);
  return q;
}

/** Query of GET …/analytics/quota (§3.2): window, period and the account filter only. */
export function quotaQuery(st: StatsState, extra: Record<string, string> = {}): URLSearchParams {
  const q = new URLSearchParams();
  q.set("window", st.w);
  periodQuery(q, st.p);
  for (const v of st.f.account ?? []) q.append("account", v);
  for (const [k, v] of Object.entries(extra)) q.set(k, v);
  return q;
}

/** Adds (or replaces with) one filter value: «Only this» on a row. */
export function withFilter(st: StatsState, key: FilterKey, value: string): StatsState {
  return { ...st, f: { ...st.f, [key]: [value] } };
}

export function withoutFilter(st: StatsState, key: FilterKey, value?: string): StatsState {
  const rest = value === undefined ? [] : (st.f[key] ?? []).filter((v) => v !== value);
  const f = { ...st.f };
  if (rest.length) f[key] = rest;
  else delete f[key];
  return { ...st, f };
}

/** The filter key a breakdown dimension narrows. */
export const filterOf = (d: UsageDimension): FilterKey => d;

/** «Who spent it» on a pool: this account's tokens by agent over the same period. */
export function whoSpent(st: StatsState, account: string): StatsState {
  return { ...st, m: "tokens", t: "total", g: "agent", s: null, f: { account: [account] } };
}

/** A click on a column keeps that day (the step turns to hours). `bucket` is the column's ISO start. */
export function onlyDay(st: StatsState, bucket: string): StatsState {
  const day = bucket.slice(0, 10);
  return isDay(day) ? { ...st, p: { from: day, to: day } } : st;
}

/** Number of days a period spans (a preset counts its length). */
export function periodDays(p: StatsPeriod): number {
  if (typeof p === "string") return p === "1d" ? 1 : p === "7d" ? 7 : 30;
  return Math.round((Date.parse(`${p.to}T00:00:00Z`) - Date.parse(`${p.from}T00:00:00Z`)) / 86_400_000) + 1;
}
