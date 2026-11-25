// Wire types of the agent-usage statistics (plan doc a9781091 §3.1 usage, §3.2 quota). A value the server could not
// count is `null`, never 0: the views show «no sum» / «no data» for it.

export type UsageMetric = "cost" | "tokens" | "turns" | "duration" | "cards_done";
export type TokenKind = "total" | "in" | "out" | "cache_read" | "cache_write";
export type UsageDimension = "model" | "provider" | "account" | "agent" | "project" | "workspace";
export type QuotaWindow = "5h" | "week" | "month" | "budget";
export type UsageStep = "hour" | "day" | "week";
/** Filter keys of the page URL (`f.<key>=`) and of the API (`<key>[]`). */
export type FilterKey = "project" | "agent" | "model" | "provider" | "account" | "billing_mode" | "workspace";

export interface UsageSeries {
  key: string;
  label: string;
  values: (number | null)[];
  turns_unknown?: number[];
}

export interface UsageRow {
  key: string;
  label: string;
  stack_key?: string | null;
  stack_label?: string | null;
  value: number | null;
  share?: number | null;
  turns: number;
  turns_unknown: number;
  tokens_in?: number | null;
  tokens_out?: number | null;
  cache_read?: number | null;
  cache_write?: number | null;
  cost?: number | null;
  cost_basis?: "price" | "reported" | "mixed" | null;
  duration_ms?: number | null;
  link?: { kind: "agent" | "project" | "account" | "workspace"; id: string } | null;
  /** The server's «Others» row (top-N cut). */
  other_count?: number;
}

export interface UsageTotals {
  value: number | null;
  turns: number;
  turns_unknown: number;
  tokens_in?: number | null;
  tokens_out?: number | null;
  cache_read?: number | null;
  cache_write?: number | null;
  cost?: number | null;
}

export interface ModelPrice {
  model: string;
  valid_from: string;
  input_per_mtok: number;
  output_per_mtok: number;
  cache_read_per_mtok?: number;
  cache_write_per_mtok?: number;
  source_url?: string;
}

export interface UsageCapabilities {
  group_by: UsageDimension[];
  cost: boolean;
  quota: boolean;
  cards_done_group_by?: UsageDimension[];
}

export interface UsageResponse {
  query: { metric: UsageMetric; group_by: UsageDimension; stack_by: UsageDimension | null; from: string; to: string; step: UsageStep; tz: string };
  buckets: string[];
  series: UsageSeries[];
  rows: UsageRow[];
  totals: UsageTotals;
  pricing?: { label?: string; models: ModelPrice[]; no_price_models?: string[] } | null;
  coverage?: { by_bucket: { turns: number; turns_known: number }[]; history_precision_until?: string | null } | null;
  capabilities: UsageCapabilities;
}

/** Structured no-data reason the quota API sends: code + ready human text + other windows' shares. */
export interface QuotaNoDataReason {
  code: "no_window" | "no_data" | string;
  text: string;
  other_windows?: { window: string; window_raw?: string; now?: number | null; status?: string }[];
}

export interface QuotaRow {
  account: string;
  window_raw?: string | null;
  /** Share of the window used now, 0..1; null = no data. */
  now: number | null;
  reset_at?: string | null;
  peak?: number | null;
  hours_at_limit?: number | null;
  status: string;
  observed_at?: string | null;
  /** Why a pool has no data for this window («no "week" window: 5h — 71%, month — 33%»): the API sends an object, fixtures used a plain string. */
  reason?: string | QuotaNoDataReason | null;
  /** Every other window of the account, also on a row that has the chosen one. */
  other_windows?: QuotaNoDataReason["other_windows"];
}

export interface QuotaResponse {
  query: { window: QuotaWindow; from: string; to: string; step: UsageStep; tz: string };
  buckets: string[];
  series: { key: string; label: string; values: (number | null)[] }[];
  rows: QuotaRow[];
  capabilities?: { quota: boolean };
}
