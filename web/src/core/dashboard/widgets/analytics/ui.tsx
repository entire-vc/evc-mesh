// Building blocks of the analytics widgets: the card with its header and "⋯" menu, the four states,
// and the SVG/div charts of the Analytics page. Colours come only from the --chart-*, --priority-* and
// --status-* tokens, read by the browser at paint time, so a theme or mode switch recolours in place.
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { Download, MoreHorizontal } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { apiErrorMessage } from "@/lib/api-error";
import * as fmt from "@/i18n/format";
import { cn } from "@/lib/cn";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Async } from "../../types";
import { downloadAnalyticsCsv } from "./filters";
import type { AnalyticsQuery } from "./use-analytics";

// -- copy ---------------------------------------------------------------------------------------------

export type AT = (key: string, en: string, opts?: Record<string, unknown>) => string;

/** Copy of the analytics widgets: `notifications:dashboard.analytics.<key>`, English when the key is missing. */
export function useAT(): AT {
  const { t } = useTranslation("notifications");
  return (key, en, opts) => t(`dashboard.analytics.${key}`, { defaultValue: en, ...opts });
}

// -- colours and numbers ------------------------------------------------------------------------------

/** Categorical series, in order. */
export const SERIES = [
  "hsl(var(--chart-1))",
  "hsl(var(--chart-2))",
  "hsl(var(--chart-3))",
  "hsl(var(--chart-4))",
  "hsl(var(--chart-5))",
  "hsl(var(--chart-priority-high))",
  "hsl(var(--chart-priority-low))",
] as const;
export const seriesColor = (i: number) => SERIES[i % SERIES.length]!;
/** The neutral token: "Other", "none", unknown statuses. */
export const NEUTRAL = "var(--status-backlog)";
export const statusColor = (category: string) => `var(--status-${category.replace(/_/g, "-")}, ${NEUTRAL})`;
export const priorityColor = (p: string) => (p === "none" ? NEUTRAL : `var(--priority-${p}, ${NEUTRAL})`);

export function formatCost(cost: number): string {
  if (cost > 0 && cost < 0.01) return "<$0.01";
  return `$${fmt.formatNumber(cost, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}
export function formatTokens(n: number): string {
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(1)}B`;
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

// -- rows ---------------------------------------------------------------------------------------------

/** One row of a breakdown: what it counts, how it reads, where it leads. */
export interface Row {
  key: string;
  label: string;
  value: number;
  /** Shown instead of the bare number (a cost, tokens). */
  display?: string;
  color: string;
  /** A route inside the app. */
  href?: string;
  /** An action of the host (open an agent). */
  onOpen?: () => void;
  /** Drawn after the label (an agent's short tag). */
  suffix?: ReactNode;
}

export const OTHER_KEY = "__other";

/**
 * The `n` largest rows and one "Other" for the rest. `total` is the whole the rows are part of, when the
 * rows may be a cut of it (the server lists at most 50 assignees): Other is then what the shown rows miss.
 */
export function topN(rows: Row[], n: number, otherLabel: string, total?: number, format?: (v: number) => string): Row[] {
  const sorted = [...rows].filter((r) => r.value > 0).sort((a, b) => b.value - a.value);
  if (sorted.length <= n && (total === undefined || total <= sorted.reduce((s, r) => s + r.value, 0))) return sorted;
  const top = sorted.slice(0, n);
  const topSum = top.reduce((s, r) => s + r.value, 0);
  const restSum = sorted.slice(n).reduce((s, r) => s + r.value, 0);
  const other = Math.max(restSum, total !== undefined ? total - topSum : 0);
  if (other <= 0) return top;
  return [...top, { key: OTHER_KEY, label: otherLabel, value: other, color: NEUTRAL, display: format?.(other) }];
}

// -- card and states ----------------------------------------------------------------------------------

function periodLabel(at: AT, q: AnalyticsQuery): string {
  const p = q.period;
  if (!p || "days" in p) return at("period.days", "Last {{days}} days", { days: p?.days ?? 30 });
  const day = (d: string) => fmt.formatDate(`${d}T00:00:00`, { day: "numeric", month: "short" });
  return `${day(p.from)} – ${day(p.to)}`;
}

/** Card of one widget: title, period, the "⋯" menu with the CSV export of the instance's filter. */
/** `query` absent: a live workspace count that no period or project narrows, so no period label and no CSV. */
export function WidgetFrame({ title, query, children }: { title: string; query?: AnalyticsQuery; children: ReactNode }) {
  const at = useAT();
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const [busy, setBusy] = useState(false);
  const download = async () => {
    if (!wsId || busy || !query) return;
    setBusy(true);
    try {
      await downloadAnalyticsCsv(wsId, query);
    } catch (e) {
      toast.error(apiErrorMessage(e, at("csvFailed", "CSV export failed")));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card className="h-full min-w-0 @container">
      <CardContent className="flex h-full min-w-0 flex-col gap-3 pt-4">
        <div className="flex min-w-0 items-center gap-2">
          {/* On paper there is no hover title to read the rest from: the heading wraps instead of ending in "…". */}
          <h3 className="min-w-0 flex-1 truncate text-sm font-semibold print:overflow-visible print:whitespace-normal" title={title}>
            {title}
          </h3>
          {query && <span className="shrink-0 text-[11px] text-muted-foreground">{periodLabel(at, query)}</span>}
          {query && (
          <DropdownMenu>
            <DropdownMenuTrigger
              aria-label={at("menu", "Widget menu")}
              className="shrink-0 rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground print:hidden"
            >
              <MoreHorizontal className="h-4 w-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent>
              <DropdownMenuItem disabled={!wsId || busy} onClick={() => void download()} className="gap-2 text-xs">
                <Download className="h-3.5 w-3.5" />
                {busy ? at("csvBusy", "Downloading…") : at("csv", "Download CSV")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          )}
        </div>
        <div className="min-h-0 min-w-0 flex-1">{children}</div>
      </CardContent>
    </Card>
  );
}

/** "Data incomplete since <date>" (or "for this period" when the server names no date). */
export function IncompleteNote({ since }: { since?: string | null }) {
  const at = useAT();
  return (
    <p role="note" className="mt-2 text-[11px] text-muted-foreground">
      {since
        ? at("incompleteSince", "Data incomplete since {{date}}", { date: fmt.formatDate(since) })
        : at("incomplete", "Data incomplete for this period")}
    </p>
  );
}

/**
 * Loading is a skeleton, an error says so with a Retry (the host's `retry` when it gives one, else the
 * hook's own), no data is "No data for this period", and partial data carries the note under whatever is drawn.
 */
export function AnalyticsBody<D>({
  result,
  retry,
  rows = 3,
  isEmpty,
  incomplete,
  children,
}: {
  result: Async<D>;
  retry?: () => void;
  rows?: number;
  isEmpty: (v: D) => boolean;
  /** `false` = complete; `null`/a date = incomplete (since that date when known). */
  incomplete?: (v: D) => false | string | null;
  children: (v: D) => ReactNode;
}) {
  const at = useAT();
  if (result.state === "loading") {
    return (
      <div className="space-y-2" aria-busy="true" aria-label={at("loading", "Loading")}>
        {Array.from({ length: rows }).map((_, i) => (
          <Skeleton key={i} className="h-6 w-full rounded-md" />
        ))}
      </div>
    );
  }
  if (result.state === "error") {
    return (
      <div role="alert" className="flex flex-col items-center justify-center gap-2 py-6 text-center text-xs text-muted-foreground">
        <p className="max-w-full break-words">{result.error.message || at("error", "Could not load the data")}</p>
        <button type="button" className="underline hover:text-foreground" onClick={retry ?? result.retry}>
          {at("retry", "Retry")}
        </button>
      </div>
    );
  }
  const v = result.value;
  const partial = incomplete ? incomplete(v) : false;
  return (
    <>
      {isEmpty(v) ? <p className="py-6 text-center text-xs text-muted-foreground">{at("empty", "No data for this period")}</p> : children(v)}
      {partial !== false && <IncompleteNote since={partial} />}
    </>
  );
}

// -- charts -------------------------------------------------------------------------------------------

function RowLabel({ row, className }: { row: Row; className?: string }) {
  const body = (
    <>
      <span className="truncate">{row.label}</span>
      {row.suffix}
    </>
  );
  const cls = cn("flex min-w-0 items-center truncate", className);
  if (row.href) {
    return (
      <Link to={row.href} title={row.label} className={cn(cls, "hover:text-foreground hover:underline")}>
        {body}
      </Link>
    );
  }
  if (row.onOpen) {
    return (
      <button type="button" title={row.label} onClick={row.onOpen} className={cn(cls, "text-left hover:text-foreground hover:underline")}>
        {body}
      </button>
    );
  }
  return (
    <span title={row.label} className={cls}>
      {body}
    </span>
  );
}

/** Horizontal bars (the Analytics page's chart): label, bar to scale, value. Fits any cell width. */
export function BarList({ rows, rank = false }: { rows: Row[]; rank?: boolean }) {
  const max = Math.max(...rows.map((r) => r.value), 1);
  return (
    <ul className="space-y-1.5">
      {rows.map((row, i) => (
        <li key={row.key} data-row={row.key} className="flex min-w-0 items-center gap-2 text-xs">
          {rank && <span className="w-5 shrink-0 text-right tabular-nums text-muted-foreground">{i + 1}.</span>}
          <RowLabel row={row} className="w-2/5 shrink-0 text-muted-foreground" />
          <div className="relative h-3 min-w-0 flex-1">
            <div
              className="absolute inset-y-0 left-0 rounded-sm"
              style={{ width: `${(row.value / max) * 100}%`, backgroundColor: row.color, minWidth: row.value > 0 ? 3 : 0 }}
            />
          </div>
          <span className="shrink-0 text-right font-medium tabular-nums">{row.display ?? fmt.formatNumber(row.value)}</span>
        </li>
      ))}
    </ul>
  );
}

/** Ring of shares with its legend; the ring keeps its size, the legend wraps under it in a narrow cell. */
export function Donut({ rows, label }: { rows: Row[]; label: string }) {
  const total = rows.reduce((s, r) => s + r.value, 0) || 1;
  const R = 15.915; // circumference 100: a share is its own dash length
  let offset = 25; // start at 12 o'clock
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-4">
      <svg viewBox="0 0 42 42" className="h-28 w-28 shrink-0" role="img" aria-label={label}>
        <circle cx="21" cy="21" r={R} fill="none" stroke={NEUTRAL} strokeOpacity={0.15} strokeWidth="6" />
        {rows.map((r) => {
          const share = (r.value / total) * 100;
          const el = (
            <circle
              key={r.key}
              cx="21"
              cy="21"
              r={R}
              fill="none"
              stroke={r.color}
              strokeWidth="6"
              strokeDasharray={`${share} ${100 - share}`}
              strokeDashoffset={offset}
            >
              <title>{`${r.label}: ${r.display ?? fmt.formatNumber(r.value)}`}</title>
            </circle>
          );
          offset -= share;
          return el;
        })}
      </svg>
      <ul className="min-w-0 flex-1 basis-40 space-y-1">
        {rows.map((r) => (
          <li key={r.key} data-row={r.key} className="flex min-w-0 items-center gap-2 text-xs">
            <span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: r.color }} />
            <RowLabel row={r} className="flex-1 text-muted-foreground" />
            <span className="shrink-0 font-medium tabular-nums">{r.display ?? fmt.formatNumber(r.value)}</span>
            <span className="w-9 shrink-0 text-right tabular-nums text-muted-foreground">{Math.round((r.value / total) * 100)}%</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

const W = 600;
const H = 150;
const PAD = { top: 8, right: 12, bottom: 22, left: 12 };

function ticksOf(days: string[]) {
  const every = Math.max(1, Math.ceil(days.length / 6));
  return days.map((d, i) => ({ i, label: d.slice(5) })).filter((t) => t.i % every === 0);
}

/** Lines per day (created vs completed), as on the Analytics page; the SVG scales with the cell. */
export function LineChart({ days, series, label }: { days: string[]; series: { key: string; label: string; color: string; values: number[] }[]; label: string }) {
  const cw = W - PAD.left - PAD.right;
  const ch = H - PAD.top - PAD.bottom;
  const max = Math.max(1, ...series.flatMap((s) => s.values));
  const step = days.length > 1 ? cw / (days.length - 1) : 0;
  const x = (i: number) => PAD.left + (days.length > 1 ? i * step : cw / 2);
  const y = (v: number) => PAD.top + ch - (v / max) * ch;
  return (
    <div className="min-w-0">
      <svg viewBox={`0 0 ${W} ${H}`} className="block h-auto w-full" role="img" aria-label={label}>
        {[0, 0.5, 1].map((f) => (
          <line key={f} x1={PAD.left} x2={PAD.left + cw} y1={PAD.top + ch - f * ch} y2={PAD.top + ch - f * ch} stroke="currentColor" strokeOpacity={0.1} />
        ))}
        {series.map((s) => (
          <g key={s.key}>
            <path d={`M ${PAD.left},${PAD.top + ch} ${s.values.map((v, i) => `L ${x(i)},${y(v)}`).join(" ")} L ${x(s.values.length - 1)},${PAD.top + ch} Z`} fill={s.color} fillOpacity={0.12} />
            <path d={`M ${s.values.map((v, i) => `${x(i)},${y(v)}`).join(" L ")}`} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
          </g>
        ))}
        {ticksOf(days).map((t) => (
          <text key={t.i} x={x(t.i)} y={H - 6} textAnchor="middle" fontSize={11} fill="currentColor" fillOpacity={0.5}>
            {t.label}
          </text>
        ))}
      </svg>
      <div className="mt-1 flex flex-wrap justify-center gap-x-4 gap-y-1">
        {series.map((s) => (
          <span key={s.key} className="flex items-center gap-1 text-xs text-muted-foreground">
            <span className="inline-block h-2 w-4 rounded-sm" style={{ backgroundColor: s.color }} />
            {s.label} <span className="font-medium tabular-nums text-foreground">{fmt.formatNumber(s.values.reduce((a, b) => a + b, 0))}</span>
          </span>
        ))}
      </div>
    </div>
  );
}

/** One column per day; each carries its value in a tooltip. */
export function ColumnChart({ days, values, color, format, label }: { days: string[]; values: number[]; color: string; format: (v: number) => string; label: string }) {
  const cw = W - PAD.left - PAD.right;
  const ch = H - PAD.top - PAD.bottom;
  const max = Math.max(...values, 0) || 1;
  const slot = cw / Math.max(days.length, 1);
  const bar = Math.max(1, slot * 0.7);
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="block h-auto w-full min-w-0" role="img" aria-label={label}>
      <line x1={PAD.left} x2={PAD.left + cw} y1={PAD.top + ch} y2={PAD.top + ch} stroke="currentColor" strokeOpacity={0.2} />
      {days.map((d, i) => {
        const h = (values[i]! / max) * ch;
        return (
          <rect key={d} data-day={d} x={PAD.left + i * slot + (slot - bar) / 2} y={PAD.top + ch - h} width={bar} height={h} rx={1.5} fill={color}>
            <title>{`${d}: ${format(values[i]!)}`}</title>
          </rect>
        );
      })}
      {ticksOf(days).map((t) => (
        <text key={t.i} x={PAD.left + t.i * slot + slot / 2} y={H - 6} textAnchor="middle" fontSize={11} fill="currentColor" fillOpacity={0.5}>
          {t.label}
        </text>
      ))}
    </svg>
  );
}

/** A number with its caption (the cost block's tiles, the metric tile). */
/** `wrap`: a two-part value ("in / out") breaks onto a second line in a narrow tile instead of being cut. */
export function Tile({ label, value, caption, className, wrap }: { label: string; value: string; caption?: ReactNode; className?: string; wrap?: boolean }) {
  return (
    <div className={cn("min-w-0 rounded-lg border border-border p-3", className)}>
      <p className="truncate text-[11px] text-muted-foreground" title={label}>
        {label}
      </p>
      <p className={cn("text-lg font-semibold tabular-nums", wrap ? "leading-tight [overflow-wrap:anywhere]" : "truncate")} title={value}>
        {value}
      </p>
      {caption}
    </div>
  );
}
