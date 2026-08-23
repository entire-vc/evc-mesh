import { create } from "zustand";
import { api } from "@/lib/api";
import { getAccessToken } from "@/lib/api";
import type { CreateWorkspaceRequest, Workspace } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";

const BASE_URL = import.meta.env.VITE_API_URL || "";

function getErrorMessage(error: unknown): string {
  return apiErrorMessage(error, "Failed to load workspaces");
}

interface WorkspaceState {
  workspaces: Workspace[];
  currentWorkspace: Workspace | null;
  isLoading: boolean;
  error: string | null;

  fetchWorkspaces: () => Promise<void>;
  setCurrentWorkspace: (workspace: Workspace) => void;
  setCurrentWorkspaceBySlug: (slug: string) => boolean;
  createWorkspace: (req: CreateWorkspaceRequest) => Promise<Workspace>;
  updateWorkspace: (
    id: string,
    req: Partial<CreateWorkspaceRequest>,
  ) => Promise<void>;
  deleteWorkspace: (id: string) => Promise<void>;
  uploadIcon: (id: string, file: File) => Promise<void>;
}

export const useWorkspaceStore = create<WorkspaceState>((set, get) => ({
  workspaces: [],
  currentWorkspace: null,
  isLoading: false,
  error: null,

  fetchWorkspaces: async () => {
    set({ isLoading: true, error: null });
    try {
      const workspaces = await api<Workspace[]>("/api/v1/workspaces");
      set({ workspaces, isLoading: false, error: null });
    } catch (error) {
      set({
        workspaces: [],
        currentWorkspace: null,
        isLoading: false,
        error: getErrorMessage(error),
      });
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

  uploadIcon: async (id: string, file: File) => {
    const form = new FormData();
    form.append("file", file, file.name);

    const token = getAccessToken();
    const headers: HeadersInit = {};
    if (token) headers["Authorization"] = `Bearer ${token}`;

    const res = await fetch(`${BASE_URL}/api/v1/workspaces/${id}/icon`, {
      method: "PUT",
      headers,
      body: form,
    });

    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error((err as { message?: string }).message ?? "Upload failed");
    }

    const updated = (await res.json()) as Workspace;
    set((state) => ({
      workspaces: state.workspaces.map((w) => (w.id === id ? updated : w)),
      currentWorkspace:
        state.currentWorkspace?.id === id ? updated : state.currentWorkspace,
    }));
  },
}));
