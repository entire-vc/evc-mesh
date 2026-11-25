// Permissions shared by statistics controls, panels and dashboard settings.
import { create } from "zustand";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { DIMENSIONS, type StatsState } from "./state";
import type { UsageCapabilities, UsageDimension } from "./types";

/** Last capabilities the server sent, per workspace or @me (the views of the switch before the next answer lands). */
export const useCapsStore = create<{ byWs: Record<string, UsageCapabilities>; set: (ws: string, c: UsageCapabilities) => void }>((set) => ({
  byWs: {},
  set: (ws, c) => set((s) => ({ byWs: { ...s.byWs, [ws]: c } })),
}));
useAuthStore.subscribe((state, prev) => {
  if (state.user?.id !== prev.user?.id) useCapsStore.setState({ byWs: {} });
});

export type UsageScope = "workspace" | "user";
export const capabilitiesKey = (ws: string, scope: UsageScope): string => (scope === "user" ? "@me" : ws);

export function useCapabilities(scope: UsageScope = "workspace"): UsageCapabilities | undefined {
  const ws = useWorkspaceStore((s) => s.currentWorkspace?.id);
  return useCapsStore((s) => (ws ? s.byWs[capabilitiesKey(ws, scope)] : undefined));
}

/** Breakdowns the server allows for a metric. Unknown capabilities: everything except the pool (it needs admin). */
export function allowedDimensions(caps: UsageCapabilities | undefined, metric: StatsState["m"]): UsageDimension[] {
  if (!caps) return DIMENSIONS.filter((d) => d !== "account");
  const list = metric === "cards_done" && caps.cards_done_group_by ? caps.cards_done_group_by : caps.group_by;
  return DIMENSIONS.filter((d) => d === "workspace" || list.includes(d));
}

/** The state the server can answer for this viewer: a view or breakdown they lack falls back, never errors. */
export function sanitize(st: StatsState, caps: UsageCapabilities | undefined): StatsState {
  if (!caps) return st;
  let m = st.m;
  if (m === "quota" && !caps.quota) m = caps.cost ? "cost" : "tokens";
  if (m === "cost" && !caps.cost) m = "tokens";
  const dims = allowedDimensions(caps, m);
  const g = dims.includes(st.g) ? st.g : (dims[0] ?? st.g);
  const s = st.s && dims.includes(st.s) && st.s !== g ? st.s : null;
  const f = { ...st.f };
  if (!caps.group_by.includes("account")) delete f.account;
  if (m === st.m && g === st.g && s === st.s && Object.keys(f).length === Object.keys(st.f).length) return st;
  return { ...st, m, g, s, f };
}

