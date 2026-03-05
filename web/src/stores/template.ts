import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  CreateTemplateRequest,
  Task,
  TaskTemplate,
  UpdateTemplateRequest,
} from "@/types";

interface TemplateState {
  templates: TaskTemplate[];
  isLoading: boolean;

  fetchTemplates: (projectId: string) => Promise<void>;
  createTemplate: (
    projectId: string,
    req: CreateTemplateRequest,
  ) => Promise<TaskTemplate>;
  updateTemplate: (
    id: string,
    req: UpdateTemplateRequest,
  ) => Promise<TaskTemplate>;
  deleteTemplate: (id: string) => Promise<void>;
  createTaskFromTemplate: (
    templateId: string,
    overrides?: Record<string, unknown>,
  ) => Promise<Task>;
}

export const useTemplateStore = create<TemplateState>((set) => ({
  templates: [],
  isLoading: false,

  fetchTemplates: async (projectId: string) => {
    set({ isLoading: true });
    try {
      const data = await api<{ items: TaskTemplate[]; total: number }>(
        `/api/v1/projects/${projectId}/templates`,
      );
      set({ templates: data.items ?? [], isLoading: false });
    } catch {
      set({ isLoading: false });
    }
  },

  createTemplate: async (
    projectId: string,
    req: CreateTemplateRequest,
  ): Promise<TaskTemplate> => {
    const tmpl = await api<TaskTemplate>(
      `/api/v1/projects/${projectId}/templates`,
      { method: "POST", body: req },
    );
    set((state) => ({ templates: [...state.templates, tmpl] }));
    return tmpl;
  },

  updateTemplate: async (
    id: string,
    req: UpdateTemplateRequest,
  ): Promise<TaskTemplate> => {
    const updated = await api<TaskTemplate>(`/api/v1/templates/${id}`, {
      method: "PATCH",
      body: req,
    });
    set((state) => ({
      templates: state.templates.map((t) => (t.id === id ? updated : t)),
    }));
    return updated;
  },

  deleteTemplate: async (id: string) => {
    await api<void>(`/api/v1/templates/${id}`, { method: "DELETE" });
    set((state) => ({
      templates: state.templates.filter((t) => t.id !== id),
    }));
  },

  createTaskFromTemplate: async (
    templateId: string,
    overrides?: Record<string, unknown>,
  ): Promise<Task> => {
    const task = await api<Task>(
      `/api/v1/templates/${templateId}/create-task`,
      { method: "POST", body: { overrides: overrides ?? {} } },
    );
    return task;
  },
}));
