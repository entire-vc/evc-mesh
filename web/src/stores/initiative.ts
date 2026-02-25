import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  CreateInitiativeRequest,
  Initiative,
  UpdateInitiativeRequest,
} from "@/types";

interface InitiativeState {
  initiatives: Initiative[];
  currentInitiative: Initiative | null;
  isLoading: boolean;

  fetchInitiatives: (workspaceId: string) => Promise<void>;
  createInitiative: (
    workspaceId: string,
    req: CreateInitiativeRequest,
  ) => Promise<Initiative>;
  getInitiative: (id: string) => Promise<Initiative>;
  updateInitiative: (
    id: string,
    req: UpdateInitiativeRequest,
  ) => Promise<Initiative>;
  deleteInitiative: (id: string) => Promise<void>;
  linkProject: (initiativeId: string, projectId: string) => Promise<void>;
  unlinkProject: (initiativeId: string, projectId: string) => Promise<void>;
}

export const useInitiativeStore = create<InitiativeState>((set, get) => ({
  initiatives: [],
  currentInitiative: null,
  isLoading: false,

  fetchInitiatives: async (workspaceId: string) => {
    set({ isLoading: true });
    try {
      const items = await api<Initiative[]>(
        `/api/v1/workspaces/${workspaceId}/initiatives`,
      );
      set({ initiatives: items ?? [], isLoading: false });
    } catch {
      set({ isLoading: false });
    }
  },

  createInitiative: async (
    workspaceId: string,
    req: CreateInitiativeRequest,
  ): Promise<Initiative> => {
    const initiative = await api<Initiative>(
      `/api/v1/workspaces/${workspaceId}/initiatives`,
      { method: "POST", body: req },
    );
    set((state) => ({ initiatives: [initiative, ...state.initiatives] }));
    return initiative;
  },

  getInitiative: async (id: string): Promise<Initiative> => {
    const initiative = await api<Initiative>(`/api/v1/initiatives/${id}`);
    set({ currentInitiative: initiative });
    return initiative;
  },

  updateInitiative: async (
    id: string,
    req: UpdateInitiativeRequest,
  ): Promise<Initiative> => {
    const updated = await api<Initiative>(`/api/v1/initiatives/${id}`, {
      method: "PATCH",
      body: req,
    });
    set((state) => ({
      initiatives: state.initiatives.map((i) => (i.id === id ? updated : i)),
      currentInitiative:
        state.currentInitiative?.id === id ? updated : state.currentInitiative,
    }));
    return updated;
  },

  deleteInitiative: async (id: string) => {
    await api(`/api/v1/initiatives/${id}`, { method: "DELETE" });
    set((state) => ({
      initiatives: state.initiatives.filter((i) => i.id !== id),
      currentInitiative:
        state.currentInitiative?.id === id ? null : state.currentInitiative,
    }));
  },

  linkProject: async (initiativeId: string, projectId: string) => {
    await api(`/api/v1/initiatives/${initiativeId}/projects`, {
      method: "POST",
      body: { project_id: projectId },
    });
    // Re-fetch the current initiative to update linked projects.
    if (get().currentInitiative?.id === initiativeId) {
      await get().getInitiative(initiativeId);
    }
  },

  unlinkProject: async (initiativeId: string, projectId: string) => {
    await api(
      `/api/v1/initiatives/${initiativeId}/projects/${projectId}`,
      { method: "DELETE" },
    );
    if (get().currentInitiative?.id === initiativeId) {
      await get().getInitiative(initiativeId);
    }
  },
}));
