// Views bundle loader (ADR D10). The bundle is an ES module built by the SDK
// with react, react-dom, jsx-runtime, react-i18next and the SDK itself as
// externals; the import map in index.html points those at the core's single
// copies (see shared.ts), so the theme never carries its own React.
//
// Module shape (what the SDK emits):
//   export default {
//     views: { "board.card": { contract: "^1", view: Component }, ... },
//   }
import type { SlotView, WidgetDescriptor } from "@/core/slots/types";
import { registerSlotView, unregisterThemeViews } from "@/core/slots/registry";
import { coreSlotVersion, satisfies } from "./versions";
import { reportThemeIssue } from "./log";

export interface BundleView {
  contract: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  view: SlotView<any, any, any>;
}
export interface ViewsBundle {
  views: Record<string, BundleView>;
  /** Descriptors of the theme's own dashboard widgets; each has a `dashboard.widget.<name>` view in `views`. */
  widgets?: WidgetDescriptor[];
}

export const BUNDLE_TIMEOUT_MS = 10_000;
// The server serves theme files only from here (themes.AssetsPath), same origin as the app.
const ASSET_PATH = /^\/api\/v1\/theme\/assets\/([0-9a-f]{64})\.(js|css)$/;

/**
 * Resolves `url` and accepts it only if it is a content-addressed theme asset of THIS origin
 * (a views bundle runs with the user's session, so a foreign origin must never get in).
 * Returns the absolute URL and the hash, or null.
 */
export function trustedThemeAsset(url: string, ext: "js" | "css"): { href: string; hash: string } | null {
  let u: URL;
  try {
    u = new URL(url, window.location.href);
  } catch {
    return null;
  }
  if (u.origin !== window.location.origin || u.username || u.password) return null;
  const m = ASSET_PATH.exec(u.pathname);
  if (!m || m[2] !== ext) return null;
  return { href: u.origin + u.pathname, hash: m[1]! };
}

// Theme development (`mesh-theme dev`, packages/mesh-theme-sdk/cli): in a DEV build of the instance only,
// localStorage[DEV_BUNDLE_KEY] names a bundle served by the author's machine, which replaces the
// installed theme's views. `import.meta.env.DEV` is false in a shipped build, so this whole path and
// the exemption from the same-origin rule below are removed from it at build time.
export const DEV_BUNDLE_KEY = "mesh-theme-dev-bundle";

/** The dev bundle URL the author set, or null (always null outside a DEV build). */
export function devBundleUrl(): string | null {
  if (!import.meta.env.DEV) return null;
  try {
    const raw = localStorage.getItem(DEV_BUNDLE_KEY);
    return raw && /^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?\//.test(raw) ? raw : null;
  } catch {
    return null;
  }
}

/** Imports the local dev bundle (never cached: every call is a fresh build). */
export async function loadDevViewsBundle(url: string): Promise<ViewsBundle> {
  const shared = await import("./shared");
  shared.exposeSharedModules();
  const mod = await import(/* @vite-ignore */ `${url}${url.includes("?") ? "&" : "?"}t=${Date.now()}`);
  const def = (mod as { default?: unknown }).default ?? mod;
  if (!isBundle(def)) throw new Error("dev bundle has no `views` export");
  return def;
}

/** Calls `onChange` whenever the dev server announces a rebuild (EventSource on its /events). */
export function watchDevBundle(url: string, onChange: () => void): () => void {
  const es = new EventSource(new URL("/events", url).href);
  let first = true;
  es.onmessage = () => {
    if (first) first = false; // the server greets with the current version; the page already has it
    else onChange();
  };
  return () => es.close();
}

// hash -> module. The URL carries the hash and is immutable, so the browser's
// HTTP cache serves repeat visits; this serves workspace switches in-session.
const loaded = new Map<string, Promise<ViewsBundle>>();

function isBundle(m: unknown): m is ViewsBundle {
  const v = (m as { views?: unknown } | null)?.views;
  return !!v && typeof v === "object";
}

/** Imports the bundle; rejects on timeout, a URL that is not a hashed asset, or a wrong shape. */
export function loadViewsBundle(url: string, timeoutMs = BUNDLE_TIMEOUT_MS): Promise<ViewsBundle> {
  const asset = trustedThemeAsset(url, "js");
  if (!asset) return Promise.reject(new Error("views_url is not a content-addressed asset of this origin"));
  const { hash, href } = asset;
  let p = loaded.get(hash);
  if (!p) {
    p = new Promise<ViewsBundle>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`bundle load timed out after ${timeoutMs} ms`)), timeoutMs);
      // The shared copies (all of React's neighbours and the primitives) are a lazy chunk: a workspace
      // with no theme views, and the login screen, never pay for them (perf-bundle budget).
      import("./shared")
        .then((shared) => {
          shared.exposeSharedModules();
          return import(/* @vite-ignore */ href);
        })
        .then(
        (mod) => {
          clearTimeout(timer);
          const def = (mod as { default?: unknown }).default ?? mod;
          if (isBundle(def)) resolve(def);
          else reject(new Error("bundle has no `views` export"));
        },
        (e) => {
          clearTimeout(timer);
          reject(e instanceof Error ? e : new Error(String(e)));
        },
      );
    });
    loaded.set(hash, p);
    // A failure must not be cached: the next visit retries.
    p.catch(() => loaded.delete(hash));
  }
  return p;
}

/**
 * Registers the views whose declared contract the core satisfies; every other
 * slot keeps `mesh-default` and one warning goes to the log (ADR D6).
 * Returns the slots that were registered.
 */
export function registerThemeViews(themeId: string, bundle: ViewsBundle): string[] {
  const ok: string[] = [];
  // A bundle that replaces an earlier one of the same theme must not leave the old views behind
  // for slots the new one omits or no longer satisfies.
  unregisterThemeViews(themeId);
  for (const [slot, entry] of Object.entries(bundle.views)) {
    const core = coreSlotVersion(slot);
    if (!core) {
      reportThemeIssue({ code: "contract-mismatch", theme: themeId, slot, message: "slot is not in this core's catalog" });
    } else if (typeof entry?.view !== "function" || typeof entry.contract !== "string") {
      reportThemeIssue({ code: "bundle-invalid", theme: themeId, slot, message: "view entry needs {contract, view}" });
    } else if (!satisfies(entry.contract, core)) {
      reportThemeIssue({
        code: "contract-mismatch",
        theme: themeId,
        slot,
        message: `theme wants ${entry.contract}, core implements ${core}; default view used`,
      });
    } else {
      registerSlotView(slot, themeId, entry.view);
      ok.push(slot);
    }
  }
  return ok;
}

const OWN_WIDGET_ID = /^([a-z0-9][a-z0-9-]*)\.([a-z][a-z0-9-]{0,39})$/;
const SIZE_AXIS = [1, 2, 3, 4];

function validSize(s: unknown, hMax: number): boolean {
  const v = s as { w?: number; h?: number } | null;
  return !!v && SIZE_AXIS.includes(v.w as number) && Number.isInteger(v.h) && v.h! >= 1 && v.h! <= hMax;
}

/**
 * The theme's own dashboard widgets: a descriptor counts only when its id is `<theme-id>.<name>`, its
 * `dashboard.widget.<name>` view was registered, and its sizes are inside the grid. Anything else is
 * reported and left out (the dashboard keeps drawing the rest). Returns the descriptors to list.
 */
export function ownWidgets(themeId: string, bundle: ViewsBundle, registeredSlots: string[]): WidgetDescriptor[] {
  const out: WidgetDescriptor[] = [];
  for (const d of bundle.widgets ?? []) {
    const m = OWN_WIDGET_ID.exec(typeof d?.id === "string" ? d.id : "");
    const bad = (message: string) => reportThemeIssue({ code: "bundle-invalid", theme: themeId, slot: `dashboard.widget.${m?.[2] ?? "?"}`, message });
    if (!m || m[1] !== themeId) bad(`widget id must be "${themeId}.<name>", got ${JSON.stringify(d?.id)}`);
    else if (!registeredSlots.includes(`dashboard.widget.${m[2]}`)) bad(`widget ${d.id} has no registered dashboard.widget.${m[2]} view`);
    else if (!d.title || !validSize(d.size?.default, 3) || !validSize(d.size?.min, 3) || !validSize(d.size?.max, 3)) bad(`widget ${d.id} needs a title and sizes w 1..4, h 1..3`);
    else out.push({ ...d, origin: "theme" });
  }
  return out;
}
