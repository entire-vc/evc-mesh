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
  /** Memoize board columns and cards (perf·Б3) so a drag/drop or a store
   * update outside a column only re-renders the cards that actually changed,
   * instead of every card on the board. Off falls back to the pre-memo
   * behavior — every card and column re-renders on every board state change,
   * same as before this flag existed. */
  boardCardMemo: boolean;
}

const DEFAULT_FLAGS: RuntimeFlags = {
  prefetchNextRoute: true,
  boardCardMemo: true,
};

declare global {
  interface Window {
    __MESH_FLAGS__?: Partial<RuntimeFlags>;
  }
}

let loaded: Promise<RuntimeFlags> | null = null;

const FALSY_FLAG_STRINGS = new Set(["false", "0", "off"]);
const TRUTHY_FLAG_STRINGS = new Set(["true", "1", "on"]);

/** A flag read from JSON (`/config.json` or `window.__MESH_FLAGS__`) can be
 * anything a human typed by hand. `{...DEFAULT_FLAGS, ...fromConfig}` used to
 * trust it verbatim, so `"prefetchNextRoute": "false"` (a string, truthy in
 * JS) merged in as-is and the `if (!flags.prefetchNextRoute) return;` kill
 * switch never fired — fail-open on exactly the kind of typo someone makes
 * editing the file by hand during an incident. Only a real boolean or one of
 * the common textual spellings changes the flag; anything else is logged and
 * ignored so a typo shows up in the console instead of silently no-op'ing. */
function normalizeFlagValue(name: string, value: unknown, fallback: boolean): boolean {
  if (typeof value === "boolean") return value;
  if (typeof value === "string") {
    const normalized = value.trim().toLowerCase();
    if (FALSY_FLAG_STRINGS.has(normalized)) return false;
    if (TRUTHY_FLAG_STRINGS.has(normalized)) return true;
  }
  console.warn(
    `[runtime-flags] ${name} = ${JSON.stringify(value)} is not a recognized boolean value — falling back to ${fallback}.`,
  );
  return fallback;
}

function normalizeFlags(raw: Partial<RuntimeFlags> | undefined): Partial<RuntimeFlags> {
  if (!raw || typeof raw !== "object") return {};
  const result: Partial<RuntimeFlags> = {};
  for (const key of Object.keys(DEFAULT_FLAGS) as (keyof RuntimeFlags)[]) {
    if (!(key in raw)) continue;
    result[key] = normalizeFlagValue(key, raw[key], DEFAULT_FLAGS[key]);
  }
  return result;
}

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
        ...normalizeFlags(fromConfig),
        ...normalizeFlags(window.__MESH_FLAGS__),
      };
      window.__MESH_FLAGS__ = merged;
      return merged;
    });
  }
  return loaded;
}
