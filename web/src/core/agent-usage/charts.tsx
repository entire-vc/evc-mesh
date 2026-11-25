// Charts of the statistics view. Columns are HTML (a column is a button: focus and tap open the same tooltip the
// mouse does, and text never scales with the chart); the limit lines are an SVG drawn at the measured pixel width,
// so labels keep their size at 393 and at 1440. Colours come only from the chart tokens.
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { bucketLabel, percent, type ST } from "./format";
import type { UsageStep } from "./types";

export const SERIES = ["hsl(var(--chart-1))", "hsl(var(--chart-2))", "hsl(var(--chart-3))", "hsl(var(--chart-4))", "hsl(var(--chart-5))", "hsl(var(--chart-priority-high))", "hsl(var(--chart-priority-low))"] as const;
export const seriesColor = (i: number) => SERIES[i % SERIES.length]!;
export const NEUTRAL = "var(--status-backlog)";
/** Hatching of «no sum»: a striped neutral. */
export const HATCH = `repeating-linear-gradient(135deg, color-mix(in oklab, ${NEUTRAL} 55%, transparent) 0 2px, transparent 2px 5px)`;

/** 0, a round step, …: the axis top for `max` with `n` steps. */
export function niceMax(max: number, n = 4): number {
  if (!(max > 0)) return 1;
  const raw = max / n;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const f = raw / mag;
  const step = (f <= 1 ? 1 : f <= 1.5 ? 1.5 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 3 ? 3 : f <= 5 ? 5 : 10) * mag;
  return step * n;
}

export function useWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = useRef<T>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

export interface ColumnSeries {
  key: string;
  label: string;
  color: string;
  values: (number | null)[];
}

/** Every `every`-th label shows when the columns are too narrow for all. */
const labelEvery = (n: number, width: number) => Math.max(1, Math.ceil(n / Math.max(1, Math.floor(width / 34))));

/**
 * Stacked columns per bucket, the coverage strip under each (share of turns with a sum), and a tooltip a column
 * opens on hover, focus or tap. `onPick` is the click (keep this bucket).
 */
export function StackedColumns({
  buckets,
  series,
  step,
  tz,
  coverage,
  format,
  axis,
  tooltip,
  onPick,
  label,
  st,
  coverageLabel,
  emptyMark,
}: {
  buckets: string[];
  series: ColumnSeries[];
  step: UsageStep;
  tz: string;
  coverage?: { turns: number; turns_known: number }[] | null;
  format: (v: number) => string;
  axis: (v: number) => string;
  tooltip: (i: number) => ReactNode;
  onPick?: (i: number) => void;
  label: string;
  st: ST;
  /** Caption at the left of the coverage strip («with $»). */
  coverageLabel?: string;
  /** Text over a column whose turns all lack a value («no $»). */
  emptyMark?: string;
}) {
  const [box, width] = useWidth<HTMLDivElement>();
  const [open, setOpen] = useState<number | null>(null);
  const totals = buckets.map((_, i) => series.reduce((s, x) => s + (x.values[i] ?? 0), 0));
  const top = niceMax(Math.max(0, ...totals));
  const ticks = [4, 3, 2, 1, 0].map((k) => (top * k) / 4);
  const every = labelEvery(buckets.length, width || 600);
  const showPct = buckets.length <= 14 && (width || 600) / Math.max(1, buckets.length) >= 30;
  const H = 200;

  useEffect(() => {
    if (open === null) return;
    const close = (e: KeyboardEvent) => e.key === "Escape" && setOpen(null);
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [open]);

  return (
    <div className="min-w-0" data-testid="usage-chart">
      <div className="flex min-w-0 gap-2">
        <div className="relative w-11 shrink-0 text-right text-[11px] tabular-nums text-muted-foreground" style={{ height: H }} aria-hidden="true">
          {ticks.map((v, k) => (
            <span key={k} className="absolute right-0 -translate-y-1/2 truncate" style={{ top: `${(k / 4) * 100}%` }}>
              {axis(v)}
            </span>
          ))}
        </div>
        <div ref={box} className="relative min-w-0 flex-1" style={{ height: H }} role="group" aria-label={label} onMouseLeave={() => setOpen(null)}>
          {ticks.map((_, k) => (
            <div key={k} className="pointer-events-none absolute inset-x-0 border-t border-border/70" style={{ top: `${(k / 4) * 100}%` }} />
          ))}
          <div className="absolute inset-0 flex items-end">
            {buckets.map((b, i) => {
              const total = totals[i]!;
              const cov = coverage?.[i];
              const none = cov && cov.turns > 0 && cov.turns_known === 0;
              return (
                <div key={b} className="relative flex h-full min-w-0 flex-1 items-end justify-center px-0.5">
                  <button
                    type="button"
                    data-bucket={b}
                    aria-label={`${bucketLabel(b, step, tz).main}: ${format(total)}`}
                    aria-expanded={open === i}
                    className="group flex h-full w-3/5 flex-col justify-end rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    onMouseEnter={() => setOpen(i)}
                    onFocus={() => setOpen(i)}
                    onBlur={() => setOpen((o) => (o === i ? null : o))}
                    onClick={() => onPick?.(i)}
                  >
                    {none && emptyMark && <span className="mb-1 w-full truncate text-center text-[10px] text-muted-foreground">{emptyMark}</span>}
                    {series.map((s) => {
                      const v = s.values[i] ?? 0;
                      if (!(v > 0)) return null;
                      return <span key={s.key} className="block w-full first:rounded-b-sm last:rounded-t-sm group-hover:opacity-90" style={{ height: `${(v / top) * 100}%`, backgroundColor: s.color, minHeight: 2 }} />;
                    })}
                  </button>
                </div>
              );
            })}
          </div>
          {open !== null && (
            <div
              role="tooltip"
              data-testid="column-tooltip"
              className={cn(
                "pointer-events-none absolute top-2 z-20 w-max max-w-[min(20rem,90vw)] rounded-lg border border-border bg-popover p-3 text-xs text-popover-foreground shadow-lg",
              )}
              style={open >= buckets.length / 2 ? { right: 0 } : { left: 0 }}
            >
              {tooltip(open)}
            </div>
          )}
        </div>
      </div>
      <div className="ml-[3.25rem] flex min-w-0" aria-hidden="true">
        {buckets.map((b, i) => {
          const l = bucketLabel(b, step, tz);
          return (
            <div key={b} className="min-w-0 flex-1 truncate pt-1 text-center text-[11px] tabular-nums text-muted-foreground">
              {i % every === 0 ? (
                <>
                  {l.main}
                  {l.sub && width / buckets.length >= 44 ? ` ${l.sub}` : ""}
                </>
              ) : (
                " "
              )}
            </div>
          );
        })}
      </div>
      {coverage && coverage.length === buckets.length && (
        <div className="mt-1 flex min-w-0 items-start gap-2" data-testid="coverage-strip">
          {/* The caption sits in the axis column; a longer word («known», «известны») runs out to the left into the card's
              padding instead of being cut to «извес…». */}
          <span className="flex w-11 shrink-0 justify-end pt-0.5">
            <span className="whitespace-nowrap text-[10px] text-muted-foreground" data-testid="coverage-label">{coverageLabel}</span>
          </span>
          <div className="flex min-w-0 flex-1">
            {coverage.map((c, i) => {
              const share = c.turns > 0 ? c.turns_known / c.turns : 1;
              return (
                <div key={i} className="min-w-0 flex-1 px-0.5" title={st("coverage.title", "{{known}} of {{turns}} turns with a value", { known: c.turns_known, turns: c.turns })}>
                  <div className="relative mx-auto h-2 w-3/5 overflow-hidden rounded-sm" style={{ background: c.turns > 0 ? HATCH : "transparent" }}>
                    <div className="absolute inset-y-0 left-0 bg-primary" style={{ width: `${share * 100}%`, minWidth: share > 0 && c.turns > 0 ? 2 : 0 }} />
                  </div>
                  {showPct && <div className="truncate pt-0.5 text-center text-[10px] tabular-nums text-muted-foreground">{c.turns > 0 ? percent(share) : "—"}</div>}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

/** Room in px between two day labels for «15 вс». At 393 a week leaves about 45 px and reads «15 16 …», as its mockup
 *  does; at 1440 it reads «15 вс … 21 сб» like the «≈ $» columns. */
const WEEKDAY_ROOM = 64;

/**
 * Axis labels of the limit lines. Days read «15 вс», as the «≈ $» columns do at 1440, when the labels have room.
 * Hours over several days are labelled by day, at each day's first hour (a week of hourly points would otherwise read
 * «14:00 … 14:00»); hours of one day read «14:00».
 */
function ticksOf(buckets: string[], step: UsageStep, tz: string, every: number, room = Infinity): { i: number; text: string }[] {
  if (step === "hour" && buckets.length > 26) {
    const out: { i: number; text: string }[] = [];
    let prev = "";
    buckets.forEach((b, i) => {
      const d = bucketLabel(b, "day", tz);
      const text = d.sub ? `${d.main} ${d.sub}` : d.main;
      if (text !== prev) out.push({ i, text });
      prev = text;
    });
    const room = Math.max(1, Math.ceil(out.length / Math.max(1, Math.floor(buckets.length / every))));
    return out.filter((_, k) => k % room === 0);
  }
  const withDay = room >= WEEKDAY_ROOM;
  return buckets
    .map((b, i) => {
      const l = bucketLabel(b, step, tz);
      return { i, text: l.sub && withDay ? `${l.main} ${l.sub}` : l.main };
    })
    .filter(({ i }) => i % every === 0);
}

/** Lines of shares 0..1 per pool, the threshold dashed, the last point marked by a dotted vertical «now» line. */
export function QuotaLines({
  buckets,
  series,
  step,
  tz,
  threshold,
  thresholdLabel,
  nowLabel,
  label,
}: {
  buckets: string[];
  series: ColumnSeries[];
  step: UsageStep;
  tz: string;
  threshold: number;
  thresholdLabel: string;
  nowLabel: string;
  label: string;
}) {
  const [box, width] = useWidth<HTMLDivElement>();
  const W = Math.max(width, 200);
  const H = 220;
  // The right pad holds half of the last axis label («20 пт», «14:00»), which is centred on the «now» line.
  const pad = { top: 10, right: 18, bottom: 22, left: 40 };
  const cw = W - pad.left - pad.right;
  const ch = H - pad.top - pad.bottom;
  const n = buckets.length;
  const x = (i: number) => pad.left + (n > 1 ? (i / (n - 1)) * cw : cw / 2);
  const y = (v: number) => pad.top + ch - Math.min(1, Math.max(0, v)) * ch;
  const every = labelEvery(n, cw);
  const room = n > 1 ? (every * cw) / (n - 1) : cw;
  const path = (vals: (number | null)[]) => {
    let d = "";
    let pen = false;
    vals.forEach((v, i) => {
      if (v === null || v === undefined) {
        pen = false;
        return;
      }
      d += `${pen ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)} `;
      pen = true;
    });
    return d.trim();
  };
  return (
    <div ref={box} className="min-w-0" data-testid="quota-chart">
      {width > 0 && (
        <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} role="img" aria-label={label} className="block max-w-full">
          {[0, 0.25, 0.5, 0.75, 1].map((f) => (
            <g key={f}>
              <line x1={pad.left} x2={pad.left + cw} y1={y(f)} y2={y(f)} stroke="currentColor" strokeOpacity={0.1} />
              <text x={pad.left - 6} y={y(f) + 4} textAnchor="end" fontSize={11} fill="currentColor" fillOpacity={0.55}>
                {percent(f)}
              </text>
            </g>
          ))}
          <line x1={pad.left} x2={pad.left + cw} y1={y(threshold)} y2={y(threshold)} stroke="hsl(var(--destructive))" strokeDasharray="4 4" strokeOpacity={0.8} />
          <text x={pad.left + cw / 2} y={y(threshold) - 4} textAnchor="middle" fontSize={11} fill="hsl(var(--destructive))">
            {thresholdLabel}
          </text>
          {series.map((s) => (
            <g key={s.key} data-pool={s.key}>
              <path d={path(s.values)} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round">
                <title>{s.label}</title>
              </path>
              {(() => {
                const last = s.values.length - 1;
                const v = s.values[last];
                return v === null || v === undefined ? null : <circle cx={x(last)} cy={y(v)} r={3.5} fill={s.color} />;
              })()}
            </g>
          ))}
          {n > 0 && (
            <g data-testid="quota-now">
              <line x1={x(n - 1)} x2={x(n - 1)} y1={pad.top} y2={pad.top + ch} stroke="currentColor" strokeOpacity={0.45} strokeDasharray="2 3" />
              <text x={x(n - 1) - 6} y={pad.top + ch - 6} textAnchor="end" fontSize={10} fill="currentColor" fillOpacity={0.55}>
                {nowLabel}
              </text>
            </g>
          )}
          {ticksOf(buckets, step, tz, every, room).map(({ i, text }) => (
            <text key={buckets[i]} x={x(i)} y={H - 6} textAnchor="middle" fontSize={11} fill="currentColor" fillOpacity={0.55}>
              {text}
            </text>
          ))}
        </svg>
      )}
    </div>
  );
}
