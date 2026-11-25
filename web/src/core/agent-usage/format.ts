// Copy and number formats of the statistics view. Copy lives in `notifications:dashboard.agentUsage.*` (English
// default in code, Russian in locales/ru); numbers and dates follow the UI language through i18n/format.
import { useTranslation } from "react-i18next";
import * as fmt from "@/i18n/format";
import type { StatsPeriod } from "./state";
import type { TokenKind, UsageMetric, UsageStep } from "./types";

export type ST = (key: string, en: string, opts?: Record<string, unknown>) => string;

export function useST(): ST {
  const { t } = useTranslation("notifications");
  return (key, en, opts) => t(`dashboard.agentUsage.${key}`, { defaultValue: en, ...opts });
}

/** «$1 724,89»; a positive amount under a cent is «<$0.01», not $0.00. */
export function money(v: number): string {
  if (v > 0 && v < 0.01) return `<$${fmt.formatNumber(0.01)}`;
  return `$${fmt.formatNumber(v, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

/** Axis money: whole dollars. */
export const moneyAxis = (v: number) => `$${fmt.formatNumber(Math.round(v))}`;

/** «22,0 млрд», «80,0 млн», «30 тыс.» (compact, one decimal). */
export function compact(n: number): string {
  if (Math.abs(n) < 1000) return fmt.formatNumber(n);
  return fmt.formatNumber(n, { notation: "compact", minimumFractionDigits: 1, maximumFractionDigits: 1 });
}

export const integer = (n: number) => fmt.formatNumber(Math.round(n));
export const percent = (share: number, digits = 0) =>
  fmt.formatNumber(share, { style: "percent", minimumFractionDigits: digits, maximumFractionDigits: digits });

export function hours(ms: number, st: ST): string {
  return st("unit.hours", "{{n}} h", { n: fmt.formatNumber(ms / 3_600_000, { maximumFractionDigits: 1 }) });
}

/** How a value of the metric reads. */
export function metricValue(metric: UsageMetric, v: number, st: ST): string {
  switch (metric) {
    case "cost":
      return money(v);
    case "tokens":
      return compact(v);
    case "duration":
      return hours(v, st);
    default:
      return integer(v);
  }
}

export function metricAxis(metric: UsageMetric, v: number, st: ST): string {
  return metric === "cost" ? moneyAxis(v) : metricValue(metric, v, st);
}

/** Column label of a bucket: «12 чт» per day, «14:00» per hour, «12 ноя» per week. */
export function bucketLabel(iso: string, step: UsageStep, tz: string): { main: string; sub?: string } {
  const d = new Date(iso);
  if (step === "hour") return { main: fmt.formatTime(d, { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: tz }) };
  if (step === "week") return { main: fmt.formatDate(d, { day: "numeric", month: "short", timeZone: tz }) };
  return {
    main: fmt.formatDate(d, { day: "numeric", timeZone: tz }),
    sub: fmt.formatDate(d, { weekday: "short", timeZone: tz }),
  };
}

/** Tooltip title of a bucket: «15 ноября, вс» / «15 ноября, 14:00». */
export function bucketTitle(iso: string, step: UsageStep, tz: string): string {
  const d = new Date(iso);
  const day = fmt.formatDate(d, { day: "numeric", month: "long", weekday: step === "hour" ? undefined : "short", timeZone: tz });
  if (step === "hour") return `${day}, ${fmt.formatTime(d, { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: tz })}`;
  return day;
}

export function dayMonth(iso: string, tz?: string): string {
  return fmt.formatDate(new Date(iso), { day: "numeric", month: "short", timeZone: tz });
}

export function dayMonthTime(iso: string, tz?: string): string {
  const d = new Date(iso);
  return `${fmt.formatDate(d, { day: "numeric", month: "short", timeZone: tz })} ${fmt.formatTime(d, { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: tz })}`;
}

/** «16:40» in the zone. */
export function clock(ms: number, tz: string): string {
  return fmt.formatTime(new Date(ms), { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: tz });
}

/** «MSK» for Moscow time (the page's zone), the IANA name otherwise. */
export function zoneName(tz: string, st: ST): string {
  return tz === "Europe/Moscow" ? st("zone.msk", "MSK") : tz;
}

/** «12–18 ноя» for the server's from/to (to is exclusive when it is midnight). */
export function rangeLabel(from: string, to: string, tz: string): string {
  const a = new Date(from);
  let b = new Date(to);
  if (b.getTime() - a.getTime() > 3_600_000) b = new Date(b.getTime() - 1);
  const da = fmt.formatDate(a, { day: "numeric", month: "short", timeZone: tz });
  const db = fmt.formatDate(b, { day: "numeric", month: "short", timeZone: tz });
  return da === db ? da : `${da} – ${db}`;
}

export function periodWord(p: StatsPeriod, st: ST): string {
  if (p === "1d") return st("period.word1d", "for 1 day");
  if (p === "7d") return st("period.word7d", "for 7 days");
  if (p === "30d") return st("period.word30d", "for 30 days");
  return p.from === p.to ? st("period.wordDay", "for {{day}}", { day: dayMonth(`${p.from}T12:00:00Z`) }) : st("period.wordRange", "for {{from}} – {{to}}", { from: dayMonth(`${p.from}T12:00:00Z`), to: dayMonth(`${p.to}T12:00:00Z`) });
}

export function stepWord(step: UsageStep, st: ST): string {
  return step === "hour" ? st("step.hour", "hour") : step === "week" ? st("step.week", "week") : st("step.day", "day");
}

export function tokenKindLabel(k: TokenKind, st: ST): string {
  switch (k) {
    case "in":
      return st("tokens.in", "Input");
    case "out":
      return st("tokens.out", "Output");
    case "cache_read":
      return st("tokens.cache_read", "Cache read");
    case "cache_write":
      return st("tokens.cache_write", "Cache write");
    default:
      return st("tokens.total", "All tokens");
  }
}
