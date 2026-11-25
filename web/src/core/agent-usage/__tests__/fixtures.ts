import type { QuotaResponse, UsageResponse, UsageRow } from "../types";

export const usageFixture = (): UsageResponse => ({
  query: { metric: "cost", group_by: "model", stack_by: null, from: "2026-11-12T00:00:00+03:00", to: "2026-11-19T00:00:00+03:00", step: "day", tz: "Europe/Moscow" },
  buckets: ["2026-11-12T00:00:00+03:00", "2026-11-13T00:00:00+03:00"],
  series: [{ key: "opus", label: "Opus", values: [10, 20] }, { key: "unknown-model", label: "Unknown model", values: [null, null] }],
  rows: [
    { key: "opus", label: "Opus", value: 30, turns: 3, turns_unknown: 0, tokens_in: 100, tokens_out: 20, cache_read: 0, cache_write: 0 },
    { key: "unknown-model", label: "Unknown model", value: null, turns: 2, turns_unknown: 2 },
  ],
  totals: { value: 30, turns: 5, turns_unknown: 2, tokens_in: 100, tokens_out: 20, cache_read: 0, cache_write: 0 },
  coverage: { by_bucket: [{ turns: 2, turns_known: 1 }, { turns: 3, turns_known: 2 }] },
  pricing: { models: [{ model: "opus", valid_from: "2026-11-01", input_per_mtok: 5, output_per_mtok: 25, cache_read_per_mtok: 0.5 }], no_price_models: ["unknown-model"] },
  capabilities: { group_by: ["model", "provider", "agent", "project", "account"], cost: true, quota: true, cards_done_group_by: ["agent", "project"] },
});

export const quotaFixture = (): QuotaResponse => ({
  query: { window: "week", from: "2026-11-12T00:00:00+03:00", to: "2026-11-19T00:00:00+03:00", step: "day", tz: "Europe/Moscow" },
  buckets: ["2026-11-12T00:00:00+03:00", "2026-11-13T00:00:00+03:00"],
  series: [{ key: "claude/bob-team", label: "claude/bob-team", values: [0.8, 0.97] }],
  rows: [
    { account: "claude/bob-team", now: 0.97, peak: 0.99, hours_at_limit: 2, status: "available", reset_at: "2026-11-20T00:00:00Z" },
    { account: "kimi/alex", now: null, status: "unknown", reason: "No weekly window; monthly 33%" },
  ],
});

const prodRow = (key: string, value: number | null, turns = 4): UsageRow => ({ key, label: key, value, turns, turns_unknown: value === null ? turns : 0, tokens_in: 1000, tokens_out: 100, cache_read: 0, cache_write: 0 });

/**
 * Usage as prod sends it (19.11): eight models; up to `top` of them in the server's own order (by its key,
 * not by cost), the rest folded into `__other__` / "Other" with no count.
 */
export function prodUsageFixture(top = 5): UsageResponse {
  const d = usageFixture();
  const all = [prodRow("claude-opus-5-5", 30), prodRow("zcode", null), prodRow("kimi", null), prodRow("claude-sonnet-5-5", 5), prodRow("gpt-6.1-sol", 0.73), prodRow("<synthetic>", null, 3), prodRow("gpt-6-astra", null, 3), prodRow("gpt-6-luna", null, 2)];
  d.rows = top >= all.length ? all : [...all.slice(0, top), { ...prodRow("__other__", null, 8), label: "Other" }];
  d.series = d.rows.map((r) => ({ key: r.key, label: r.label, values: [r.value, r.value] }));
  d.totals = { ...d.totals, value: 35.73, turns: 28, turns_unknown: 16 };
  d.coverage = { by_bucket: [{ turns: 14, turns_known: 6 }, { turns: 14, turns_known: 6 }] };
  return d;
}

/** The limits answer of prod 19.11 22:50Z: a rolling 7×24 h period, the first bucket the day before it starts. */
export function prodQuotaFixture(): QuotaResponse {
  const d = quotaFixture();
  const days = ["13", "14", "15", "16", "17", "18", "19", "20"];
  d.query = { window: "week", from: "2026-11-12T22:50:26.473284779Z", to: "2026-11-19T22:50:26.473284779Z", step: "day", tz: "Europe/Moscow" };
  d.buckets = days.map((x) => `2026-11-${x}T00:00:00+03:00`);
  d.series = [{ key: "claude/bob-team", label: "claude/bob-team", values: [0.5, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.09] }];
  d.rows = [
    { account: "claude/bob-team", window_raw: "7d", now: 0.09, status: "available", observed_at: "2026-11-19T22:46:33Z" },
    { account: "kimi/alex", window_raw: null, now: null, status: null as unknown as string, reason: { code: "no_window", text: "no week window: 5h — 9%, month — 13%", other_windows: [{ window: "5h", window_raw: "5h", now: 0.0885, status: "available" }, { window: "month", window_raw: "month", now: 0.1271, status: "available" }] } },
    { account: "anthropic-api/bob", now: null, status: null as unknown as string, reason: { code: "no_window", text: "no week window: budget — 18%", other_windows: [{ window: "budget", window_raw: "budget", now: 0.1816, status: "available" }] } },
    { account: "claude/alex", window_raw: "7d", now: null, status: "unknown", reason: { code: "no_data", text: "source provided no data (unknown)", other_windows: [{ window: "5h", window_raw: "5h", now: null, status: "unknown" }] } },
    { account: "glm/odd", now: null, status: "unknown", reason: { code: "stale", text: "observation is 3 days old" } },
    { account: "codex/none", now: null, status: null as unknown as string, reason: { code: "no_window", text: "no week window", other_windows: [{ window: "5h", now: null, status: "unknown" }] } },
  ];
  return d;
}
