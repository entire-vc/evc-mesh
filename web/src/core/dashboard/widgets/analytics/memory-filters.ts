// Filter of the «Память Mesh» widget: the instance settings give its starting point, the header changes it in place.
// Period and project are the analytics widgets' own (readPeriod, analyticsParams), so the memory widget asks the same
// span the others on the dashboard ask; the rest are the §6 parameters of /analytics/memory.
import { previousPeriod, readPeriod } from "./filters";
import { analyticsParams, type AnalyticsPeriod } from "./use-analytics";
import {
  CALLER_KINDS,
  MEMORY_DETAILS,
  MEMORY_GROUPS,
  MEMORY_OPS,
  MEMORY_SOURCES,
  type CallerKind,
  type MemoryDetailKind,
  type MemoryGroup,
  type MemoryOp,
  type MemorySource,
} from "./memory-types";

export type MemoryView = "table" | "top" | "chart";
export const MEMORY_VIEWS: readonly MemoryView[] = ["chart", "top", "table"];

export interface MemoryFilter {
  period: AnalyticsPeriod;
  projectId?: string;
  /** Agent ids; empty = every agent. */
  agents: string[];
  op: MemoryOp;
  /** Empty = every source. */
  sources: MemorySource[];
  /** Empty = every caller. */
  callers: CallerKind[];
  groupBy: MemoryGroup;
  groupBy2?: MemoryGroup;
  view: MemoryView;
}

/** The widget opens on a week of the model's own reads, split by agent, as a table (plan §7, mockup v3). */
export const MEMORY_DEFAULT_PERIOD: AnalyticsPeriod = { days: 7 };
export const MEMORY_DEFAULT_CALLERS: CallerKind[] = ["agent_tool"];

const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string" && x !== "") : []);
const oneOf = <T extends string>(all: readonly T[], v: unknown): T | undefined => (all as readonly unknown[]).includes(v) ? (v as T) : undefined;
const someOf = <T extends string>(all: readonly T[], v: unknown): T[] => [...new Set(strings(v))].filter((x): x is T => (all as readonly string[]).includes(x));

/** The instance settings as a filter; anything missing or malformed falls back to its default. */
export function readMemoryFilter(settings: Record<string, unknown> | undefined): MemoryFilter {
  const s = settings ?? {};
  const groupBy = oneOf(MEMORY_GROUPS, s.group_by) ?? "agent";
  const groupBy2 = oneOf(MEMORY_GROUPS, s.group_by2);
  const f: MemoryFilter = {
    period: s.period === undefined ? MEMORY_DEFAULT_PERIOD : readPeriod(s.period),
    agents: [...new Set(strings(s.agents))],
    op: oneOf(MEMORY_OPS, s.op) ?? "all",
    sources: someOf(MEMORY_SOURCES, s.sources),
    // a non-array is a broken setting, not "every caller": back to the default like a missing one
    callers: Array.isArray(s.callers) ? someOf(CALLER_KINDS, s.callers) : [...MEMORY_DEFAULT_CALLERS],
    groupBy,
    view: oneOf(MEMORY_VIEWS, s.view) ?? "table",
  };
  if (groupBy2 && groupBy2 !== groupBy) f.groupBy2 = groupBy2;
  if (typeof s.project === "string" && s.project !== "") f.projectId = s.project;
  return f;
}

/** Request parameters: the analytics period and project, then the §6 filters (lists comma-separated). */
export function memoryParams(f: MemoryFilter, now = new Date()): Record<string, string> {
  const out = analyticsParams({ period: f.period, projectId: f.projectId }, now);
  if (f.agents.length) out.agent_id = [...f.agents].sort().join(",");
  if (f.op !== "all") out.op = f.op;
  if (f.sources.length) out.source = [...f.sources].sort().join(",");
  if (f.callers.length) out.caller_kind = [...f.callers].sort().join(",");
  out.group_by = f.groupBy;
  if (f.groupBy2) out.group_by2 = f.groupBy2;
  return out;
}

/**
 * The data request: the §6 parameters plus the comparison window `prev_from`/`prev_to`, computed by the same
 * `previousPeriod` as the other analytics widgets, so the deltas «к прошлому» compare the window they compare.
 * The CSV and the detail lists take `memoryParams` alone: they have no comparison.
 */
export function memoryDataParams(f: MemoryFilter, now = new Date()): Record<string, string> {
  const prev = previousPeriod(f.period, now);
  // previousPeriod ends on a day; the memory API reads a bare date as that day's midnight (exclusive), so the last
  // day is sent to its end, as analyticsParams sends a custom period's `to`.
  return { ...memoryParams(f, now), prev_from: prev.from, prev_to: `${prev.to}T23:59:59Z` };
}

/** How many filters differ from the defaults: the count on the narrow «Фильтры · N» button. */
export function activeFilterCount(f: MemoryFilter): number {
  const callersDefault = f.callers.length === MEMORY_DEFAULT_CALLERS.length && f.callers.every((c) => MEMORY_DEFAULT_CALLERS.includes(c));
  return [Boolean(f.projectId), f.agents.length > 0, f.op !== "all", f.sources.length > 0, !callersDefault, f.groupBy !== "agent" || Boolean(f.groupBy2)].filter(Boolean).length;
}

/** The CSV of the same selection (§6 `…/memory/export?format=csv`). */
export function memoryExportRequest(wsId: string, f: MemoryFilter): { path: string; params: Record<string, string> } {
  return { path: `/api/v1/workspaces/${wsId}/analytics/memory/export`, params: { ...memoryParams(f), format: "csv" } };
}

export const isDetailKind = (v: string): v is MemoryDetailKind => (MEMORY_DETAILS as readonly string[]).includes(v);

/** Toggle one value in a list filter. */
export function toggle<T>(list: readonly T[], v: T): T[] {
  return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
}
