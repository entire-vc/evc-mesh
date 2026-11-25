// Instance settings of the analytics widgets -> the query of useAnalytics, the previous period of the
// same length, and the CSV export of the same filter. Settings come from a stored layout, so anything
// malformed falls back to the default instead of failing the widget.
import { apiBlob } from "@/lib/api";
import { DEFAULT_PERIOD, analyticsExportRequest, analyticsParams, type AnalyticsPeriod, type AnalyticsQuery } from "./use-analytics";

const DAY_MS = 86_400_000;
const MINUTE_MS = 60_000;
const ISO_DAY = /^\d{4}-\d{2}-\d{2}$/;
const isDay = (v: unknown): v is string => typeof v === "string" && ISO_DAY.test(v) && !Number.isNaN(Date.parse(`${v}T00:00:00Z`));
const toDay = (ms: number) => new Date(ms).toISOString().slice(0, 10);
const dayMs = (d: string) => Date.parse(`${d}T00:00:00Z`);

/** The `period` setting, or the default (30 days) when it is missing or malformed. */
export function readPeriod(v: unknown): AnalyticsPeriod {
  if (v && typeof v === "object") {
    const o = v as Record<string, unknown>;
    if (o.days === 7 || o.days === 30 || o.days === 90) return { days: o.days };
    if (isDay(o.from) && isDay(o.to) && o.from <= o.to) return { from: o.from, to: o.to };
  }
  return DEFAULT_PERIOD;
}

/**
 * The filter every analytics widget sends: period and project only. Widgets with the same settings build
 * equal queries and so share one request; a widget's own settings (metric, dimension, view) never enter it.
 */
export function readQuery(settings: Record<string, unknown> | undefined): AnalyticsQuery {
  const s = settings ?? {};
  const q: AnalyticsQuery = { period: readPeriod(s.period) };
  if (typeof s.project === "string" && s.project !== "") q.projectId = s.project;
  return q;
}

/**
 * The period of the same length right before `period`, ending where it starts. A preset runs from midnight
 * `days` days ago to now, so it is `days` days plus the part of today already gone; the previous one ends
 * with the last second before the current first day (the server counts both bounds, as for a custom range)
 * and starts that much earlier: `days` days plus the same part of a day. That part is cut to the minute, so
 * the widgets drawn together still ask one thing; the lengths then differ by less than a minute.
 * A custom range is whole days and its previous one the same number of whole days.
 */
export function previousPeriod(period: AnalyticsPeriod, now = new Date()): { from: string; to: string } {
  if ("days" in period) {
    const today = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
    const start = today - period.days * DAY_MS;
    const elapsed = Math.floor((now.getTime() - today) / MINUTE_MS) * MINUTE_MS;
    const from = new Date(start - period.days * DAY_MS - elapsed).toISOString().replace(/\.\d{3}Z$/, "Z");
    return { from, to: toDay(start - DAY_MS) };
  }
  const from = dayMs(period.from);
  const length = Math.round((dayMs(period.to) - from) / DAY_MS) + 1;
  return { from: toDay(from - length * DAY_MS), to: toDay(from - DAY_MS) };
}

/** The first day the query covers (YYYY-MM-DD), as the server is asked. */
export function firstDay(q: AnalyticsQuery, now = new Date()): string {
  return analyticsParams(q, now).from ?? toDay(now.getTime());
}

/** Downloads the CSV export of the widget's filter (same parameters as its data request). */
export async function downloadAnalyticsCsv(wsId: string, q: AnalyticsQuery): Promise<void> {
  return downloadCsv(analyticsExportRequest(wsId, q), "analytics");
}

/** Downloads one CSV export request; the file is named by the server, else `<prefix>-<from>.csv`. */
export async function downloadCsv({ path, params }: { path: string; params: Record<string, string> }, prefix: string): Promise<void> {
  const { blob, filename } = await apiBlob(path, params);
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  // apiBlob names a response without Content-Disposition "download".
  a.download = filename && filename !== "download" ? filename : `${prefix}-${params.from ?? "export"}.csv`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
