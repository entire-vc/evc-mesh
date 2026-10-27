import { afterEach, describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";
import i18n, { APP_NAMESPACES, resources, setLanguage } from "@/i18n";
import { NAMESPACES } from "@/i18n/chunks";
import { formatDate, formatNumber, formatRelativeShort, formatRelativeTime } from "@/i18n/format";
import { normalizeLanguage, resolveLanguage } from "@/i18n/config";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("language switch", () => {
  it("renders the same key in en and ru without a reload", async () => {
    await setLanguage("en");
    expect(i18n.t("common:time.daysAgo", { count: 3 })).toBe("3d ago");
    await setLanguage("ru");
    expect(i18n.t("common:time.daysAgo", { count: 3 })).toBe("3 дн назад");
    expect(document.documentElement.lang).toBe("ru");
  });

  it("ignores unsupported codes and falls back to en for a missing key", async () => {
    await setLanguage("ru");
    expect(setLanguage("xx")).toBeUndefined();
    expect(i18n.language).toBe("ru");
    expect(i18n.t("common:no.such.key", { defaultValue: "" })).toBe("");
  });
});

describe("resolveLanguage", () => {
  it("prefers the account locale, then the browser, then en", () => {
    expect(resolveLanguage("ru", ["en-US"])).toBe("ru");
    expect(resolveLanguage(undefined, ["de-DE", "ru-RU"])).toBe("ru");
    expect(resolveLanguage("fr", ["de"])).toBe("en");
    expect(resolveLanguage("fr", ["de", "ru-RU"])).toBe("ru");
    expect(normalizeLanguage("RU_ru")).toBe("ru");
  });
});

describe("Intl formatters follow the UI language", () => {
  const d = "2026-10-13T14:05:00Z";
  const OPTS = { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" } as const;
  it("dates and numbers differ between en and ru", async () => {
    await setLanguage("en");
    const en = { date: formatDate(d, OPTS), num: formatNumber(1234567.5) };
    await setLanguage("ru");
    const ru = { date: formatDate(d, OPTS), num: formatNumber(1234567.5) };
    expect(en.date).toBe("Oct 13, 2026");
    expect(en.date).toMatch(/Oct/);
    expect(ru.date).toMatch(/окт/);
    expect(en.num).toBe("1,234,567.5");
    expect(ru.num).not.toBe(en.num);
  });

  it("short relative time is translated", async () => {
    const t = (k: string, o?: Record<string, unknown>) => i18n.t(`common:${k}`, o) as string;
    const fiveMinAgo = new Date(Date.now() - 5 * 60_000);
    await setLanguage("en");
    expect(formatRelativeShort(fiveMinAgo, t)).toBe("5m ago");
    await setLanguage("ru");
    expect(formatRelativeShort(fiveMinAgo, t)).toBe("5 мин назад");
  });

  it("long relative time is translated", async () => {
    const twoHoursAgo = new Date(Date.now() - 2 * 3600_000);
    await setLanguage("en");
    expect(formatRelativeTime(twoHoursAgo)).toBe("about 2 hours ago");
    await setLanguage("ru");
    expect(formatRelativeTime(twoHoursAgo)).toBe("около 2 часов назад");
  });
});

// Completeness: every key of en exists in ru and vice versa (no orphans).
function flatten(obj: Record<string, unknown>, prefix = ""): string[] {
  return Object.entries(obj).flatMap(([k, v]) =>
    v && typeof v === "object" ? flatten(v as Record<string, unknown>, `${prefix}${k}.`) : [`${prefix}${k}`],
  );
}

// Plural keys (`key_one`, `key_other`, ...) carry the CLDR categories of their
// own language: en has one/other, ru has one/few/many/other. A plural base in en
// therefore expects every category of the target language, not en's suffixes.
const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/;
function expectedKeys(enKeys: Iterable<string>, lang: string): Set<string> {
  const categories = new Intl.PluralRules(lang).resolvedOptions().pluralCategories;
  const out = new Set<string>();
  for (const k of enKeys) {
    const m = PLURAL_SUFFIX.exec(k);
    if (!m) {
      out.add(k);
      continue;
    }
    const base = k.slice(0, m.index);
    for (const c of categories) out.add(`${base}_${c}`);
  }
  return out;
}

describe("locale files are complete", () => {
  it("every namespace exists in every language", () => {
    const langs = fs.readdirSync(path.resolve(__dirname, "../../locales"));
    for (const lang of langs) expect(Object.keys(resources[lang] ?? {}).sort()).toEqual([...NAMESPACES].sort());
  });

  it("every en key is in ru, and ru has no keys en lacks", () => {
    const missing: string[] = [];
    for (const ns of NAMESPACES) {
      const en = expectedKeys(flatten(resources.en?.[ns] ?? {}), "ru");
      const ru = new Set(flatten(resources.ru?.[ns] ?? {}));
      for (const k of en) if (!ru.has(k)) missing.push(`ru/${ns}: missing ${k}`);
      // ru plural forms (_few/_many) have no en counterpart: they belong to the en plural key (_other).
      const pluralBase = (k: string) => k.replace(/_(zero|one|two|few|many|other)$/, "_other");
      for (const k of ru) if (!en.has(k) && !en.has(pluralBase(k))) missing.push(`ru/${ns}: orphan ${k}`);
    }
    expect(missing).toEqual([]);
  });

  it("expects every ru plural category for an en plural key", () => {
    expect([...expectedKeys(["a.n_one", "a.n_other", "b"], "ru")].sort()).toEqual(
      ["a.n_few", "a.n_many", "a.n_one", "a.n_other", "b"],
    );
  });
});

it("APP_NAMESPACES lists every namespace that has locale files", () => {
  expect([...APP_NAMESPACES].sort()).toEqual([...NAMESPACES].sort());
});
