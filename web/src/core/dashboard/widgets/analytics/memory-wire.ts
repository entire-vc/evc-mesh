// The answer of GET /api/v1/workspaces/:ws/analytics/memory exactly as the U5 server sends it
// (internal/service/memory_analytics_types.go, doc memory-usage-widget-stage1 §6), and the one adapter from it into
// the model the widget draws (memory-types.ts). A null stays null with its reason, never a 0: the root `reasons` map
// keys a KPI by its name ("useful_rate"), the comparison by "previous_kpis.<name>", the base by "quality" (no snapshot
// at all) or "quality.<name>", coverage by "coverage.<name>". Rows and days carry their own `reasons`.
import type {
  MemoryAnalytics,
  MemoryDay,
  MemoryDetailItem,
  MemoryDetailKind,
  MemoryDetailPage,
  MemoryGroup,
  MemoryKpis,
  MemoryQuality,
  MemoryReasons,
  MemoryRow,
  MemoryTotals,
} from "./memory-types";

type N = number | null;
type Reasons = Record<string, string>;

export interface WireKpis {
  reads: N;
  writes: N;
  with_reason: N;
  hit_rate: N;
  useful_rate: N;
  miss_rate: N;
  dead30: N;
  tokens_est: N;
  superseded_served_rate: N;
}

export interface WireSeriesPoint {
  day: string;
  reads: N;
  reads_by_op: Record<string, number> | null;
  served_rows_by_source: Record<string, number> | null;
  useful_rate: N;
  writes: N;
  with_reason: N;
  hits: N;
  useful: N;
  reasons?: Reasons;
}

export interface WireRow {
  key: string | null;
  label: string;
  key2: string | null;
  label2?: string;
  reads: N;
  hits: N;
  useful: N;
  writes: N;
  with_reason: N;
  hit_rate: N;
  useful_rate: N;
  tokens_est: N;
  served_rows: N;
  superseded_served_rate: N;
  reasons?: Reasons;
}

export interface WireQuality {
  day: string | null;
  total: N;
  active: N;
  archived: N;
  superseded_active: N;
  conflicted: N;
  no_kind: N;
  statuslog_like: N;
  dup_groups: N;
  dup_rows: N;
  dead30: N;
  dead90: N;
}

export interface WireMemoryAnalytics {
  period: { from: string; to: string };
  previous_period: { from: string; to: string };
  kpis: WireKpis;
  previous_kpis: WireKpis;
  series: WireSeriesPoint[];
  rows: WireRow[];
  totals: WireRow;
  quality: WireQuality;
  coverage: { journal_since: string | null; unknown_share: N; caller_kind: { caller_kind: string; reads: number }[] | null };
  reasons: Reasons | null;
}

export interface WireEmptyQuery {
  query_hash: string;
  query_text: string | null;
  redacted: boolean;
  reason?: string;
  agent_id: string | null;
  agent_name: string | null;
  count: number;
  last_at: string;
}

export interface WireRecordRow {
  memory_id: string;
  key: string | null;
  redacted: boolean;
  scope: string;
  kind: string | null;
  project_id: string | null;
  agent_id: string | null;
  created_at: string;
  served: number;
  useful: number;
  last_served_at: string | null;
  group_id?: string;
  group_size?: number;
}

export interface WireUnknownSource {
  route: string;
  user_agent: string;
  reads: number;
  sample_keys: string[] | null;
}

export interface WireList<T> {
  items: T[] | null;
  total: number;
  page: number;
  page_size: number;
  reason?: string;
  coverage?: { detail_since: string | null; reason?: string };
}

// -- adapter ------------------------------------------------------------------------------------------------

const KPI_NAMES = ["reads", "writes", "with_reason", "hit_rate", "useful_rate", "miss_rate", "dead30", "tokens_est", "superseded_served_rate"] as const;

/** The reasons of one section: `<prefix><name>` of the root map, without the prefix. */
function section(root: Reasons, prefix: string, names: readonly string[], whole?: string): MemoryReasons {
  const out: MemoryReasons = {};
  for (const n of names) {
    const r = root[prefix + n] ?? whole;
    if (r) out[n] = r;
  }
  return out;
}

const share = (part: N, whole: N): N => (part == null || whole == null || whole === 0 ? null : part / whole);

function kpisOf(k: WireKpis, root: Reasons, prefix: string, extra: Partial<MemoryKpis> = {}): MemoryKpis {
  return {
    ...k,
    tokens_per_read: share(k.tokens_est, k.reads),
    ...extra,
    reasons: section(root, prefix, KPI_NAMES),
  };
}

function rowOf(r: WireRow, agentKey: boolean): MemoryRow {
  return {
    key: r.key ?? "",
    label: r.label,
    agent_id: agentKey && r.key ? r.key : null,
    key2: r.key2,
    label2: r.label2 || null,
    reads: r.reads,
    writes: r.writes,
    with_reason: r.with_reason,
    hits: r.hits,
    useful: r.useful,
    hit_rate: r.hit_rate,
    useful_rate: r.useful_rate,
    tokens_est: r.tokens_est,
    superseded_served_rate: r.superseded_served_rate,
    reads_per_write: share(r.reads, r.writes),
    reasons: { ...r.reasons, ...(r.reads == null || !r.writes ? { reads_per_write: r.reasons?.reads ?? r.reasons?.writes ?? "no_data" } : {}) },
  };
}

/**
 * The period's last day, inclusive (YYYY-MM-DD). The server sends an exclusive end: a closed period ends at the next
 * day's midnight (a custom 01–10.11 comes back as `to` 11.11T00:00Z), an open one at now.
 */
export function lastDay(to: string): string {
  const t = Date.parse(to);
  if (Number.isNaN(t)) return to.slice(0, 10);
  const midnight = t % 86_400_000 === 0;
  return new Date(midnight ? t - 86_400_000 : t).toISOString().slice(0, 10);
}

/** The server's answer as the widget draws it: derived numbers are computed here, once, and keep their reasons. */
export function fromWire(w: WireMemoryAnalytics, groupBy: MemoryGroup): MemoryAnalytics {
  const root = w.reasons ?? {};
  const series: MemoryDay[] = w.series.map((p) => ({
    date: p.day,
    reads: p.reads,
    reads_by_op: p.reads_by_op ?? {},
    served_rows_by_source: p.served_rows_by_source ?? {},
    useful_rate: p.useful_rate,
    reasons: p.reasons ?? {},
  }));
  const readsByOp: Record<string, number> = {};
  for (const p of w.series) for (const [op, n] of Object.entries(p.reads_by_op ?? {})) readsByOp[op] = (readsByOp[op] ?? 0) + n;
  const q = w.quality;
  const qWhole = root.quality;
  const qReasons = section(root, "quality.", ["active", "superseded_active", "conflicted", "no_kind", "statuslog_like", "dup_groups", "dup_rows", "dead30"], qWhole);
  const withReasonRate = share(w.kpis.with_reason, w.kpis.writes);
  if (withReasonRate == null) qReasons.with_reason_rate = root.writes ?? root.with_reason ?? "no_data";
  const quality: MemoryQuality = {
    active: q.active,
    superseded_active: q.superseded_active,
    conflicted: q.conflicted,
    no_kind: q.no_kind,
    statuslog_like: q.statuslog_like,
    dup_groups: q.dup_groups,
    dup_rows: q.dup_rows,
    with_reason_rate: withReasonRate,
    reasons: qReasons,
  };
  const t = w.totals;
  const kpis = kpisOf(w.kpis, root, "", {
    reads_by_op: readsByOp,
    hits: t.hits,
    useful: t.useful,
    dead30_count: q.dead30,
  });
  const prevNull = KPI_NAMES.every((n) => w.previous_kpis[n] == null);
  const totals: MemoryTotals = rowOf(t, false);
  return {
    period: { from: w.period.from, to: lastDay(w.period.to) },
    previous_period: prevNull ? null : { ...w.previous_period, kpis: kpisOf(w.previous_kpis, root, "previous_kpis.") },
    kpis,
    series,
    rows: w.rows.map((r) => rowOf(r, groupBy === "agent")),
    totals,
    quality,
    coverage: {
      journal_since: w.coverage.journal_since,
      unknown_share: w.coverage.unknown_share,
      unknown_share_reason: root["coverage.unknown_share"],
      caller_kind: Object.fromEntries((w.coverage.caller_kind ?? []).map((c) => [c.caller_kind, c.reads])),
    },
  };
}

/** One page of a detail list, whatever its row type, as the dialog draws it. */
export function detailFromWire(kind: MemoryDetailKind, w: WireList<unknown>): MemoryDetailPage {
  const rows = w.items ?? [];
  let items: MemoryDetailItem[];
  if (kind === "empty-queries") {
    items = (rows as WireEmptyQuery[]).map((r) => ({
      id: `${r.query_hash}:${r.agent_id ?? ""}`,
      label: r.query_text,
      redacted: r.redacted,
      reason: r.reason || undefined,
      count: r.count,
      at: r.last_at,
      extra: r.agent_name ? [r.agent_name] : [],
    }));
  } else if (kind === "unknown-sources") {
    items = (rows as WireUnknownSource[]).map((r) => ({
      id: `${r.route}|${r.user_agent}`,
      label: r.route,
      count: r.reads,
      extra: [r.user_agent, (r.sample_keys ?? []).join(", ")].filter(Boolean),
    }));
  } else {
    items = (rows as WireRecordRow[]).map((r) => ({
      id: r.memory_id,
      label: r.key,
      key: r.key,
      redacted: r.redacted,
      kind: r.kind,
      scope: r.scope,
      count: kind === "duplicates" ? (r.group_size ?? null) : kind === "dead-records" ? null : r.served,
      useful: kind === "top-records" ? r.useful : undefined,
      at: kind === "top-records" ? r.last_served_at : r.created_at,
      extra: [],
    }));
  }
  return { items, total: w.total, page: w.page, page_size: w.page_size, reason: w.reason || undefined, coverage: w.coverage };
}
