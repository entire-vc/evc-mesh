// Interface languages of the web app. A third language is one more entry here
// plus a src/locales/<code>/ directory (see docs/web-i18n.md). Display names of
// languages live in locales (common:language.<code>), not here. `en` is the
// source of truth and the fallback for any missing key.
export const LANGUAGES = [
  { code: "en", intl: "en-US" },
  { code: "ru", intl: "ru-RU" },
] as const;

export type LanguageCode = (typeof LANGUAGES)[number]["code"];

export const DEFAULT_LANGUAGE: LanguageCode = "en";

export function isSupportedLanguage(code: string | undefined | null): code is LanguageCode {
  return LANGUAGES.some((l) => l.code === code);
}

/** "ru-RU" / "RU" / "ru" -> "ru"; anything unsupported -> null. */
export function normalizeLanguage(code: string | undefined | null): LanguageCode | null {
  const base = (code ?? "").toLowerCase().split(/[-_]/)[0];
  return isSupportedLanguage(base) ? base : null;
}

/** BCP 47 tag handed to Intl for a UI language. */
export function intlLocale(code: string | undefined | null): string {
  return LANGUAGES.find((l) => l.code === normalizeLanguage(code))?.intl ?? "en-US";
}

/**
 * Language for the current person: the account setting (users.locale) wins;
 * before login (or when it is unset/unsupported) the browser language, then en.
 */
export function resolveLanguage(
  userLocale: string | undefined | null,
  browserLanguages: readonly string[] | undefined,
): LanguageCode {
  const fromUser = normalizeLanguage(userLocale);
  if (fromUser) return fromUser;
  for (const b of browserLanguages ?? []) {
    const n = normalizeLanguage(b);
    if (n) return n;
  }
  return DEFAULT_LANGUAGE;
}
