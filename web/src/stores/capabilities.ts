import { create } from "zustand";
import { api } from "@/lib/api";

// Instance-wide feature flags the frontend needs before a workspace is even
// selected (nav visibility). Sourced from /api/version's spark_enabled field
// rather than a dedicated endpoint — see cmd/api/main.go's versionHandler.
interface CapabilitiesState {
  sparkEnabled: boolean;
  fetched: boolean;
  fetch: () => Promise<void>;
}

export const useCapabilitiesStore = create<CapabilitiesState>((set, get) => ({
  // Defaults to true (shown) until the fetch resolves or fails: the server
  // route registration is the real gate (disabled Spark routes 404), so
  // fail-open here only means a dead nav link for the ~1 request it takes to
  // learn otherwise — the same fail-open choice web/src/pages/spark.tsx
  // already makes for the same reason.
  sparkEnabled: true,
  fetched: false,

  fetch: async () => {
    if (get().fetched) return;
    try {
      const res = await api<{ spark_enabled?: boolean }>("/api/version");
      set({ sparkEnabled: res.spark_enabled ?? true, fetched: true });
    } catch {
      set({ fetched: true });
    }
  },
}));
