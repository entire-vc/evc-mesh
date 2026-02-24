import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  CreateProjectRequest,
  CreateStatusRequest,
  PaginatedResponse,
  Project,
  TaskStatus,
} from "@/types";

interface ProjectState {
  projects: Project[];
  currentProject: Project | null;
  statuses: TaskStatus[];
  isLoading: boolean;

  fetchProjects: (workspaceId: string) => Promise<void>;
  setCurrentProject: (project: Project) => void;
  setCurrentProjectBySlug: (slug: string) => boolean;
  createProject: (
    workspaceId: string,
    req: CreateProjectRequest,
  ) => Promise<Project>;
  updateProject: (
    id: string,
    req: Partial<CreateProjectRequest>,
  ) => Promise<void>;
  deleteProject: (id: string) => Promise<void>;

  fetchStatuses: (projectId: string) => Promise<void>;
  createStatus: (
    projectId: string,
    req: CreateStatusRequest,
  ) => Promise<TaskStatus>;
  updateStatus: (
    projectId: string,
    statusId: string,
    req: Partial<CreateStatusRequest>,
  ) => Promise<void>;
  deleteStatus: (projectId: string, statusId: string) => Promise<void>;
  reorderStatuses: (projectId: string, statusIds: string[]) => Promise<void>;
}

export const useProjectStore = create<ProjectState>((set, get) => ({
  projects: [],
  currentProject: null,
  statuses: [],
  isLoading: false,

  fetchProjects: async (workspaceId: string) => {
    set({ isLoading: true });
    try {
      const page = await api<PaginatedResponse<Project>>(
        `/api/v1/workspaces/${workspaceId}/projects`,
      );
      set({ projects: page.items, isLoading: false });
    } catch {
      set({ isLoading: false });
    }
  },

  setCurrentProject: (project: Project) => {
    set({ currentProject: project });
  },

  setCurrentProjectBySlug: (slug: string): boolean => {
    const proj = get().projects.find((p) => p.slug === slug);
    if (proj) {
      set({ currentProject: proj });
      return true;
    }
    return false;
  },

  createProject: async (
    workspaceId: string,
    req: CreateProjectRequest,
  ): Promise<Project> => {
    const project = await api<Project>(
      `/api/v1/workspaces/${workspaceId}/projects`,
      { method: "POST", body: req },
    );
    set((state) => ({ projects: [...state.projects, project] }));
    return project;
  },

  updateProject: async (
    id: string,
    req: Partial<CreateProjectRequest>,
  ) => {
    const updated = await api<Project>(`/api/v1/projects/${id}`, {
      method: "PATCH",
      body: req,
    });
    set((state) => ({
      projects: state.projects.map((p) => (p.id === id ? updated : p)),
      currentProject:
        state.currentProject?.id === id ? updated : state.currentProject,
    }));
  },

  deleteProject: async (id: string) => {
    await api(`/api/v1/projects/${id}`, { method: "DELETE" });
    set((state) => ({
      projects: state.projects.filter((p) => p.id !== id),
      currentProject:
        state.currentProject?.id === id ? null : state.currentProject,
    }));
  },

  fetchStatuses: async (projectId: string) => {
    const statuses = await api<TaskStatus[]>(
      `/api/v1/projects/${projectId}/statuses`,
    );
    set({ statuses });
  },

  createStatus: async (
    projectId: string,
    req: CreateStatusRequest,
  ): Promise<TaskStatus> => {
    const status = await api<TaskStatus>(
      `/api/v1/projects/${projectId}/statuses`,
      { method: "POST", body: req },
    );
    set((state) => ({ statuses: [...state.statuses, status] }));
    return status;
  },

  updateStatus: async (
    projectId: string,
    statusId: string,
    req: Partial<CreateStatusRequest>,
  ) => {
    const updated = await api<TaskStatus>(
      `/api/v1/projects/${projectId}/statuses/${statusId}`,
      { method: "PATCH", body: req },
    );
    set((state) => ({
      statuses: state.statuses.map((s) => (s.id === statusId ? updated : s)),
    }));
  },

  deleteStatus: async (projectId: string, statusId: string) => {
    await api(`/api/v1/projects/${projectId}/statuses/${statusId}`, {
      method: "DELETE",
    });
    set((state) => ({
      statuses: state.statuses.filter((s) => s.id !== statusId),
    }));
  },

  reorderStatuses: async (projectId: string, statusIds: string[]) => {
    const statuses = await api<TaskStatus[]>(
      `/api/v1/projects/${projectId}/statuses/reorder`,
      { method: "PUT", body: { status_ids: statusIds } },
    );
    set({ statuses });
  },
}));
