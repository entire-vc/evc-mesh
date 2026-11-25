// «≈ $ at API price», tokens, spawns, run time and closed cards: the summary, the columns per bucket with their
// coverage, and the breakdown table (cards on a narrow screen) with its total. Brief: plan doc a9781091 §4.
import { useMemo, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp } from "lucide-react";
import { cn } from "@/lib/cn";
import * as fmt from "@/i18n/format";
import { HATCH, NEUTRAL, StackedColumns, seriesColor } from "./charts";
import { bucketTitle, clock, compact, integer, metricAxis, metricValue, money, percent, periodWord, tokenKindLabel, zoneName, type ST } from "./format";
import { byDimension, dimensionLabel } from "./labels";
import { Name, RowCard, RowMenu, ShareBar, Stat, type RowAction } from "./parts";
import { MAX_TOP, filterOf, onlyDay, withFilter, type StatsState } from "./state";
import type { UsageMetric, UsageResponse, UsageRow } from "./types";

export interface UsageLinks {
  /** In-app page of a row's agent or project, when known. */
  openHref?: (link: NonNullable<UsageRow["link"]>) => string | undefined;
  open?: (href: string) => void;
  /** Copies the link of a state («Link to this slice»). */
  copySlice?: (st: StatsState) => void;
}

/** The server folds every group past `top` into one row: `__other__` (U3), `__other` in early fixtures. */
const OTHER_KEYS = new Set(["__other__", "__other"]);
const isOtherKey = (key: string) => OTHER_KEYS.has(key);
export const isOther = (r: UsageRow) => isOtherKey(r.key) || typeof r.other_count === "number";
/** A row the API could not attribute («account not given»): shown, never a filter value. */
const isUnattributed = (r: UsageRow) => r.key === "" || (r.key.startsWith("__") && !isOther(r));
const unknownShare = (turns: number, unknown: number) => (turns > 0 ? unknown / turns : 0);
const cacheOf = (r: { cache_read?: number | null; cache_write?: number | null }) =>
  r.cache_read == null && r.cache_write == null ? null : (r.cache_read ?? 0) + (r.cache_write ?? 0);

/** Only `billing_mode=api` is a real spend; everything else is the API-price equivalent. */
export const isActualSpend = (st: StatsState) => st.f.billing_mode?.length === 1 && st.f.billing_mode[0] === "api";

type SortKey = "label" | "value" | "in" | "out" | "cache" | "turns" | "unknown";

function sortValue(r: UsageRow, k: SortKey): number | string {
  switch (k) {
    case "label":
      return r.label.toLowerCase();
    case "value":
      return r.value ?? -1;
    case "in":
      return r.tokens_in ?? -1;
    case "out":
      return r.tokens_out ?? -1;
    case "cache":
      return cacheOf(r) ?? -1;
    case "turns":
      return r.turns;
    default:
      return unknownShare(r.turns, r.turns_unknown);
  }
}

const DEFAULT_SORT: { key: SortKey; dir: 1 | -1 } = { key: "value", dir: -1 };

/** «Sorted by: cost» over the wide table, «by cost» over the cards (the mockup's 393 header). */
function sortName(sort: { key: SortKey; dir: 1 | -1 }, metric: UsageMetric, dim: UsageResponse["query"]["group_by"], st: ST, s: StatsState): { noun: string; by: string } {
  let noun: string;
  let by: string;
  switch (sort.key) {
    case "value":
      noun = metric === "cost" ? st("sort.cost", "cost") : metric === "tokens" ? tokenKindLabel(s.t, st).toLowerCase() : metric === "turns" ? st("sort.turns", "spawns") : metric === "duration" ? st("sort.duration", "run time") : st("sort.cards_done", "cards");
      by = metric === "cost" ? st("sortBy.cost", "by cost") : metric === "tokens" ? st("sortBy.tokens", "by tokens") : metric === "turns" ? st("sortBy.turns", "by spawns") : metric === "duration" ? st("sortBy.duration", "by run time") : st("sortBy.cards_done", "by cards");
      break;
    case "label":
      noun = dimensionLabel(dim, st).toLowerCase();
      by = st("sortBy.label", "by name");
      break;
    case "in":
      noun = st("col.in", "Input").toLowerCase();
      by = st("sortBy.in", "by input");
      break;
    case "out":
      noun = st("col.out", "Output").toLowerCase();
      by = st("sortBy.out", "by output");
      break;
    case "cache":
      noun = st("col.cache", "Cache").toLowerCase();
      by = st("sortBy.cache", "by cache");
      break;
    case "turns":
      noun = st("col.turnsCount", "Turns").toLowerCase();
      by = st("sortBy.turnsCount", "by turns");
      break;
    default:
      noun = unknownHeader(metric, st).toLowerCase();
      by = st("sortBy.unknown", "by share without a value");
  }
  if (sort.dir === 1 && sort.key !== "label") noun = st("sort.ascending", "{{what}}, ascending", { what: noun });
  return { noun, by };
}

function metricCaption(metric: UsageMetric, st: ST, s: StatsState): string {
  if (metric === "cost") return isActualSpend(s) ? st("caption.actual", "actual spend") : st("caption.cost", "at API price");
  if (metric === "tokens") return st("caption.tokens", "tokens: {{kind}}", { kind: tokenKindLabel(s.t, st).toLowerCase() });
  if (metric === "turns") return st("caption.turns", "spawns");
  if (metric === "duration") return st("caption.duration", "of agent run time");
  return st("caption.cards_done", "cards closed");
}

/** Column header of the value. */
function valueHeader(metric: UsageMetric, st: ST, s: StatsState): string {
  if (metric === "cost") return isActualSpend(s) ? st("col.actual", "$ actual") : st("col.cost", "≈ $ API");
  if (metric === "tokens") return tokenKindLabel(s.t, st);
  if (metric === "turns") return st("col.turns", "Spawns");
  if (metric === "duration") return st("col.duration", "Time");
  return st("col.cards_done", "Cards");
}

const unknownHeader = (metric: UsageMetric, st: ST) => (metric === "cost" ? st("col.noCost", "No $") : metric === "tokens" ? st("col.noTokens", "No tokens") : st("col.noData", "No data"));

function basisTitle(r: UsageRow, st: ST): string | undefined {
  if (r.cost_basis === "reported") return st("basis.reported", "API-price estimate reported by the harness");
  if (r.cost_basis === "mixed") return st("basis.mixed", "API-price estimate from prices and harness reports");
  if (r.cost_basis === "price") return st("basis.price", "≈ at API price");
  return undefined;
}

function UnknownCell({ share }: { share: number }) {
  return (
    <span className="inline-flex items-center justify-end gap-2">
      <span aria-hidden="true" className="hidden h-1.5 w-10 rounded-sm @5xl:block" style={{ background: share > 0 ? HATCH : "transparent" }} />
      <span className="tabular-nums">{percent(share)}</span>
    </span>
  );
}

/** How many groups the period has in all, from a `top=MAX_TOP` read: `more` = even that one folded some away. */
export interface OthersCount {
  all: number;
  more: boolean;
}

export function UsagePanel({
  data,
  state,
  onChange,
  mode,
  links,
  pinned,
  othersCount,
  updatedAt,
  st,
}: {
  data: UsageResponse;
  state: StatsState;
  onChange: (s: StatsState) => void;
  mode: "page" | "widget";
  links: UsageLinks;
  pinned?: "project" | "agent";
  othersCount?: OthersCount | null;
  /** When this answer was loaded (ms): «Updated 16:40 MSK» under the table. */
  updatedAt?: number;
  st: ST;
}) {
  const metric = data.query.metric;
  const tz = data.query.tz || "Europe/Moscow";
  // The mockup's table is ordered by the value, the server's top-N by its own key: sort here, say so in the caption.
  const [sort, setSort] = useState<{ key: SortKey; dir: 1 | -1 }>(DEFAULT_SORT);

  // Palette slots go to the named series in order; «Others» is neutral and takes none.
  const colorOf = useMemo(() => {
    const idx = new Map(data.series.filter((s) => !isOtherKey(s.key)).map((s, i) => [s.key, i]));
    return (key: string, i: number) => (isOtherKey(key) ? NEUTRAL : seriesColor(idx.get(key) ?? i));
  }, [data.series]);
  const othersLabel = st("row.others", "Others");

  const series = data.series.map((s, i) => ({ key: s.key, label: isOtherKey(s.key) ? othersLabel : s.label, color: colorOf(s.key, i), values: s.values }));
  const fmtValue = (v: number) => metricValue(metric, v, st);
  const rows = data.rows;
  const total = data.totals;
  const empty = total.turns === 0 && rows.every((r) => !r.turns && !r.value);
  const unknownPct = unknownShare(total.turns, total.turns_unknown);
  const hasCoverage = metric === "cost" || metric === "tokens";
  const dim = data.query.group_by;

  const sorted = useMemo(() => {
    const main = rows.filter((r) => !isOther(r));
    const others = rows.filter(isOther);
    const ordered = [...main].sort((a, b) => {
      const x = sortValue(a, sort.key);
      const y = sortValue(b, sort.key);
      return (x < y ? -1 : x > y ? 1 : 0) * sort.dir;
    });
    return [...ordered, ...others];
  }, [rows, sort]);

  const filterValue = (r: UsageRow) => (dim === "agent" || dim === "project" ? (r.link?.id ?? r.key) : r.key);
  const rowActions = (r: UsageRow): RowAction[] => {
    if (isOther(r)) return state.n < MAX_TOP ? [{ id: "expand", label: st("row.expand", "Expand"), run: () => onChange({ ...state, n: MAX_TOP }) }] : [];
    if (isUnattributed(r)) return [];
    const next = withFilter(state, filterOf(dim), filterValue(r));
    const out: RowAction[] = [{ id: "only", label: st("row.only", "Only this"), run: () => onChange(next) }];
    const href = r.link && (r.link.kind === "agent" || r.link.kind === "project") ? links.openHref?.(r.link) : undefined;
    if (href && links.open) out.push({ id: "open", label: st("row.open", "Open"), run: () => links.open!(href) });
    if (links.copySlice) out.push({ id: "link", label: st("row.link", "Link to this slice"), run: () => links.copySlice!(next) });
    return out;
  };

  const tooltip = (i: number): ReactNode => {
    const items = series.map((s) => ({ s, v: s.values[i] })).filter((x) => x.v !== null && x.v !== undefined && x.v > 0).sort((a, b) => b.v! - a.v!);
    const sum = items.reduce((a, x) => a + x.v!, 0);
    const cov = data.coverage?.by_bucket?.[i];
    return (
      <div className="space-y-1.5">
        <p className="font-semibold">{bucketTitle(data.buckets[i]!, data.query.step, tz)}</p>
        <ul className="space-y-0.5">
          {items.slice(0, 8).map(({ s, v }) => (
            <li key={s.key} className="flex items-center justify-between gap-4">
              <Name text={s.label} color={s.color} />
              <span className="tabular-nums font-medium">{fmtValue(v!)}</span>
            </li>
          ))}
        </ul>
        <div className="space-y-0.5 border-t border-border pt-1.5">
          <p className="flex justify-between gap-4 font-medium">
            <span>{st("total", "Total")}</span>
            <span className="tabular-nums">{fmtValue(sum)}</span>
          </p>
          {hasCoverage && cov && cov.turns > 0 && (
            <p className="flex justify-between gap-4 text-muted-foreground">
              <Name text={metric === "cost" ? st("tip.noSum", "No sum") : st("tip.noTokens", "No tokens")} hatch />
              <span className="tabular-nums">{st("tip.ofTurns", "{{n}} of {{total}} turns", { n: integer(cov.turns - cov.turns_known), total: integer(cov.turns) })}</span>
            </p>
          )}
        </div>
        {metric === "cost" && <PriceNote data={data} st={st} actual={isActualSpend(state)} />}
      </div>
    );
  };

  const onPick = (i: number) => {
    const b = data.buckets[i];
    if (b && data.query.step !== "hour") onChange(onlyDay(state, b));
  };

  if (empty) {
    return (
      <div className="rounded-lg border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground" data-testid="stats-empty">
        <p>
          {pinned === "project"
            ? st("emptyPinned.project", "No agent worked on this project in this period.")
            : pinned === "agent"
              ? st("emptyPinned.agent", "This agent made no turns in this period.")
              : st("empty", "No agent turns in this period. Take 30 days or another project.")}
        </p>
        {state.p !== "30d" && (
          // A pinned filter belongs to the host page: only the period widens, the pin stays.
          <button type="button" className="mt-3 rounded-lg border border-border px-3 py-1.5 text-foreground hover:bg-accent" onClick={() => onChange(pinned ? { ...state, p: "30d" } : { ...state, p: "30d", f: {} })}>
            {pinned ? st("emptyPinned.action", "Show 30 days") : st("emptyAction", "Show 30 days, all projects")}
          </button>
        )}
      </div>
    );
  }

  const others = rows.find(isOther);
  const shownCount = rows.filter((r) => !isOther(r)).length;
  // «Others (K)»: the server's own count when it sends one, else the full read's; unknown → no number.
  const allCount = typeof others?.other_count === "number" ? { all: shownCount + others.other_count, more: false } : othersCount && othersCount.all >= shownCount ? othersCount : null;
  const plus = allCount?.more ? "+" : "";
  const othersName = () => (allCount && allCount.all > shownCount ? `${othersLabel} (${allCount.all - shownCount}${plus})` : othersLabel);
  const rowName = (r: UsageRow) => (isOther(r) ? othersName() : r.label);
  const stackName = (r: UsageRow) => r.stack_label?.trim() || r.stack_key || "—";
  const sortCaption = sortName(sort, metric, dim, st, state);
  const head = valueHeader(metric, st, state);
  const showTokens = metric === "cost" || metric === "tokens";
  const sessionsUntil = data.coverage?.history_precision_until;
  const sessionsNote = sessionsUntil && data.buckets.length > 0 && data.buckets[0]! < sessionsUntil;
  const totalValue = total.value;

  const sortHeader = (key: SortKey, label: string, align: "left" | "right" = "right", width?: string) => (
    <th scope="col" className={cn("px-3 py-2 font-normal", align === "right" ? "text-right" : "text-left", width)} aria-sort={sort?.key === key ? (sort.dir === 1 ? "ascending" : "descending") : "none"}>
      <button
        type="button"
        className="inline-flex items-center gap-1 hover:text-foreground"
        onClick={() => setSort((s) => (s.key === key ? { key, dir: s.dir === 1 ? -1 : 1 } : { key, dir: key === "label" ? 1 : -1 }))}
      >
        {label}
        {sort?.key === key && (sort.dir === 1 ? <ArrowUp className="h-3 w-3" /> : <ArrowDown className="h-3 w-3" />)}
      </button>
    </th>
  );

  return (
    <div className="min-w-0 space-y-4 @container" data-testid="usage-panel">
      <section className={cn("min-w-0 space-y-3", mode === "page" && "rounded-xl border border-border bg-card p-4 @3xl:p-5")}>
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-6 gap-y-1" data-testid="usage-summary">
          <p className="min-w-0">
            <span className={cn("font-semibold tabular-nums", mode === "page" ? "text-3xl" : "text-xl")} data-testid="usage-total">
              {metric === "cost" && !isActualSpend(state) ? "≈ " : ""}
              {totalValue === null ? st("noValue", "none") : fmtValue(totalValue)}
            </span>{" "}
            <span className="text-sm text-muted-foreground">
              {metricCaption(metric, st, state)} {periodWord(state.p, st)}
            </span>
          </p>
          {metric !== "turns" && (
            <p className="text-sm text-muted-foreground">
              <span className="font-semibold text-foreground tabular-nums">{integer(total.turns)}</span> {st("summary.turns", "turns")}
            </p>
          )}
          {showTokens && total.tokens_in != null && (
            <p className="text-sm text-muted-foreground">
              <span className="font-semibold text-foreground tabular-nums">{compact(total.tokens_in)}</span>{" "}
              {total.cache_read != null ? st("summary.inCache", "input tokens, of them cache {{cache}}", { cache: compact(total.cache_read) }) : st("summary.in", "input tokens")}
            </p>
          )}
          {showTokens && total.tokens_out != null && (
            <p className="text-sm text-muted-foreground">
              <span className="font-semibold text-foreground tabular-nums">{compact(total.tokens_out)}</span> {st("summary.out", "output")}
            </p>
          )}
        </div>
        {hasCoverage && total.turns_unknown > 0 && (
          <p role="note" data-testid="partial-note" className="inline-flex max-w-full items-center gap-2 rounded-md bg-muted px-2 py-1 text-xs text-muted-foreground">
            <span aria-hidden="true" className="h-2.5 w-4 shrink-0 rounded-sm" style={{ background: HATCH }} />
            <span className="min-w-0">
              {metric === "cost"
                ? st("partial.cost", "{{n}} turns ({{pct}}) have no price or tokens: they are not in the sum and are shown apart", { n: integer(total.turns_unknown), pct: percent(unknownPct) })
                : st("partial.tokens", "{{n}} turns ({{pct}}) have no tokens: they are not in the sum and are shown apart", { n: integer(total.turns_unknown), pct: percent(unknownPct) })}
            </span>
          </p>
        )}
        {metric === "cost" && (
          <p className="text-xs text-muted-foreground" data-testid="cost-explainer">
            {isActualSpend(state)
              ? st("explain.actual", "Actual spend of API keys, as the provider bills it.")
              : st("explain.cost", "A notional equivalent: tokens × the model's API price. Subscriptions are paid separately.")}
          </p>
        )}
        <StackedColumns
          buckets={data.buckets}
          series={series}
          step={data.query.step}
          tz={tz}
          coverage={hasCoverage ? data.coverage?.by_bucket : null}
          format={fmtValue}
          axis={(v) => metricAxis(metric, v, st)}
          tooltip={tooltip}
          onPick={onPick}
          label={st("chart.label", "{{what}} by {{step}}", { what: head, step: data.query.step })}
          st={st}
          coverageLabel={metric === "cost" ? st("coverage.withCost", "with $") : st("coverage.withTokens", "known")}
          emptyMark={metric === "cost" ? st("chart.noCost", "no $") : undefined}
        />
        {mode === "page" && (
          <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
            {hasCoverage && (
              <>
                <span className="inline-flex items-center gap-1.5">
                  <span aria-hidden="true" className="h-2 w-4 rounded-sm bg-primary" />
                  {metric === "cost" ? st("legend.withSum", "turns with a sum") : st("legend.withTokens", "turns with tokens")}
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <span aria-hidden="true" className="h-2 w-4 rounded-sm" style={{ background: HATCH }} />
                  {metric === "cost" ? st("legend.noSum", "turns without a sum") : st("legend.noTokens", "turns without tokens")}
                </span>
              </>
            )}
            <span className="hidden @3xl:inline">
              {data.query.step === "hour"
                ? st("hint.rowOnly", "Click a table row to keep that {{dim}}", { dim: dimensionLabel(dim, st).toLowerCase() })
                : st("hint.click", "Click a column to keep that day, a table row to keep that {{dim}}", { dim: dimensionLabel(dim, st).toLowerCase() })}
            </span>
          </div>
        )}
        {sessionsNote && (
          <p className="text-[11px] text-muted-foreground">{st("sessionsNote", "Before {{date}} the data is per session, not per turn", { date: fmt.formatDate(sessionsUntil!) })}</p>
        )}
      </section>

      <section className={cn("min-w-0", mode === "page" && "overflow-hidden rounded-xl border border-border bg-card")} data-testid="usage-table">
        <div className={cn("flex min-w-0 flex-wrap items-baseline justify-between gap-2", mode === "page" ? "px-4 pb-2 pt-4" : "pb-2")}>
          <h2 className="text-sm font-semibold">{byDimension(dim, st)}{data.query.stack_by ? ` · ${st("table.inside", "inside: {{dim}}", { dim: dimensionLabel(data.query.stack_by, st).toLowerCase() })}` : ""}</h2>
          <p className="hidden text-[11px] text-muted-foreground @3xl:block" data-testid="usage-sort-caption">
            {st("table.sortedBy", "Sorted by: {{what}}.", { what: sortCaption.noun })}
            {others
              ? ` ${allCount ? st("table.shown", "Showing {{shown}} of {{all}}, «Others» below", { shown: shownCount, all: `${allCount.all}${plus}` }) : st("table.shownSome", "Showing {{shown}}, «Others» below", { shown: shownCount })}`
              : ""}
          </p>
          <p className="text-[11px] text-muted-foreground @3xl:hidden" data-testid="usage-sort-short">
            {sortCaption.by}
          </p>
        </div>

        {/* wide: a table with every column; the page never scrolls sideways, the table cells truncate */}
        <div className="hidden min-w-0 @3xl:block">
          <table className="w-full table-fixed text-sm">
            <thead className="border-y border-border text-[11px] text-muted-foreground">
              <tr>
                {sortHeader("label", dimensionLabel(dim, st), "left", "w-[26%]")}
                {sortHeader("value", head)}
                <th scope="col" className="w-[14%] px-3 py-2 text-left font-normal">{metric === "cost" ? st("col.shareCost", "Share of $") : st("col.share", "Share")}</th>
                {showTokens && sortHeader("in", st("col.in", "Input"))}
                {showTokens && sortHeader("out", st("col.out", "Output"))}
                {showTokens && sortHeader("cache", st("col.cache", "Cache"))}
                {sortHeader("turns", st("col.turnsCount", "Turns"))}
                {hasCoverage && sortHeader("unknown", unknownHeader(metric, st))}
                <th scope="col" className="w-36 px-3 py-2">
                  <span className="sr-only">{st("col.actions", "Actions")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {sorted.map((r, i) => {
                const acts = rowActions(r);
                const only = acts.find((a) => a.id === "only" || a.id === "expand");
                const share = r.share ?? (totalValue && r.value != null ? r.value / totalValue : 0);
                const color = isUnattributed(r) ? NEUTRAL : colorOf(r.key, i);
                return (
                  <tr
                    key={`${r.key}|${r.stack_key ?? ""}`}
                    data-row={r.key}
                    className={cn("border-b border-border last:border-b-0 hover:bg-muted/40", only && "cursor-pointer")}
                    // «Click a table row to keep it» (the hint under the chart): the whole row, not only the name; the
                    // row's own buttons and menu keep their action. Keyboard users have the name button.
                    onClick={only ? (e) => { if (!(e.target as HTMLElement).closest("button, a, [role='menu']")) only.run(); } : undefined}
                  >
                    <td className="px-3 py-2.5">
                      <button type="button" className="block w-full min-w-0 rounded text-left hover:text-primary focus-visible:outline-2 focus-visible:outline-ring" onClick={only?.run} disabled={!only}>
                        <Name text={rowName(r)} color={color} hatch={isUnattributed(r)} className="font-medium" />
                      </button>
                      {data.query.stack_by && <span className="block break-words pl-[18px] text-[11px] text-muted-foreground [overflow-wrap:anywhere]">{stackName(r)}</span>}
                    </td>
                    <td className="px-3 py-2.5 text-right font-semibold tabular-nums" title={metric === "cost" ? basisTitle(r, st) : undefined}>
                      {r.value === null ? <span className="font-normal text-muted-foreground">{st("noValue", "none")}</span> : fmtValue(r.value)}
                    </td>
                    <td className="px-3 py-2.5">
                      {r.value === null ? (
                        <span className="text-muted-foreground">—</span>
                      ) : (
                        <span className="flex min-w-0 items-center gap-2">
                          <ShareBar share={share} color={color} className="flex-1" />
                          <span className="w-12 shrink-0 text-right text-[11px] tabular-nums text-muted-foreground">{percent(share, 1)}</span>
                        </span>
                      )}
                    </td>
                    {showTokens && <td className="px-3 py-2.5 text-right tabular-nums">{r.tokens_in == null ? "—" : compact(r.tokens_in)}</td>}
                    {showTokens && <td className="px-3 py-2.5 text-right tabular-nums">{r.tokens_out == null ? "—" : compact(r.tokens_out)}</td>}
                    {showTokens && <td className="px-3 py-2.5 text-right tabular-nums">{cacheOf(r) == null ? "—" : compact(cacheOf(r)!)}</td>}
                    <td className="px-3 py-2.5 text-right tabular-nums">{integer(r.turns)}</td>
                    {hasCoverage && (
                      <td className="px-3 py-2.5 text-right">
                        <UnknownCell share={unknownShare(r.turns, r.turns_unknown)} />
                      </td>
                    )}
                    <td className="px-3 py-2.5">
                      <span className="flex items-center justify-end gap-1">
                        {only && (
                          <button type="button" className="whitespace-nowrap rounded-md px-1.5 py-0.5 text-xs font-medium text-primary hover:bg-primary/10" onClick={only.run}>
                            {only.label}
                          </button>
                        )}
                        <RowMenu actions={acts.filter((a) => a !== only)} label={st("row.menu", "Row actions: {{name}}", { name: rowName(r) })} />
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
            <tfoot className="border-t border-border bg-muted/50 font-semibold">
              <tr data-testid="usage-total-row">
                <td className="px-3 py-2.5">{st("total", "Total")}</td>
                <td className="px-3 py-2.5 text-right tabular-nums">{totalValue === null ? st("noValue", "none") : fmtValue(totalValue)}</td>
                <td />
                {showTokens && <td className="px-3 py-2.5 text-right tabular-nums">{total.tokens_in == null ? "—" : compact(total.tokens_in)}</td>}
                {showTokens && <td className="px-3 py-2.5 text-right tabular-nums">{total.tokens_out == null ? "—" : compact(total.tokens_out)}</td>}
                {showTokens && <td className="px-3 py-2.5 text-right tabular-nums">{cacheOf(total) == null ? "—" : compact(cacheOf(total)!)}</td>}
                <td className="px-3 py-2.5 text-right tabular-nums">{integer(total.turns)}</td>
                {hasCoverage && <td className="px-3 py-2.5 text-right tabular-nums">{percent(unknownPct)}</td>}
                <td />
              </tr>
            </tfoot>
          </table>
          {mode === "page" && (
            <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-4 gap-y-1 border-t border-border px-4 py-2 text-[11px] text-muted-foreground">
              <p className="min-w-0">
                {metric === "cost" ? st("footerCost", "Data: the runner's report of every turn. ≈ $ = tokens × the model's API price on the turn's date; «none»: the model has no price or the turn has no tokens") : st("footerPlain", "Data: the runner's report of every turn")}
              </p>
              {updatedAt != null && (
                <p className="shrink-0 tabular-nums" data-testid="usage-updated">
                  {st("updatedAt", "Updated {{time}} {{zone}}", { time: clock(updatedAt, tz), zone: zoneName(tz, st) })}
                </p>
              )}
            </div>
          )}
        </div>

        {/* narrow: one card per row, the arrow opens the same actions */}
        <ul className={cn("min-w-0 @3xl:hidden", mode === "page" ? "border-t border-border" : "rounded-lg border border-border")}>
          {sorted.map((r, i) => {
            const share = r.share ?? (totalValue && r.value != null ? r.value / totalValue : 0);
            const color = isUnattributed(r) ? NEUTRAL : colorOf(r.key, i);
            const name = rowName(r);
            return (
              <RowCard
                key={`${r.key}|${r.stack_key ?? ""}`}
                testId="usage-card"
                actions={rowActions(r)}
                label={st("row.menu", "Row actions: {{name}}", { name })}
                head={
                  <>
                    <Name text={name} color={color} hatch={isUnattributed(r)} className="flex-1 font-medium" />
                    <span className={cn("shrink-0 tabular-nums", r.value === null ? "text-muted-foreground" : "font-semibold")}>{r.value === null ? st("noValue", "none") : fmtValue(r.value)}</span>
                  </>
                }
              >
                {data.query.stack_by && <p className="mt-1 break-words text-[11px] text-muted-foreground [overflow-wrap:anywhere]">{stackName(r)}</p>}
                {!isOther(r) && (
                  <>
                    <ShareBar share={r.value === null ? 0 : share} color={color} className="mt-2" />
                    <div className="mt-2 grid grid-cols-3 gap-2">
                      {showTokens && <Stat label={st("col.out", "Output")} value={r.tokens_out == null ? "—" : compact(r.tokens_out)} />}
                      <Stat label={st("col.turnsCount", "Turns")} value={integer(r.turns)} />
                      {hasCoverage && <Stat label={unknownHeader(metric, st)} value={percent(unknownShare(r.turns, r.turns_unknown))} />}
                    </div>
                  </>
                )}
              </RowCard>
            );
          })}
        </ul>
      </section>

      {mode === "page" && (
        // The page scrolls inside <main> with p-3 / md:p-6: a sticky box stops at the padding edge, so the negative
        // bottom equal to that padding puts the bar flush on the screen edge with nothing showing under it.
        // At the end of the page the negative margin eats the page's pb-10 and main's padding, so the bar
        // stays on the edge there too instead of floating over an empty strip.
        <div className="sticky -bottom-3 z-10 -mx-3 -mb-[3.25rem] flex items-center justify-between gap-3 border-t border-border bg-background px-3 py-3 md:-bottom-6 md:-mx-6 md:-mb-16 md:px-6 @3xl:hidden" data-testid="sticky-total">
          <div className="min-w-0">
            <p className="truncate text-[11px] text-muted-foreground">
              {st("stickyTotal", "Total {{period}}", { period: periodWord(state.p, st) })}
              {metric === "cost" && !isActualSpend(state) ? `, ${st("caption.cost", "at API price")}` : ""}
            </p>
            <p className="truncate text-lg font-semibold tabular-nums">{totalValue === null ? st("noValue", "none") : fmtValue(totalValue)}</p>
          </div>
          {state.n < MAX_TOP && others && (
            <button type="button" className="shrink-0 rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-accent" onClick={() => onChange({ ...state, n: MAX_TOP })}>
              {st("row.expandAll", "Whole table")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

/** «API price as of 01.11.2026, claude-opus-5-5: $5 input, $25 output, $0.50 cache per 1M»; a model without one says so. */
export function PriceNote({ data, st, actual }: { data: UsageResponse; st: ST; actual?: boolean }) {
  const p = data.pricing;
  if (actual) return <p className="border-t border-border pt-1.5 text-[11px] text-muted-foreground">{st("tip.actual", "Actual spend of API keys")}</p>;
  if (!p || (!p.models.length && !p.no_price_models?.length)) return null;
  const date = (d: string) => fmt.formatDate(`${d.slice(0, 10)}T12:00:00Z`, { day: "2-digit", month: "2-digit", year: "numeric" });
  return (
    <div className="space-y-0.5 border-t border-border pt-1.5 text-[11px] leading-snug text-muted-foreground" data-testid="price-note">
      {p.models.map((m) => (
        <p key={`${m.model}@${m.valid_from}`}>
          {st("tip.price", "API price as of {{date}}, {{model}}: {{in}} input, {{out}} output, {{cache}} cache per 1M", {
            date: date(m.valid_from),
            model: m.model,
            in: money(m.input_per_mtok),
            out: money(m.output_per_mtok),
            cache: money(m.cache_read_per_mtok ?? 0),
          })}
        </p>
      ))}
      {p.no_price_models?.length ? <p>{st("tip.noPrice", "{{models}}: no price", { models: p.no_price_models.join(", ") })}</p> : null}
    </div>
  );
}
