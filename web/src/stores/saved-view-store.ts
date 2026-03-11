import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  SavedView,
  CreateSavedViewRequest,
  UpdateSavedViewRequest,
} from "@/types";

interface CurrentViewState {
  filters?: Record<string, unknown>;
  sortBy?: string;
  sortOrder?: string;
  columns?: string[];
}

interface SavedViewState {
  views: SavedView[];
  isLoading: boolean;

  /** Set by ViewTabBar when user clicks a saved view; consumed by pages */
  pendingView: SavedView | null;
  /** Pages call this after they apply the pending view's filters */
  clearPendingView: () => void;
  /** ViewTabBar calls this to signal a view was selected */
  applyView: (view: SavedView) => void;

  /** Pages write their current filter/sort state here so ViewTabBar can save it */
  currentViewState: CurrentViewState;
  setCurrentViewState: (state: CurrentViewState) => void;

  fetchViews: (projectId: string) => Promise<void>;
  createView: (
    projectId: string,
    req: CreateSavedViewRequest,
  ) => Promise<SavedView>;
  updateView: (
    viewId: string,
    req: UpdateSavedViewRequest,
  ) => Promise<SavedView>;
  deleteView: (viewId: string) => Promise<void>;
}

export const useSavedViewStore = create<SavedViewState>((set) => ({
  views: [],
  isLoading: false,
  pendingView: null,
  clearPendingView: () => set({ pendingView: null }),
  applyView: (view: SavedView) => set({ pendingView: view }),
  currentViewState: {},
  setCurrentViewState: (state) => set({ currentViewState: state }),

  fetchViews: async (projectId: string) => {
    set({ isLoading: true });
    try {
      const data = await api<{ views: SavedView[]; count: number }>(
        `/api/v1/projects/${projectId}/views`,
      );
      set({ views: data.views ?? [], isLoading: false });
    } catch {
      set({ isLoading: false });
    }
  },

  createView: async (
    projectId: string,
    req: CreateSavedViewRequest,
  ): Promise<SavedView> => {
    const view = await api<SavedView>(
      `/api/v1/projects/${projectId}/views`,
      { method: "POST", body: req },
    );
    set((state) => ({ views: [...state.views, view] }));
    return view;
  },

  updateView: async (
    viewId: string,
    req: UpdateSavedViewRequest,
  ): Promise<SavedView> => {
    const updated = await api<SavedView>(`/api/v1/views/${viewId}`, {
      method: "PATCH",
      body: req,
    });
    set((state) => ({
      views: state.views.map((v) => (v.id === viewId ? updated : v)),
    }));
    return updated;
  },

  deleteView: async (viewId: string): Promise<void> => {
    await api(`/api/v1/views/${viewId}`, { method: "DELETE" });
    set((state) => ({
      views: state.views.filter((v) => v.id !== viewId),
    }));
  },
}));
