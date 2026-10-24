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

export function readLocalMode(): ColorMode {
  try {
    const v = localStorage.getItem(KEY);
    return isMode(v) ? v : "system";
  } catch {
    return "system";
  }
}

interface ModeState {
  chosen: ColorMode;
  /** The only mode the current theme ships, or null when it ships both. */
  forced: "light" | "dark" | null;
}

export const useModeStore = create<ModeState>(() => ({ chosen: readLocalMode(), forced: null }));

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
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    /* locked storage: the choice lasts for this page only */
  }
  useModeStore.setState({ chosen: mode });
  void api(SCOPE, { method: "PUT", body: { state: { mode } } }).catch(() => {});
}

/**
 * After sign-in: the server's choice wins on any device; with nothing stored yet,
 * an explicit local choice is uploaded so the next device gets it. An unreachable
 * server changes nothing.
 */
export async function syncModeFromServer(): Promise<void> {
  const seen = choices;
  let server: ColorMode | null = null;
  try {
    const res = await api<{ state?: { mode?: unknown } | null }>(SCOPE);
    if (isMode(res?.state?.mode)) server = res.state.mode;
  } catch (err) {
    if ((err as { status?: number } | null)?.status !== 404) return;
  }
  if (choices !== seen) return; // the person chose while we waited; their choice is already saved
  if (server) {
    try {
      localStorage.setItem(KEY, server);
    } catch {
      /* see chooseMode */
    }
    if (useModeStore.getState().chosen !== server) useModeStore.setState({ chosen: server });
    return;
  }
  const local = readLocalMode();
  let explicit = false;
  try {
    explicit = localStorage.getItem(KEY) !== null;
  } catch {
    /* nothing to upload */
  }
  if (explicit) void api(SCOPE, { method: "PUT", body: { state: { mode: local } } }).catch(() => {});
}
