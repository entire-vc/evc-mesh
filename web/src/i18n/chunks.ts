// Lazy namespace chunks: src/locales/<lang>/<namespace>.json except `common`
// (bundled by ./index.ts). Imported only through a dynamic import() from the
// i18next backend and from the authenticated shell.
import { DEFAULT_LANGUAGE } from "./config";

const files = import.meta.glob("../locales/*/*.json", { import: "default" }) as Record<
  string,
  () => Promise<Record<string, unknown>>
>;

const loaders: Record<string, () => Promise<Record<string, unknown>>> = {};
const names = new Set<string>();
for (const [p, load] of Object.entries(files)) {
  const m = /\/locales\/([^/]+)\/([^/]+)\.json$/.exec(p);
  const [, lang, ns] = m ?? [];
  if (!lang || !ns) continue;
  if (lang === DEFAULT_LANGUAGE) names.add(ns);
  if (ns !== "common") loaders[`${lang}/${ns}`] = load;
}

/** Every namespace of the default language (bundled `common` included). */
export const NAMESPACES = Array.from(names);

/** Reads one namespace of one language; a language without it falls back to en. */
export function loadChunk(lng: string, ns: string): Promise<Record<string, unknown>> {
  return loaders[`${lng}/${ns}`]?.() ?? Promise.resolve({});
}
