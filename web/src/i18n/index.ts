import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import { DEFAULT_LANGUAGE, LANGUAGES, normalizeLanguage, resolveLanguage, type LanguageCode } from "./config";

// src/locales/<lang>/<namespace>.json - one namespace per section of the app.
// `common` is bundled (login and the language switch need it before anything
// loads); every other namespace is a separate chunk (see ./chunks) fetched per language, so
// the entry chunk does not grow with the number of translated screens and `ru`
// is never downloaded by an English user. Under vitest everything is bundled
// eagerly: tests render components synchronously.
const commonFiles = import.meta.glob("../locales/*/common.json", { eager: true, import: "default" }) as Record<
  string,
  Record<string, unknown>
>;
// Dead code in a production build (MODE is replaced by a literal): the JSON
// modules are side-effect free and are dropped, the chunks stay lazy.
const testFiles = (
  import.meta.env.MODE === "test"
    ? import.meta.glob("../locales/*/*.json", { eager: true, import: "default" })
    : {}
) as Record<string, Record<string, unknown>>;
const eagerFiles = { ...commonFiles, ...testFiles };

function parsePath(p: string): [string, string] | null {
  const m = /\/locales\/([^/]+)\/([^/]+)\.json$/.exec(p);
  return m?.[1] && m[2] ? [m[1], m[2]] : null;
}

export const resources: Record<string, Record<string, Record<string, unknown>>> = {};
for (const [p, json] of Object.entries(eagerFiles)) {
  const [lang, ns] = parsePath(p) ?? [];
  if (!lang || !ns) continue;
  (resources[lang] ??= {})[ns] = json;
}

/**
 * Namespaces the authenticated app needs. Listed by hand (not read from the
 * locale files) so the login entry does not carry the chunk table; a test
 * keeps it equal to the files in src/locales/en.
 */
export const APP_NAMESPACES = ["common", "activity", "attachDoc", "board", "list", "shell", "task"] as const;

/**
 * i18next backend: reads a namespace chunk on demand. The chunk table lives in
 * ./chunks, itself loaded lazily, so the login entry carries none of it.
 * Exported for tests.
 */
export const chunkBackend = {
  type: "backend" as const,
  init() {},
  read(lng: string, ns: string, cb: (err: Error | null, data?: Record<string, unknown>) => void) {
    import("./chunks").then(
      (m) => m.loadChunk(lng, ns).then((data) => cb(null, data), (err: unknown) => cb(asError(err))),
      (err: unknown) => cb(asError(err)),
    );
  },
};

function asError(err: unknown): Error {
  return err instanceof Error ? err : new Error(String(err));
}

export function browserLanguages(): readonly string[] {
  if (typeof navigator === "undefined") return [];
  return navigator.languages?.length ? navigator.languages : [navigator.language];
}

function reflectOnDocument(code: string) {
  if (typeof document !== "undefined") document.documentElement.lang = code;
}

const useBackend = import.meta.env.MODE !== "test";
void (useBackend ? i18n.use(chunkBackend) : i18n).use(initReactI18next).init({
  resources,
  partialBundledLanguages: true,
  lng: resolveLanguage(undefined, browserLanguages()),
  fallbackLng: DEFAULT_LANGUAGE,
  supportedLngs: LANGUAGES.map((l) => l.code),
  ns: ["common"], // the rest are added by loadNamespaces() (useTranslation)
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
