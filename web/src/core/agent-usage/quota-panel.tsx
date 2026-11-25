// «Subscription limits»: a line per account (shares do not add up across accounts, so lines, not a stack), the 90 %
// threshold, «N of M at the limit» (M = accounts that have the chosen window), and the accounts without data for this
// window as rows with the reason. «Who spent it» on a row opens that account's tokens by agent over the same period.
import { useMemo } from "react";
import { cn } from "@/lib/cn";
import { HATCH, QuotaLines, seriesColor } from "./charts";
import { dayMonth, dayMonthTime, integer, percent, type ST } from "./format";
import { windowLabel } from "./labels";
import { Name, RowCard, RowMenu, ShareBar, Stat, type RowAction } from "./parts";
import { whoSpent, type StatsState } from "./state";
import type { QuotaNoDataReason, QuotaResponse, QuotaRow, QuotaWindow, UsageStep } from "./types";

/** The one limit threshold: the dashed line, «N of M pools at the limit», the red share and the at-limit hours
 *  column all read it (the server counts those hours from the same 90%). */
export const THRESHOLD = 0.9;
export const atLimit = (share: number | null | undefined): boolean => (share ?? 0) >= THRESHOLD;

/** A measured share: available, or exhausted with its share (an account at its limit, not «no data», Doc §3.2 v4). */
const hasData = (r: QuotaRow) => r.now !== null && r.now !== undefined && (r.status === "available" || r.status === "exhausted");
/** M in «N of M»: rows that have the chosen window; an account without it is listed but not counted. A legacy plain-string
 *  reason carries no code, so such a row counts only when it names its window or has a share. */
const hasWindow = (r: QuotaRow) => (r.reason && typeof r.reason === "object" ? r.reason.code !== "no_window" : r.window_raw != null || hasData(r));
const sharePct = (v: number | null | undefined) => (v === null || v === undefined ? "—" : percent(v));

/** The line is the highest share seen in each bucket (`used_max`, Doc §3.2), so the caption names the chart's step. */
function legendOf(step: UsageStep, st: ST): string {
  const pct = percent(THRESHOLD);
  if (step === "hour") return st("quota.legendHour", "Line: the highest share of the limit used in the hour. A drop to 0 is a window reset. Dashed: the {{pct}} threshold", { pct });
  if (step === "week") return st("quota.legendWeek", "Line: the highest share of the limit used in the week. A drop to 0 is a window reset. Dashed: the {{pct}} threshold", { pct });
  return st("quota.legendDay", "Line: the highest share of the limit used in the day. A drop to 0 is a window reset. Dashed: the {{pct}} threshold", { pct });
}

/** Milliseconds of an RFC 3339 time; Go sends nanoseconds, which Date.parse does not promise to read. */
const timeOf = (iso: string) => Date.parse(iso.replace(/(\.\d{3})\d+/, "$1"));

/**
 * The limits answer covers a rolling period (now − 7×24 h), so its first bucket is the day before the period the page
 * names («14 – 20 Nov» has a 13th). Buckets that start before `from` are dropped, with their values; a period that
 * starts at a bucket edge (a picked range) keeps them all.
 */
export function trimToPeriod(data: QuotaResponse): QuotaResponse {
  const from = timeOf(data.query.from);
  if (!Number.isFinite(from)) return data;
  const first = data.buckets.findIndex((b) => timeOf(b) >= from);
  if (first <= 0) return data;
  return { ...data, buckets: data.buckets.slice(first), series: data.series.map((s) => ({ ...s, values: s.values.slice(first) })) };
}

export function QuotaPanel({ data: raw, state, onChange, mode, copySlice, st }: { data: QuotaResponse; state: StatsState; onChange: (s: StatsState) => void; mode: "page" | "widget"; copySlice?: (s: StatsState) => void; st: ST }) {
  const data = useMemo(() => trimToPeriod(raw), [raw]);
  const tz = data.query.tz || "Europe/Moscow";
  const live = data.rows.filter(hasData).sort((a, b) => (b.now ?? 0) - (a.now ?? 0));
  // A pool without a current share but with a history in the period (prod 20.11: the line is drawn, `now`
  // is null): its row keeps the line's colour, peak and at-limit hours, «no data» and the reason under «Now».
  const drawsLine = new Set(data.series.filter((s) => s.values.some((v) => v != null)).map((s) => s.key));
  const hasHistory = (r: QuotaRow) => drawsLine.has(r.account) || (r.peak !== null && r.peak !== undefined);
  const past = data.rows.filter((r) => !hasData(r) && hasHistory(r));
  const dead = data.rows.filter((r) => !hasData(r) && !hasHistory(r));
  const near = live.filter((r) => atLimit(r.now));
  // Colours go to the series that draw something first, so a palette slot is not spent on an empty line.
  const drawn = [...data.series].sort((a, b) => Number(b.values.some((v) => v != null)) - Number(a.values.some((v) => v != null)));
  const color = new Map(drawn.map((s, i) => [s.key, seriesColor(i)]));
  const colorOf = (key: string, i: number) => color.get(key) ?? seriesColor(i);
  const series = data.series.map((s, i) => ({ key: s.key, label: s.label, color: colorOf(s.key, i), values: s.values }));
  const withWindow = data.rows.filter(hasWindow).length;
  const win = windowLabel(state.w, st).toLowerCase();
  const observed = data.rows.map((r) => r.observed_at).filter((x): x is string => !!x).sort().pop();

  const hoursOf = (r: QuotaRow) => (r.hours_at_limit == null ? "—" : st("unit.hoursShort", "{{n}} h", { n: integer(r.hours_at_limit) }));
  const resetOf = (r: QuotaRow) => (r.reset_at ? dayMonthTime(r.reset_at, tz) : "—");
  const actions = (r: QuotaRow): RowAction[] => {
    const out: RowAction[] = [{ id: "who", label: st("quota.who", "Who spent it"), run: () => onChange(whoSpent(state, r.account)) }];
    if (copySlice) out.push({ id: "link", label: st("row.link", "Link to this slice"), run: () => copySlice(whoSpent(state, r.account)) });
    return out;
  };

  if (!data.rows.length) {
    return (
      <div className="rounded-lg border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground" data-testid="stats-empty">
        {st("quota.empty", "No subscription pools report limits yet. They appear once the runner passes the account.")}
      </div>
    );
  }

  return (
    <div className="min-w-0 space-y-4 @container" data-testid="quota-panel">
      <section className={cn("min-w-0 space-y-3", mode === "page" && "rounded-xl border border-border bg-card p-4 @3xl:p-5")}>
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-6 gap-y-2" data-testid="quota-summary">
          <p className="min-w-0">
            <span className={cn("font-semibold tabular-nums", mode === "page" ? "text-3xl" : "text-xl")} data-testid="quota-near">
              {st("quota.nOfM", "{{n}} of {{m}}", { n: near.length, m: withWindow })}
            </span>{" "}
            <span className="text-sm text-muted-foreground">{st("quota.atLimit", "pools at the {{window}} limit", { window: win })}</span>
          </p>
          {near.slice(0, 3).map((r) => (
            <p key={r.account} className="min-w-0 truncate text-sm text-muted-foreground">
              <span className="font-semibold text-foreground">{r.account}</span> {percent(r.now ?? 0)}
              {r.reset_at ? `, ${st("quota.resetShort", "reset {{date}}", { date: dayMonth(r.reset_at, tz) })}` : ""}
            </p>
          ))}
          {dead.length > 0 && (
            <p className="inline-flex max-w-full items-center gap-2 rounded-md bg-muted px-2 py-1 text-xs text-muted-foreground" data-testid="quota-nodata-count">
              <span aria-hidden="true" className="h-2.5 w-4 shrink-0 rounded-sm" style={{ background: HATCH }} />
              {st("quota.noDataCount", "{{n}} pools have no {{window}} data", { n: dead.length, window: win })}
            </p>
          )}
        </div>
        <QuotaLines
          buckets={data.buckets}
          series={series}
          step={data.query.step}
          tz={tz}
          threshold={THRESHOLD}
          thresholdLabel={st("quota.threshold", "threshold {{pct}}", { pct: percent(THRESHOLD) })}
          nowLabel={st("quota.now", "now")}
          label={st("quota.chart", "Share of the {{window}} limit used, per pool", { window: win })}
        />
        {mode === "page" && (
          <p className="text-[11px] text-muted-foreground" data-testid="quota-legend">
            {legendOf(data.query.step, st)}
          </p>
        )}
      </section>

      <section className={cn("min-w-0", mode === "page" && "overflow-hidden rounded-xl border border-border bg-card")} data-testid="quota-table">
        <div className={cn("flex min-w-0 flex-wrap items-baseline justify-between gap-2", mode === "page" ? "px-4 pb-2 pt-4" : "pb-2")}>
          <h2 className="text-sm font-semibold">{st("quota.byPool", "By pool, «{{window}}» window", { window: win })}</h2>
          {observed && <p className="text-[11px] text-muted-foreground">{st("quota.updated", "Limit data updated {{time}}", { time: dayMonthTime(observed, tz) })}</p>}
        </div>
        <div className="hidden min-w-0 @3xl:block">
          <table className="w-full table-fixed text-sm">
            <thead className="border-y border-border text-[11px] text-muted-foreground">
              <tr>
                <th scope="col" className="w-[22%] px-3 py-2 text-left font-normal">{st("dim.account", "Pool / account")}</th>
                <th scope="col" className="w-[9%] px-3 py-2 text-right font-normal">{st("quota.colNow", "Now")}</th>
                <th scope="col" className="px-3 py-2 font-normal"><span className="sr-only">{st("quota.colBar", "Used")}</span></th>
                <th scope="col" className="w-[13%] px-3 py-2 text-right font-normal">{st("quota.colReset", "Reset")}</th>
                <th scope="col" className="w-[11%] px-3 py-2 text-right font-normal">{st("quota.colPeak", "Peak in period")}</th>
                <th scope="col" className="w-[12%] px-3 py-2 text-right font-normal">{st("quota.colAtLimit", "At limit (≥ {{pct}})", { pct: percent(THRESHOLD) })}</th>
                <th scope="col" className="w-36 px-3 py-2"><span className="sr-only">{st("col.actions", "Actions")}</span></th>
              </tr>
            </thead>
            <tbody>
              {live.map((r, i) => (
                <tr key={r.account} data-row={r.account} className="border-b border-border hover:bg-muted/40">
                  <td className="px-3 py-2.5"><Name text={r.account} color={colorOf(r.account, i)} className="font-medium" /></td>
                  <td className={cn("px-3 py-2.5 text-right font-semibold tabular-nums", atLimit(r.now) && "text-destructive")}>{sharePct(r.now)}</td>
                  <td className="px-3 py-2.5"><ShareBar share={r.now ?? 0} color={colorOf(r.account, i)} className="max-w-[8rem]" /></td>
                  <td className="px-3 py-2.5 text-right tabular-nums">{resetOf(r)}</td>
                  <td className="px-3 py-2.5 text-right tabular-nums">{sharePct(r.peak)}</td>
                  <td className="px-3 py-2.5 text-right tabular-nums">{hoursOf(r)}</td>
                  <td className="px-3 py-2.5">
                    <span className="flex items-center justify-end gap-1">
                      <button type="button" className="whitespace-nowrap rounded-md px-1.5 py-0.5 text-xs font-medium text-primary hover:bg-primary/10" onClick={() => onChange(whoSpent(state, r.account))}>
                        {st("quota.who", "Who spent it")}
                      </button>
                      <RowMenu actions={actions(r).slice(1)} label={st("row.menu", "Row actions: {{name}}", { name: r.account })} />
                    </span>
                  </td>
                </tr>
              ))}
              {past.map((r, i) => (
                <tr key={r.account} data-row={r.account} data-history="true" className="border-b border-border hover:bg-muted/40">
                  <td className="px-3 py-2.5"><Name text={r.account} color={colorOf(r.account, live.length + i)} className="font-medium" /></td>
                  <td className="px-3 py-2.5 text-right text-muted-foreground">{st("quota.noData", "no data")}</td>
                  <td className="px-3 py-2.5 text-xs text-muted-foreground"><span className="line-clamp-2 break-words" title={reasonOf(r, st, win)}>{reasonOf(r, st, win)}</span></td>
                  <td className="px-3 py-2.5 text-right tabular-nums">{resetOf(r)}</td>
                  <td className="px-3 py-2.5 text-right tabular-nums">{sharePct(r.peak)}</td>
                  <td className="px-3 py-2.5 text-right tabular-nums">{hoursOf(r)}</td>
                  <td className="px-3 py-2.5">
                    <span className="flex items-center justify-end gap-1">
                      <button type="button" className="whitespace-nowrap rounded-md px-1.5 py-0.5 text-xs font-medium text-primary hover:bg-primary/10" onClick={() => onChange(whoSpent(state, r.account))}>
                        {st("quota.who", "Who spent it")}
                      </button>
                      <RowMenu actions={actions(r).slice(1)} label={st("row.menu", "Row actions: {{name}}", { name: r.account })} />
                    </span>
                  </td>
                </tr>
              ))}
              {dead.map((r) => (
                <tr key={r.account} data-row={r.account} data-nodata="true" className="border-b border-border bg-muted/30 last:border-b-0">
                  <td className="px-3 py-2.5"><Name text={r.account} hatch /></td>
                  <td className="px-3 py-2.5 text-right text-muted-foreground">{st("quota.noData", "no data")}</td>
                  <td className="px-3 py-2.5 text-xs text-muted-foreground"><span className="line-clamp-2 break-words" title={reasonOf(r, st, win)}>{reasonOf(r, st, win)}</span></td>
                  <td className="px-3 py-2.5 text-right text-muted-foreground">—</td>
                  <td className="px-3 py-2.5 text-right text-muted-foreground">—</td>
                  <td className="px-3 py-2.5 text-right text-muted-foreground">—</td>
                  <td className="px-3 py-2.5 text-right">
                    <button type="button" className="whitespace-nowrap rounded-md px-1.5 py-0.5 text-xs font-medium text-primary hover:bg-primary/10" onClick={() => onChange(whoSpent(state, r.account))}>
                      {st("quota.who", "Who spent it")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {mode === "page" && <p className="border-t border-border px-4 py-2 text-[11px] text-muted-foreground">{st("quota.footer", "«Who spent it» opens this account's tokens by agent over the same period")}</p>}
        </div>
        <ul className={cn("min-w-0 @3xl:hidden", mode === "page" ? "border-t border-border" : "rounded-lg border border-border")}>
          {live.map((r, i) => (
            <RowCard
              key={r.account}
              testId="quota-card"
              actions={actions(r)}
              label={st("row.menu", "Row actions: {{name}}", { name: r.account })}
              head={
                <>
                  <Name text={r.account} color={colorOf(r.account, i)} className="flex-1 font-medium" />
                  <span className={cn("shrink-0 font-semibold tabular-nums", atLimit(r.now) && "text-destructive")}>{sharePct(r.now)}</span>
                </>
              }
            >
              <ShareBar share={r.now ?? 0} color={colorOf(r.account, i)} className="mt-2" />
              <div className="mt-2 grid grid-cols-3 gap-2">
                <Stat label={st("quota.colReset", "Reset")} value={resetOf(r)} />
                <Stat label={st("quota.peak", "Peak")} value={sharePct(r.peak)} />
                <Stat label={st("quota.atLimitShort", "At limit")} value={hoursOf(r)} />
              </div>
            </RowCard>
          ))}
          {past.map((r, i) => (
            <RowCard
              key={r.account}
              testId="quota-card"
              actions={actions(r)}
              label={st("row.menu", "Row actions: {{name}}", { name: r.account })}
              head={
                <>
                  <Name text={r.account} color={colorOf(r.account, live.length + i)} className="flex-1 font-medium" />
                  <span className="shrink-0 text-muted-foreground">{st("quota.noData", "no data")}</span>
                </>
              }
            >
              <p className="mt-1 pl-[18px] text-xs text-muted-foreground">{reasonOf(r, st, win)}</p>
              <div className="mt-2 grid grid-cols-3 gap-2" data-history="true">
                <Stat label={st("quota.colReset", "Reset")} value={resetOf(r)} />
                <Stat label={st("quota.peak", "Peak")} value={sharePct(r.peak)} />
                <Stat label={st("quota.atLimitShort", "At limit")} value={hoursOf(r)} />
              </div>
            </RowCard>
          ))}
          {dead.map((r) => (
            <RowCard
              key={r.account}
              testId="quota-card"
              actions={actions(r)}
              label={st("row.menu", "Row actions: {{name}}", { name: r.account })}
              head={
                <>
                  <Name text={r.account} hatch className="flex-1 font-medium" />
                  <span className="shrink-0 text-muted-foreground">{st("quota.noData", "no data")}</span>
                </>
              }
            >
              <p className="mt-1 pl-[18px] text-xs text-muted-foreground">{reasonOf(r, st, win)}</p>
            </RowCard>
          ))}
        </ul>
      </section>
    </div>
  );
}

/** «5 h», «month», «budget»: a window inside a reason; a name the page does not know stays as the server sent it. */
function windowShort(w: string, raw: string | undefined, st: ST): string {
  if (w === "5h") return st("windowShort.5h", "5 h");
  if (w === "week" || w === "month" || w === "budget") return windowLabel(w as QuotaWindow, st).toLowerCase();
  return raw || w;
}

/**
 * Why a pool has no data, in the page's language: built from `reason.code` (Doc §3.2), «no «week» window: 5 h — 4 %,
 * month — 12 %» from the other windows that have a share. A code the page does not know shows the server's text.
 */
export function reasonOf(r: QuotaRow, st: ST, win: string): string {
  if (typeof r.reason === "string") return r.reason;
  const reason: QuotaNoDataReason | null | undefined = r.reason;
  const code = reason?.code ?? (r.status === "available" ? "no_window" : "no_data");
  if (code === "no_window") {
    const others = (reason?.other_windows ?? r.other_windows ?? []).filter((o) => o.now != null);
    const base = st("quota.noWindow", "no «{{window}}» window", { window: win });
    if (!others.length) return base;
    return `${base}: ${others.map((o) => `${windowShort(o.window, o.window_raw, st)} — ${percent(o.now!)}`).join(", ")}`;
  }
  if (code === "no_data") return st("quota.noSource", "the limit source returned no data");
  return reason?.text || st("quota.noSource", "the limit source returned no data");
}
