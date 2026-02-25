import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  CreateProjectUpdateRequest,
  PaginatedResponse,
  ProjectUpdate,
} from "@/types";

interface ProjectUpdateState {
  updates: ProjectUpdate[];
  latestUpdate: ProjectUpdate | null;
  isLoading: boolean;
  total: number;
  page: number;

  fetchUpdates: (projectId: string, page?: number) => Promise<void>;
  fetchLatest: (projectId: string) => Promise<ProjectUpdate | null>;
  createUpdate: (
    projectId: string,
    req: CreateProjectUpdateRequest,
  ) => Promise<ProjectUpdate>;
}

export const useProjectUpdateStore = create<ProjectUpdateState>((set) => ({
  updates: [],
  latestUpdate: null,
  isLoading: false,
  total: 0,
  page: 1,

  fetchUpdates: async (projectId: string, page = 1) => {
    set({ isLoading: true });
    try {
      const result = await api<PaginatedResponse<ProjectUpdate>>(
        `/api/v1/projects/${projectId}/updates`,
        { params: { page, per_page: 20 } },
      );
      set({
        updates: result.items ?? [],
        total: result.total_count ?? result.total ?? 0,
        page,
        isLoading: false,
      });
    } catch {
      set({ isLoading: false });
    }
  },

  fetchLatest: async (projectId: string): Promise<ProjectUpdate | null> => {
    try {
      const update = await api<ProjectUpdate>(
        `/api/v1/projects/${projectId}/updates/latest`,
      );
      set({ latestUpdate: update });
      return update;
    } catch {
      set({ latestUpdate: null });
      return null;
    }
  },

  createUpdate: async (
    projectId: string,
    req: CreateProjectUpdateRequest,
  ): Promise<ProjectUpdate> => {
    const update = await api<ProjectUpdate>(
      `/api/v1/projects/${projectId}/updates`,
      { method: "POST", body: req },
    );
    set((state) => ({
      updates: [update, ...state.updates],
      latestUpdate: update,
    }));
    return update;
  },
}));
