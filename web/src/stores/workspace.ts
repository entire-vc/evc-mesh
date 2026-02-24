import { create } from "zustand";
import { api } from "@/lib/api";
import type { CreateWorkspaceRequest, Workspace } from "@/types";

interface WorkspaceState {
  workspaces: Workspace[];
  currentWorkspace: Workspace | null;
  isLoading: boolean;

  fetchWorkspaces: () => Promise<void>;
  setCurrentWorkspace: (workspace: Workspace) => void;
  setCurrentWorkspaceBySlug: (slug: string) => boolean;
  createWorkspace: (req: CreateWorkspaceRequest) => Promise<Workspace>;
  updateWorkspace: (
    id: string,
    req: Partial<CreateWorkspaceRequest>,
  ) => Promise<void>;
  deleteWorkspace: (id: string) => Promise<void>;
}

export const useWorkspaceStore = create<WorkspaceState>((set, get) => ({
  workspaces: [],
  currentWorkspace: null,
  isLoading: false,

  fetchWorkspaces: async () => {
    set({ isLoading: true });
    try {
      const workspaces = await api<Workspace[]>("/api/v1/workspaces");
      set({ workspaces, isLoading: false });
    } catch {
      set({ isLoading: false });
    }
  },

  setCurrentWorkspace: (workspace: Workspace) => {
    set({ currentWorkspace: workspace });
  },

  setCurrentWorkspaceBySlug: (slug: string): boolean => {
    const ws = get().workspaces.find((w) => w.slug === slug);
    if (ws) {
      set({ currentWorkspace: ws });
      return true;
    }
    return false;
  },

  createWorkspace: async (
    req: CreateWorkspaceRequest,
  ): Promise<Workspace> => {
    const workspace = await api<Workspace>("/api/v1/workspaces", {
      method: "POST",
      body: req,
    });
    set((state) => ({
      workspaces: [...state.workspaces, workspace],
    }));
    return workspace;
  },

  updateWorkspace: async (
    id: string,
    req: Partial<CreateWorkspaceRequest>,
  ) => {
    const updated = await api<Workspace>(`/api/v1/workspaces/${id}`, {
      method: "PATCH",
      body: req,
    });
    set((state) => ({
      workspaces: state.workspaces.map((w) => (w.id === id ? updated : w)),
      currentWorkspace:
        state.currentWorkspace?.id === id ? updated : state.currentWorkspace,
    }));
  },

  deleteWorkspace: async (id: string) => {
    await api(`/api/v1/workspaces/${id}`, { method: "DELETE" });
    set((state) => ({
      workspaces: state.workspaces.filter((w) => w.id !== id),
      currentWorkspace:
        state.currentWorkspace?.id === id ? null : state.currentWorkspace,
    }));
  },
}));
