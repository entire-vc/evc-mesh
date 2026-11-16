import { create } from "zustand";
import {
  applyOrder,
  listDashboards,
  orderOf,
  putDashboardOrder,
  type Dashboard,
  type DashboardOrder,
} from "@/lib/dashboards";
import { apiErrorMessage } from "@/lib/api-error";

interface DashboardsState {
  workspaceId: string | null;
  items: Dashboard[];
  status: "idle" | "loading" | "ready" | "error";
  error: string | null;
  /** Loads the list for a workspace; `silent` keeps the current list on screen (a WS refresh). */
  load: (workspaceId: string, opts?: { silent?: boolean }) => Promise<void>;
  /** Optimistic reorder: the menu moves at once, rolls back (and reloads) when the server refuses. */
  reorder: (order: DashboardOrder) => Promise<void>;
  clear: () => void;
}

// Every write of the list (a load answer, an order answer) takes a ticket; an answer whose ticket is no longer
// the newest is dropped, so a slow older refresh never puts back names, deleted or hidden entries.
let latest = 0;

export const useDashboardsStore = create<DashboardsState>((set, get) => ({
  workspaceId: null,
  items: [],
  status: "idle",
  error: null,

  load: async (workspaceId, opts) => {
    const switched = get().workspaceId !== workspaceId;
    if (switched) set({ workspaceId, items: [], status: "loading", error: null });
    else if (!opts?.silent) set({ status: "loading", error: null });
    const ticket = ++latest;
    try {
      const items = await listDashboards(workspaceId, true);
      if (ticket !== latest || get().workspaceId !== workspaceId) return;
      set({ items, status: "ready", error: null });
    } catch (err) {
      if (ticket !== latest || get().workspaceId !== workspaceId) return;
      // A silent refresh that fails keeps what the user already sees.
      if (opts?.silent && get().status === "ready") return;
      set({ status: "error", error: apiErrorMessage(err, "Failed to load dashboards") });
    }
  },

  reorder: async (order) => {
    const { workspaceId, items } = get();
    if (!workspaceId) return;
    const before = items;
    set({ items: applyOrder(items, order) });
    const ticket = ++latest;
    try {
      const next = await putDashboardOrder(workspaceId, order);
      // The answer is the menu with real ids (a virtual default gets one); the hidden ones are not in it.
      if (ticket === latest && get().workspaceId === workspaceId) set({ items: [...next, ...before.filter((d) => d.hidden)] });
    } catch (err) {
      // Roll back and re-read only while this reorder is still the newest word on the same workspace:
      // after a workspace switch or clear() the old workspace must not come back into the menu.
      if (ticket === latest && get().workspaceId === workspaceId) {
        set({ items: before, error: apiErrorMessage(err, "Failed to save the order") });
        void get().load(workspaceId, { silent: true });
      }
      throw err;
    }
  },

  // Also drops every answer still in flight.
  clear: () => {
    latest++;
    set({ workspaceId: null, items: [], status: "idle", error: null });
  },
}));

export { orderOf };
