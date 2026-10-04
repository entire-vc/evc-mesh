import { create } from "zustand";
import { api } from "@/lib/api";
import { fetchLinkedEntities } from "@/lib/entity-deep-links";
import type {
  CreateRecurringRequest,
  PaginatedResponse,
  RecurringInstanceSummary,
  RecurringSchedule,
  Task,
  TriggerRecurringResponse,
  UpdateRecurringRequest,
} from "@/types";

interface PaginationParams {
  page?: number;
  page_size?: number;
  sort_dir?: "asc" | "desc";
}

interface RecurringState {
  schedules: RecurringSchedule[];
  history: RecurringInstanceSummary[];
  isLoading: boolean;
  schedulesLoading: boolean;
  schedulesError: boolean;
  schedulesProjectId: string | null;

  fetchSchedules: (projectId: string, focusId?: string | null) => Promise<void>;
  createSchedule: (
    projectId: string,
    req: CreateRecurringRequest,
  ) => Promise<RecurringSchedule>;
  updateSchedule: (
    id: string,
    req: UpdateRecurringRequest,
  ) => Promise<RecurringSchedule>;
  deleteSchedule: (id: string) => Promise<void>;
  triggerNow: (id: string) => Promise<Task>;
  fetchHistory: (
    scheduleId: string,
    params?: PaginationParams,
  ) => Promise<void>;
}

let schedulesRequest = 0;
export const useRecurringStore = create<RecurringState>((set, get) => ({
  schedules: [],
  history: [],
  isLoading: false,
  schedulesLoading: false,
  schedulesError: false,
  schedulesProjectId: null,

  fetchSchedules: async (projectId: string, focusId?: string | null) => {
    const request = ++schedulesRequest;
    set({ isLoading: true, schedulesLoading: true, schedulesError: false, schedulesProjectId: null, schedules: [] });
    try {
      const schedules = await fetchLinkedEntities<RecurringSchedule>(
        `/api/v1/projects/${projectId}/recurring`,
        focusId,
      );
      if (request !== schedulesRequest) return;
      set({ schedules, isLoading: false, schedulesLoading: false, schedulesProjectId: projectId });
    } catch {
      if (request !== schedulesRequest) return;
      set({ isLoading: false, schedulesLoading: false, schedulesError: true, schedulesProjectId: projectId });
    }
  },

  createSchedule: async (
    projectId: string,
    req: CreateRecurringRequest,
  ): Promise<RecurringSchedule> => {
    const schedule = await api<RecurringSchedule>(
      `/api/v1/projects/${projectId}/recurring`,
      { method: "POST", body: req },
    );
    set((state) => ({ schedules: [...state.schedules, schedule] }));
    return schedule;
  },

  updateSchedule: async (
    id: string,
    req: UpdateRecurringRequest,
  ): Promise<RecurringSchedule> => {
    const updated = await api<RecurringSchedule>(`/api/v1/recurring/${id}`, {
      method: "PATCH",
      body: req,
    });
    set((state) => ({
      schedules: state.schedules.map((s) => (s.id === id ? updated : s)),
    }));
    return updated;
  },

  deleteSchedule: async (id: string) => {
    await api(`/api/v1/recurring/${id}`, { method: "DELETE" });
    set((state) => ({
      schedules: state.schedules.filter((s) => s.id !== id),
    }));
  },

  triggerNow: async (id: string): Promise<Task> => {
    const response = await api<TriggerRecurringResponse>(
      `/api/v1/recurring/${id}/trigger`,
      { method: "POST" },
    );
    // Refresh schedule list to update instance_count
    const current = get().schedules;
    const updated = current.map((s) =>
      s.id === id
        ? { ...s, instance_count: s.instance_count + 1 }
        : s,
    );
    set({ schedules: updated });
    return response.task;
  },

  fetchHistory: async (
    scheduleId: string,
    params?: PaginationParams,
  ) => {
    set({ isLoading: true });
    try {
      const data = await api<PaginatedResponse<RecurringInstanceSummary>>(
        `/api/v1/recurring/${scheduleId}/history`,
        {
          params: {
            page: params?.page,
            page_size: params?.page_size ?? 50,
            sort_dir: params?.sort_dir ?? "desc",
          },
        },
      );
      set({ history: data.items ?? [], isLoading: false });
    } catch {
      set({ isLoading: false });
    }
  },
}));
