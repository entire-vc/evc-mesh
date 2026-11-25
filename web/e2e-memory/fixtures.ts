// The harness's API answers, typed by the widget's wire contract (memory-wire.ts). Their key sets are
// checked against u5-golden.json — the U5 server's own serialization of internal/service/memory_analytics_types.go,
// every field set — by the vitest suite (memory-widget.test.tsx), so a fixture cannot drift from the API unnoticed.
// No Playwright import: vitest loads this module too.
import type { WireEmptyQuery, WireKpis, WireList, WireMemoryAnalytics, WireRecordRow, WireRow, WireUnknownSource } from "../src/core/dashboard/widgets/analytics/memory-wire";

export const day = (offset: number) => new Date(Date.now() + offset * 86_400_000).toISOString().slice(0, 10);

export const row = (id: string, label: string, over: Partial<WireRow> = {}): WireRow => ({
  key: id,
  label,
  key2: null,
  reads: id === "a1" ? 50 : 34,
  hits: id === "a1" ? 40 : 23,
  useful: id === "a1" ? 28 : 12,
  writes: id === "a1" ? 20 : 10,
  with_reason: id === "a1" ? 16 : 8,
  hit_rate: id === "a1" ? 0.8 : 0.65,
  useful_rate: id === "a1" ? 0.7 : 0.5,
  tokens_est: id === "a1" ? 8000 : 4000,
  served_rows: id === "a1" ? 90 : 40,
  superseded_served_rate: 0.05,
  reasons: {},
  ...over,
});

export const kpis: WireKpis = {
  reads: 84,
  writes: 30,
  with_reason: 24,
  hit_rate: 0.75,
  useful_rate: 0.6,
  miss_rate: 0.25,
  dead30: 0.4,
  tokens_est: 12_000,
  superseded_served_rate: 0.05,
};

/** The answer to GET …/analytics/memory: the period length and the agent filter are visible in the numbers. */
export function mainPayload(p: URLSearchParams): WireMemoryAnalytics {
  const from = p.get("from") ?? day(-7);
  const days = Math.max(1, Math.floor((Date.now() - Date.parse(from)) / 86_400_000));
  const only = (p.get("agent_id") ?? "").split(",")[0];
  const k = only ? { ...kpis, useful_rate: 0.7 } : kpis;
  return {
    period: { from: `${from.slice(0, 10)}T00:00:00Z`, to: new Date().toISOString() },
    previous_period: { from: p.get("prev_from") ?? day(-14), to: p.get("prev_to") ?? day(-7) },
    kpis: { ...k, reads: days * 12 },
    previous_kpis: { ...kpis, reads: 70, useful_rate: 0.5 },
    series: Array.from({ length: days }, (_, i) => ({
      day: day(i - days + 1),
      reads: 12,
      reads_by_op: { recall: 8, recall_with_graph: 0, get_memory: 4, get_project_knowledge: 0, get_project_updates: 0 },
      served_rows_by_source: { memory: 20, project_knowledge: 0, doc: 5, unknown: 0 },
      useful_rate: 0.6,
      writes: 4,
      with_reason: 3,
      hits: 6,
      useful: 4,
      reasons: {},
    })),
    rows: only ? [row(only, only === "a1" ? "Liam" : "Maya")] : [row("a1", "Liam"), row("a2", "Maya")],
    totals: row("total", "total", { reads: days * 12, hits: 63, useful: 38, writes: 30, with_reason: 24, hit_rate: 0.75, useful_rate: 0.6, tokens_est: 12_000 }),
    quality: { day: day(-1), total: 130, active: 100, archived: 30, superseded_active: 8, conflicted: 2, no_kind: 10, statuslog_like: 5, dup_groups: 3, dup_rows: 7, dead30: 40, dead90: 25 },
    coverage: { journal_since: "2020-01-01T00:00:00Z", unknown_share: 0.02, caller_kind: [{ caller_kind: "agent_tool", reads: 70 }, { caller_kind: "runner", reads: 14 }] },
    reasons: {},
  };
}

/** No journal at all, as the server answers it: every read metric null with its reason — «нет данных», never a zero. */
export function emptyJournalPayload(p: URLSearchParams): WireMemoryAnalytics {
  const base = mainPayload(p);
  const names = Object.keys(kpis) as (keyof WireKpis)[];
  const nulls = Object.fromEntries(names.map((k) => [k, null])) as unknown as WireKpis;
  const unread = ["reads", "hits", "useful", "reads_by_op", "served_rows_by_source", "useful_rate"];
  return {
    ...base,
    kpis: nulls,
    previous_kpis: nulls,
    reasons: Object.fromEntries(names.flatMap((k) => [[k, "no_journal_before"], [`previous_kpis.${k}`, "no_journal_before"]])),
    series: base.series.map((d) => ({ ...d, reads: null, hits: null, useful: null, reads_by_op: null, served_rows_by_source: null, useful_rate: null, reasons: Object.fromEntries(unread.map((n) => [n, "no_journal_before"])) })),
    coverage: { journal_since: null, unknown_share: null, caller_kind: [] },
  };
}

export const record: WireRecordRow = {
  memory_id: "m1", key: "deploy-url", redacted: false, scope: "workspace", kind: "fact", project_id: null, agent_id: "a1",
  created_at: "2026-10-01T00:00:00Z", served: 12, useful: 5, last_served_at: "2026-11-16T09:00:00Z",
};
export const emptyQuery: WireEmptyQuery = { query_hash: "h1", query_text: "how to restart runner", redacted: false, agent_id: "a1", agent_name: "Liam", count: 3, last_at: "2026-11-17T10:00:00Z" };
export const page1 = <T,>(items: T[]): WireList<T> => ({ items, total: items.length, page: 1, page_size: 20 });
export const unknownSource: WireUnknownSource = { route: "/api/v1/memories/recall", user_agent: "curl/8", reads: 9, sample_keys: ["deploy-url"] };
