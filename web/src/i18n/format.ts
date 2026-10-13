// The only place that turns dates, numbers and durations into display text.
// Everything formats for the CURRENT UI language (not the browser's), so the
// same screen reads the same for everyone who chose that language. Do not call
// toLocale*String / Intl.* elsewhere - the guard in
// lib/__tests__/pinned-locale-number-format.test.ts fails on it.
import { formatDistanceToNow, parseISO } from "date-fns";
import { enUS } from "date-fns/locale/en-US";
import { ru } from "date-fns/locale/ru";
import { currentLanguage } from "./index";
import { intlLocale } from "./config";

type DateInput = string | number | Date;

const DATE_FNS_LOCALES = { en: enUS, ru } as const;

function toDate(v: DateInput): Date {
  return v instanceof Date ? v : typeof v === "string" ? parseISO(v) : new Date(v);
}

export function formatNumber(n: number, options?: Intl.NumberFormatOptions): string {
  return new Intl.NumberFormat(intlLocale(currentLanguage()), options).format(n);
}

/** "Oct 13, 2026" / "13 окт. 2026 г." */
export function formatDate(v: DateInput, options?: Intl.DateTimeFormatOptions): string {
  return new Intl.DateTimeFormat(
    intlLocale(currentLanguage()),
    options ?? { year: "numeric", month: "short", day: "numeric" },
  ).format(toDate(v));
}

/** Time of day, 24h. */
export function formatTime(v: DateInput, options?: Intl.DateTimeFormatOptions): string {
  return new Intl.DateTimeFormat(
    intlLocale(currentLanguage()),
    options ?? { hour: "2-digit", minute: "2-digit", hourCycle: "h23" },
  ).format(toDate(v));
}

/** "Oct 13, 2026 14:05" */
export function formatDateTime(v: DateInput): string {
  return `${formatDate(v)} ${formatTime(v)}`;
}

/** "about 2 hours ago" / "около 2 часов назад" */
export function formatRelativeTime(v: DateInput): string {
  return formatDistanceToNow(toDate(v), { addSuffix: true, locale: DATE_FNS_LOCALES[currentLanguage()] });
}

/** Compact "5m ago"-style form. Falls back to formatDate after a week. */
export function formatRelativeShort(v: DateInput, t: (key: string, o?: Record<string, unknown>) => string): string {
  const date = toDate(v);
  const diffSeconds = Math.floor((Date.now() - date.getTime()) / 1000);
  if (diffSeconds < 60) return t("time.justNow");
  const diffMinutes = Math.floor(diffSeconds / 60);
  if (diffMinutes < 60) return t("time.minutesAgo", { count: diffMinutes });
  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours < 24) return t("time.hoursAgo", { count: diffHours });
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 7) return t("time.daysAgo", { count: diffDays });
  return formatDate(date);
}
