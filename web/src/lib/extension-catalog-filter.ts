import type { CatalogEntry, ExtensionKind, TrustLevel } from "@/hooks/useExtensionCatalog";

export const CATEGORIES = [
  "development", "communication", "documents", "agents", "security", "analytics", "entire-vc",
] as const;
export const KINDS: ExtensionKind[] = ["integration", "plugin", "theme"];
export const TRUSTS: TrustLevel[] = ["builtin", "official", "verified", "community", "custom"];
export const SORTS = ["popular", "new", "updated"] as const;
export type CatalogSort = (typeof SORTS)[number];

export interface CatalogFilters {
  kinds: string[];
  categories: string[];
  trusts: string[];
  q: string;
  sort: CatalogSort;
}

function list(params: URLSearchParams, key: string): string[] {
  return (params.get(key) ?? "").split(",").filter(Boolean);
}

export function filtersFromParams(params: URLSearchParams): CatalogFilters {
  const sort = params.get("sort");
  return {
    kinds: list(params, "kind"),
    categories: list(params, "cat"),
    trusts: list(params, "trust"),
    q: params.get("q") ?? "",
    sort: (SORTS as readonly string[]).includes(sort ?? "") ? (sort as CatalogSort) : "popular",
  };
}

/** Writes the non-default filters back, leaving unrelated params (`tab`) alone. */
export function filtersToParams(base: URLSearchParams, f: CatalogFilters): URLSearchParams {
  const p = new URLSearchParams(base);
  const set = (k: string, v: string) => (v ? p.set(k, v) : p.delete(k));
  set("kind", f.kinds.join(","));
  set("cat", f.categories.join(","));
  set("trust", f.trusts.join(","));
  set("q", f.q);
  set("sort", f.sort === "popular" ? "" : f.sort);
  return p;
}

const time = (s: string) => (s ? Date.parse(s) || 0 : 0);

/**
 * The catalog has no install counter yet, so "popular" ranks what this
 * workspace already runs first, official before the rest, then by name. New and
 * updated sort by the instance timestamps and fall back to the same order.
 */
export function applyFilters(entries: CatalogEntry[], f: CatalogFilters): CatalogEntry[] {
  const q = f.q.trim().toLowerCase();
  const out = entries.filter((e) => {
    if (f.kinds.length && !f.kinds.includes(e.kind)) return false;
    if (f.categories.length && !f.categories.includes(e.category)) return false;
    if (f.trusts.length && !f.trusts.includes(e.trust)) return false;
    if (q && !`${e.name} ${e.description} ${e.publisher}`.toLowerCase().includes(q)) return false;
    return true;
  });
  const byName = (a: CatalogEntry, b: CatalogEntry) => a.name.localeCompare(b.name, "en");
  const popular = (a: CatalogEntry, b: CatalogEntry) =>
    Number(b.connected) - Number(a.connected) ||
    Number(b.trust === "official" || b.trust === "builtin") - Number(a.trust === "official" || a.trust === "builtin") ||
    byName(a, b);
  const cmp =
    f.sort === "new" ? (a: CatalogEntry, b: CatalogEntry) => time(b.createdAt) - time(a.createdAt) || popular(a, b)
    : f.sort === "updated" ? (a: CatalogEntry, b: CatalogEntry) => time(b.updatedAt) - time(a.updatedAt) || popular(a, b)
    : popular;
  return [...out].sort(cmp);
}
