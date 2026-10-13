import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import { DEFAULT_LANGUAGE, LANGUAGES, normalizeLanguage, resolveLanguage, type LanguageCode } from "./config";

// src/locales/<lang>/<namespace>.json - one namespace per section of the app.
// Bundled eagerly: the files are small, and a language switch must not wait
// for the network.
const files = import.meta.glob("../locales/*/*.json", { eager: true, import: "default" }) as Record<
  string,
  Record<string, unknown>
>;

export const resources: Record<string, Record<string, Record<string, unknown>>> = {};
for (const [p, json] of Object.entries(files)) {
  const m = /\/locales\/([^/]+)\/([^/]+)\.json$/.exec(p);
  const [, lang, ns] = m ?? [];
  if (!lang || !ns) continue;
  (resources[lang] ??= {})[ns] = json;
}

export const NAMESPACES = Object.keys(resources[DEFAULT_LANGUAGE] ?? {});

export function browserLanguages(): readonly string[] {
  if (typeof navigator === "undefined") return [];
  return navigator.languages?.length ? navigator.languages : [navigator.language];
}

function reflectOnDocument(code: string) {
  if (typeof document !== "undefined") document.documentElement.lang = code;
}

void i18n.use(initReactI18next).init({
  resources,
  lng: resolveLanguage(undefined, browserLanguages()),
  fallbackLng: DEFAULT_LANGUAGE,
  supportedLngs: LANGUAGES.map((l) => l.code),
  ns: NAMESPACES,
  defaultNS: "common",
  interpolation: { escapeValue: false }, // React escapes
  returnNull: false,
  initAsync: false,
});
reflectOnDocument(i18n.language);
i18n.on("languageChanged", reflectOnDocument);

/** Switch the interface language without a reload. Unsupported codes are ignored. */
export function setLanguage(code: string | undefined | null): Promise<unknown> | undefined {
  const next = normalizeLanguage(code);
  if (!next || next === i18n.language) return undefined;
  return i18n.changeLanguage(next);
}

/** Current UI language (always a supported code). */
export function currentLanguage(): LanguageCode {
  return normalizeLanguage(i18n.language) ?? DEFAULT_LANGUAGE;
}

export default i18n;
