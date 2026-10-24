// Theme runtime: what happens when the workspace in the URL changes.
//   1. paint the cached theme for the slug (the pre-paint script did it already
//      on a reload; this covers in-app switches),
//   2. GET /api/v1/theme?workspace=<slug> (ETag revalidation by the browser),
//   3. apply tokens + CSS, cache them,
//   4. load the views bundle; failure turns the views layer off, tokens and CSS stay (ADR D8).
import { api } from "@/lib/api";
import { applyThemeStyles, clearWorkspaceStyles } from "./apply";
import { readThemeCache, writeThemeCache } from "./cache";
import { devBundleUrl, loadDevViewsBundle, loadViewsBundle, ownWidgets, registerThemeViews, trustedThemeAsset, watchDevBundle } from "./bundle";
import { errText, reportThemeIssue } from "./log";
import { useThemeStore } from "./store";
import { setThemeModes } from "./mode";
import { registerSlotViewOverride, setThemeSettings } from "@/core/slots/registry";
import { setThemeWidgets } from "@/core/dashboard/theme-widgets";

export interface ThemeAnswer {
  theme_id: string;
  tokens_css: string;
  theme_css_url: string;
  views_url: string;
  settings: Record<string, unknown>;
  modes?: string[];
  hash: string;
}

const DEFAULT_VIEWS = "mesh-default";

// Overrides of the theme whose views are live; dropped when the theme changes.
let disposers: Array<() => void> = [];
// Workspace whose views are live; another workspace never inherits them.
let viewsSlug: string | null = null;
// Closes the dev-bundle rebuild listener (DEV builds only, see bundle.ts devBundleUrl).
let stopDevWatch: (() => void) | null = null;
function dropViews(settings: Record<string, unknown> = {}): void {
  stopDevWatch?.();
  stopDevWatch = null;
  disposers.forEach((d) => d());
  disposers = [];
  setThemeWidgets([]);
  viewsSlug = null;
  setThemeSettings(settings);
  useThemeStore.getState().set({ viewsThemeId: DEFAULT_VIEWS, settings });
}

async function fetchText(url: string): Promise<string> {
  const asset = trustedThemeAsset(url, "css");
  if (!asset) throw new Error("theme_css_url is not a content-addressed asset of this origin");
  const r = await fetch(asset.href);
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return r.text();
}

/** Applies the theme of workspace `slug`. `isStale` lets the caller drop a result that arrived after a newer switch. */
export async function applyWorkspaceTheme(slug: string, isStale: () => boolean = () => false): Promise<void> {
  // Another workspace's views and settings must not render here, not even while this one loads
  // or if its theme never arrives.
  if (viewsSlug !== slug) dropViews();
  const cached = readThemeCache(slug);
  // No cache for this slug: the previous workspace's styles must not stay under it while the answer is pending.
  if (cached) applyThemeStyles(cached.tokens_css, cached.css);
  else clearWorkspaceStyles();
  // The previous workspace's forced mode must not outlive it if this theme never answers.
  setThemeModes(cached?.modes);

  let t: ThemeAnswer;
  try {
    t = await api<ThemeAnswer>(`/api/v1/theme?workspace=${encodeURIComponent(slug)}`);
  } catch (e) {
    if (isStale()) return; // a newer switch owns the page now; this failure is not about it
    reportThemeIssue({ code: "theme-fetch-failed", theme: slug, message: errText(e) });
    // Cached tokens stay; with none, the previous workspace's must not linger.
    if (!cached) clearWorkspaceStyles(); // keeps/restores the instance theme under the shell
    return;
  }
  if (isStale()) return;
  // "" is a valid answer: internal/themes/css.go BuildTokensCSS gives "" for mesh-default, and a workspace
  // on the default theme must drop the instance theme's tokens. Only a non-string is malformed.
  if (typeof t?.tokens_css !== "string") {
    if (isStale()) return; // never touch the page for an answer a newer switch already replaced
    reportThemeIssue({ code: "theme-fetch-failed", theme: slug, message: "answer has no tokens_css" });
    if (!cached) clearWorkspaceStyles(); // keeps/restores the instance theme under the shell
    return;
  }

  let css = cached && cached.hash === t.hash ? cached.css : "";
  if (t.theme_css_url && !css) {
    try {
      css = await fetchText(t.theme_css_url);
    } catch (e) {
      reportThemeIssue({ code: "bundle-load-failed", theme: t.theme_id, message: `css: ${errText(e)}` });
    }
    if (isStale()) return;
  }
  applyThemeStyles(t.tokens_css, css);
  setThemeModes(t.modes);
  writeThemeCache(slug, { hash: t.hash, tokens_css: t.tokens_css, css, ...(t.modes ? { modes: t.modes } : {}) });

  // Guarded here, not only inside devBundleUrl: a shipped build folds `dev` to null and drops the dev loader and watcher.
  const dev = import.meta.env.DEV ? devBundleUrl() : null;
  if (!t.views_url && !dev) {
    dropViews(t.settings ?? {});
    return;
  }
  try {
    const bundle = dev ? await loadDevViewsBundle(dev) : await loadViewsBundle(t.views_url);
    if (isStale()) return;
    if (dev && !stopDevWatch) stopDevWatch = watchDevBundle(dev, () => void applyWorkspaceTheme(slug));
    const ok = registerThemeViews(t.theme_id, bundle);
    disposers.forEach((d) => d());
    disposers = ok.map((slot) => registerSlotViewOverride(slot, bundle.views[slot]!.view));
    setThemeWidgets(ownWidgets(t.theme_id, bundle, ok));
    viewsSlug = slug;
    setThemeSettings(t.settings ?? {});
    useThemeStore.getState().set({ viewsThemeId: t.theme_id, settings: t.settings ?? {} });
  } catch (e) {
    if (isStale()) return;
    reportThemeIssue({ code: "bundle-load-failed", theme: t.theme_id, message: errText(e) });
    dropViews(t.settings ?? {});
  }
}

/** The user left the workspace routes (sign-out, login): nothing of its theme may stay on the screen. */
export function clearWorkspaceTheme(): void {
  setThemeModes(undefined);
  dropViews();
  clearWorkspaceStyles();
}
