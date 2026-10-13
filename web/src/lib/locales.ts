// Single source of the interface languages. A third language is one more entry
// here plus its translation file; the server list (internal/domain/locale.go)
// and the CHECK constraints must be widened in the same change.
export const SUPPORTED_LOCALES = [
  { code: "en", label: "English" },
  { code: "ru", label: "Русский" },
] as const;

export type LocaleCode = (typeof SUPPORTED_LOCALES)[number]["code"];

export const DEFAULT_LOCALE: LocaleCode = "en";

export function isSupportedLocale(code: string | undefined | null): code is LocaleCode {
  return SUPPORTED_LOCALES.some((l) => l.code === code);
}

// Language to offer when the person never chose one: the browser language, if
// it is supported and differs from what is stored. Offered, never applied.
export function suggestedLocale(
  stored: string | undefined,
  setExplicitly: boolean | undefined,
  navigatorLanguage: string | undefined,
): LocaleCode | null {
  if (setExplicitly) return null;
  const base = (navigatorLanguage ?? "").toLowerCase().split("-")[0];
  if (!isSupportedLocale(base) || base === stored) return null;
  return base;
}
