// The model the «Память Mesh» widget draws. The server's own answer (GET …/analytics/memory, U5, doc
// memory-usage-widget-stage1 §6) is typed in memory-wire.ts and adapted into this shape there, once. Rates are
// fractions 0..1. A metric the server cannot count is `null` with its reason in `reasons`, never 0: the widget shows
// «нет данных» and the reason, and keeps a real 0 a 0.

/** Why a metric is null: the read journal starts after the period, the signal is not collected yet, and so on. */
export type MemoryNullReason =
  | "no_journal_before"
  | "not_collected"
  | "not_applicable"
  | "excluded_by_filter"
  | "no_data"
  | "retention"
  | "text_expired"
  | "detail_timeout"
  | (string & {});

/** The `source` filter (§6): where a read's rows came from. */
export const MEMORY_SOURCES = ["memory", "project_knowledge", "project_update", "doc", "unknown"] as const;
export type MemorySource = (typeof MEMORY_SOURCES)[number];

/** Sources of served rows on a chart day (`served_rows_by_source`). */
export const SERVED_SOURCES = ["memory", "project_knowledge", "doc", "unknown"] as const;

/** Who made the call (journal `caller_kind`); `agent_tool` is the model's own tool call and the default filter. */
export const CALLER_KINDS = ["agent_tool", "runner", "script", "ui", "unknown"] as const;
export type CallerKind = (typeof CALLER_KINDS)[number];

/** Read operations, in the chart's stacking order (§7: the bars are read calls per day by operation). */
export const MEMORY_READ_OPS = ["recall", "recall_with_graph", "get_memory", "get_project_knowledge", "get_project_updates"] as const;

/** `op` filter: all, every read, every write, or one read operation. */
export const MEMORY_OPS = ["all", "read", "write", ...MEMORY_READ_OPS] as const;
export type MemoryOp = (typeof MEMORY_OPS)[number];

export const MEMORY_GROUPS = ["agent", "project", "op", "source", "caller_kind", "kind", "scope", "day"] as const;
export type MemoryGroup = (typeof MEMORY_GROUPS)[number];

export type MemoryReasons = Partial<Record<string, MemoryNullReason>>;

export interface MemoryKpis {
  /** Read calls of the filter without an error. */
  reads: number | null;
  /** Reads by operation, summed over the chart days. */
  reads_by_op?: Partial<Record<string, number>>;
  writes: number | null;
  /** Writes carrying a reason. */
  with_reason: number | null;
  /** Recalls with at least one non-pinned row. */
  hits?: number | null;
  hit_rate: number | null;
  /** Recalls whose answer was used (§3): a lower bound. */
  useful?: number | null;
  /** useful / hits. */
  useful_rate: number | null;
  miss_rate: number | null;
  /** Share of active records older than 30 days nobody was served in 30 days. */
  dead30: number | null;
  dead30_count?: number | null;
  /** response_chars / 4: an estimate. */
  tokens_est: number | null;
  tokens_per_read?: number | null;
  /** Superseded rows among served rows (no pinned): the noise of the answers. */
  superseded_served_rate: number | null;
  reasons?: MemoryReasons;
}

/** One day of the chart: read calls by operation, the rows they served by source, and the day's useful rate. */
export interface MemoryDay {
  date: string;
  /** null = the day is not measured (before the journal, or reads excluded): see `reasons`. */
  reads: number | null;
  reads_by_op: Partial<Record<string, number>>;
  served_rows_by_source: Partial<Record<string, number>>;
  useful_rate: number | null;
  reasons?: MemoryReasons;
}

/** One row of the breakdown by `group_by` (and `group_by2` when asked). */
export interface MemoryRow {
  key: string;
  label: string;
  /** The agent of the row when the split is by agent: its click filters the widget. */
  agent_id?: string | null;
  key2?: string | null;
  label2?: string | null;
  reads: number | null;
  writes: number | null;
  /** Writes carrying a reason; the kind/scope split supports writes only (§6 v10). */
  with_reason?: number | null;
  hits?: number | null;
  useful?: number | null;
  hit_rate: number | null;
  useful_rate: number | null;
  tokens_est: number | null;
  superseded_served_rate?: number | null;
  /** reads / writes, null without writes. */
  reads_per_write: number | null;
  reasons?: MemoryReasons;
}

export type MemoryTotals = Omit<MemoryRow, "key" | "label" | "agent_id" | "key2" | "label2">;

/** The base now (the last snapshot of the workspace or project in the period), not the period's flow. */
export interface MemoryQuality {
  active: number | null;
  superseded_active: number | null;
  conflicted: number | null;
  /** Active records without a `kind:` tag. */
  no_kind: number | null;
  /** Active records that look like a status log. */
  statuslog_like: number | null;
  dup_groups: number | null;
  dup_rows: number | null;
  /** Writes of the period that carry a reason. */
  with_reason_rate: number | null;
  reasons?: MemoryReasons;
}

export interface MemoryCoverage {
  /** First instant the read journal covers; null = no journal yet. */
  journal_since: string | null;
  unknown_share: number | null;
  unknown_share_reason?: MemoryNullReason;
  /** Reads of the period by caller_kind. */
  caller_kind?: Partial<Record<string, number>>;
}

export interface MemoryAnalytics {
  period: { from: string; to: string };
  /** KPIs of the request's `prev_from`..`prev_to` window (filters.ts `previousPeriod`); null = not computed. */
  previous_period: { from: string; to: string; kpis: MemoryKpis } | null;
  kpis: MemoryKpis;
  series: MemoryDay[];
  rows: MemoryRow[];
  totals: MemoryTotals | null;
  quality: MemoryQuality;
  coverage: MemoryCoverage;
}

/** Detail lists (`…/analytics/memory/<kind>`): paged, searchable, some sortable. */
export const MEMORY_DETAILS = ["empty-queries", "top-records", "dead-records", "duplicates", "unknown-sources"] as const;
export type MemoryDetailKind = (typeof MEMORY_DETAILS)[number];

export interface MemoryDetailItem {
  id: string;
  /** Memory key, or the query text, or the route; null when hidden from this viewer or expired. */
  label: string | null;
  /** Opens the record in the memory browser (`/memories?q=<key>`). */
  key?: string | null;
  /** The text is hidden from this viewer (another agent's private query or record). */
  redacted?: boolean;
  reason?: MemoryNullReason;
  /** Served (top records), group size (duplicates), calls (empty queries, unknown sources); null = not counted. */
  count: number | null;
  /** Used answers of a top record. */
  useful?: number;
  /** Last served or asked (top records, empty queries), created (dead records, duplicates). */
  at?: string | null;
  /** Second-line facts as the server names them: agent, user agent, sample keys. */
  extra: string[];
  /** Current attributes of a record (not historical attributes of the recall). */
  kind?: string | null;
  scope?: string | null;
}

export interface MemoryDetailPage {
  items: MemoryDetailItem[];
  total: number;
  page: number;
  page_size: number;
  /** Why the list has no data at all. */
  reason?: MemoryNullReason;
  coverage?: { detail_since: string | null; reason?: MemoryNullReason };
}
