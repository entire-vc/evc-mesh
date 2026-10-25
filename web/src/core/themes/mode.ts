// Colour mode: light | dark | system. The person's choice lives on the server
// (user_ui_state, scope `appearance`) so every device agrees; localStorage "theme"
// is the cache the pre-paint scripts read. A theme that ships one mode forces it.
import { create } from "zustand";
import { api } from "@/lib/api";

export type ColorMode = "light" | "dark" | "system";

const KEY = "theme";
const SCOPE = "/api/v1/me/ui-state/appearance";

// Bumped by every choice made on this page, so a sync that started earlier cannot overwrite it.
let choices = 0;

const isMode = (v: unknown): v is ColorMode => v === "light" || v === "dark" || v === "system";

const readKey = (key: string): string | null => {
  try {
    return localStorage.getItem(key);
  } catch {
    return null; // locked storage
  }
};

export function readLocalMode(): ColorMode {
  const v = readKey(KEY);
  return isMode(v) ? v : "system";
}

interface ModeState {
  chosen: ColorMode;
  /** The only mode the current theme ships, or null when it ships both. */
  forced: "light" | "dark" | null;
  /** Density chosen or pulled on this page (the painted value is on <html>); lets the profile control follow the sign-in pull. */
  density?: string;
}

export const useModeStore = create<ModeState>(() => ({ chosen: readLocalMode(), forced: null }));

// The `appearance` state is one object (mode + density). Density lives in density.ts (profile
// page only); here the value on <html> (set by index.html / density.ts / the sign-in sync; absent until
// chosen) is just carried along, because a PUT replaces the whole object.
const html = () => document.documentElement.dataset;

/** Locked storage: the choice lasts for this page only. */
export function remember(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* see above */
  }
}

// Bumped by every density choice, so a pull that started earlier cannot overwrite it (even with the same value).
let densityChoices = 0;
export const markDensityChoice = (): void => {
  densityChoices++;
};

// The sign-in pull in flight: a write waits for it, so it carries the server's density, not an unset one.
let pulling: Promise<unknown> = Promise.resolve();

export const saveAppearance = (mode: ColorMode) =>
  void pulling
    .then(() => api(SCOPE, { method: "PUT", body: { state: { mode, density: html().density } } }))
    .catch(() => {});

const query = () => window.matchMedia("(prefers-color-scheme: dark)");

export function effectiveDark(s: ModeState = useModeStore.getState()): boolean {
  const m = s.forced ?? s.chosen;
  return m === "system" ? query().matches : m === "dark";
}

function paint(): void {
  document.documentElement.classList.toggle("dark", effectiveDark());
}

useModeStore.subscribe(paint);

/** Follows the OS while the effective mode is `system`; call once, returns the unsubscribe. */
export function watchSystemMode(): () => void {
  const mq = query();
  const onChange = () => {
    const s = useModeStore.getState();
    if (!s.forced && s.chosen === "system") paint();
  };
  mq.addEventListener("change", onChange);
  return () => mq.removeEventListener("change", onChange);
}

/** The theme's `modes` list (undefined: no theme / unknown, nothing is forced). */
export function setThemeModes(modes: unknown): void {
  const list = Array.isArray(modes) ? modes.filter((m) => m === "light" || m === "dark") : [];
  const forced = list.length === 1 ? (list[0] as "light" | "dark") : null;
  if (useModeStore.getState().forced !== forced) useModeStore.setState({ forced });
  else paint();
}

/** Remembers the choice here at once, then on the server (best effort: offline still works locally). */
export function chooseMode(mode: ColorMode): void {
  choices++;
  remember(KEY, mode);
  useModeStore.setState({ chosen: mode });
  saveAppearance(mode);
}

/**
 * After sign-in: the server's choice wins on any device; with nothing stored yet,
 * an explicit local choice is uploaded so the next device gets it. An unreachable
 * server changes nothing.
 */
export const syncModeFromServer = (): Promise<void> => (pulling = pull());

async function pull(): Promise<void> {
  const seen = choices;
  const seenDensity = densityChoices;
  let server: ColorMode | null = null;
  try {
    const res = await api<{ state?: { mode?: unknown; density?: unknown } | null }>(SCOPE);
    if (isMode(res?.state?.mode)) server = res.state.mode;
    const d = res?.state?.density;
    // Same answer, second field: the server's density wins unless it was chosen meanwhile.
    if ((d === "compact" || d === "comfortable") && densityChoices === seenDensity) {
      html().density = d;
      remember("density", d);
      useModeStore.setState({ density: d });
    }
  } catch (err) {
    if ((err as { status?: number } | null)?.status !== 404) return;
  }
  if (choices !== seen) return; // the person chose while we waited; their choice is already saved
  if (server) {
    remember(KEY, server);
    if (useModeStore.getState().chosen !== server) useModeStore.setState({ chosen: server });
    return;
  }
  const local = readLocalMode();
  if (readKey(KEY) !== null) saveAppearance(local); // an explicit local choice is uploaded
}
