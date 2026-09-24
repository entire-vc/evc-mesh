/** Runtime kill switches for perf features that ops needs to be able to
 * turn off without a rebuild/redeploy (perf·Б1, fleet-epic #3e9f036e).
 *
 * Route-splitting itself has no such switch — it's a single build, "off"
 * would mean shipping a second bundle, so reverting it means reverting the
 * image (documented on the card, not implemented here).
 *
 * `window.__MESH_FLAGS__` can be set synchronously by anything that runs
 * before this module's first read (an inline <script> in index.html, a
 * Playwright `addInitScript`, a browser extension) and always wins. When
 * it isn't set, this fetches /config.json — a plain static file the
 * deployed image can overwrite in place, so a flag flip doesn't need a
 * rebuild — and merges it in. Every flag defaults to enabled: absence of
 * /config.json (404, offline, slow network) must never disable a feature
 * that was never meant to depend on it.
 */

export interface RuntimeFlags {
  /** Prefetch the next likely route's JS chunk once idle. */
  prefetchNextRoute: boolean;
}

const DEFAULT_FLAGS: RuntimeFlags = {
  prefetchNextRoute: true,
};

declare global {
  interface Window {
    __MESH_FLAGS__?: Partial<RuntimeFlags>;
  }
}

let loaded: Promise<RuntimeFlags> | null = null;

async function fetchConfigFlags(): Promise<Partial<RuntimeFlags>> {
  try {
    const res = await fetch("/config.json", { cache: "no-store" });
    if (!res.ok) return {};
    const json = await res.json();
    return json && typeof json === "object" ? (json as Partial<RuntimeFlags>) : {};
  } catch {
    // Offline, 404 (no config.json deployed yet), bad JSON — flags stay at
    // their enabled-by-default value. Never let this block or throw.
    return {};
  }
}

/** Resolves once with the effective flags for this page load. Safe to call
 * many times — the fetch only happens once. */
export function loadRuntimeFlags(): Promise<RuntimeFlags> {
  if (!loaded) {
    loaded = fetchConfigFlags().then((fromConfig) => {
      const merged: RuntimeFlags = {
        ...DEFAULT_FLAGS,
        ...fromConfig,
        ...window.__MESH_FLAGS__,
      };
      window.__MESH_FLAGS__ = merged;
      return merged;
    });
  }
  return loaded;
}
