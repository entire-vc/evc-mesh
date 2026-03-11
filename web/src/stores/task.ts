import { create } from "zustand";
import { api } from "@/lib/api";
import { buildDuplicateRequest } from "@/lib/utils";
import type {
  CreateTaskRequest,
  MoveTaskRequest,
  PaginatedResponse,
  Task,
  UpdateTaskRequest,
} from "@/types";

interface TaskState {
  tasks: Task[];
  tasksByStatus: Record<string, Task[]>;
  currentTask: Task | null;
  isLoading: boolean;
  total: number;
  page: number;
  perPage: number;
  hasMore: boolean;

  fetchTasks: (
    projectId: string,
    params?: Record<string, string | number | undefined>,
  ) => Promise<void>;
  setCurrentTask: (task: Task | null) => void;
  fetchTask: (taskId: string) => Promise<Task>;
  createTask: (projectId: string, req: CreateTaskRequest) => Promise<Task>;
  updateTask: (taskId: string, req: UpdateTaskRequest) => Promise<Task>;
  deleteTask: (taskId: string) => Promise<void>;
  moveTask: (taskId: string, req: MoveTaskRequest) => Promise<void>;
  moveToProject: (taskId: string, projectId: string) => Promise<Task>;
  duplicateTask: (task: Task) => Promise<Task>;

  groupByStatus: () => void;
}

export const useTaskStore = create<TaskState>((set, get) => ({
  tasks: [],
  tasksByStatus: {},
  currentTask: null,
  isLoading: false,
  total: 0,
  page: 1,
  perPage: 50,
  hasMore: false,

  fetchTasks: async (
    projectId: string,
    params?: Record<string, string | number | undefined>,
  ) => {
    set({ isLoading: true });
    try {
      const data = await api<PaginatedResponse<Task>>(
        `/api/v1/projects/${projectId}/tasks`,
        { params: { page_size: "200", ...params } },
      );
      set({
        tasks: data.items ?? [],
        total: data.total_count ?? data.total ?? 0,
        page: data.page,
        perPage: data.per_page ?? data.page_size ?? 50,
        hasMore: data.has_more,
        isLoading: false,
      });
      get().groupByStatus();
    } catch {
      set({ isLoading: false });
    }
  },

  setCurrentTask: (task: Task | null) => {
    set({ currentTask: task });
  },

  fetchTask: async (taskId: string): Promise<Task> => {
    const task = await api<Task>(`/api/v1/tasks/${taskId}`);
    set({ currentTask: task });
    return task;
  },

  createTask: async (
    projectId: string,
    req: CreateTaskRequest,
  ): Promise<Task> => {
    const task = await api<Task>(`/api/v1/projects/${projectId}/tasks`, {
      method: "POST",
      body: req,
    });
    set((state) => ({ tasks: [...state.tasks, task] }));
    get().groupByStatus();
    return task;
  },

  updateTask: async (
    taskId: string,
    req: UpdateTaskRequest,
  ): Promise<Task> => {
    const updated = await api<Task>(`/api/v1/tasks/${taskId}`, {
      method: "PATCH",
      body: req,
    });
    set((state) => ({
      tasks: state.tasks.map((t) => (t.id === taskId ? updated : t)),
      currentTask:
        state.currentTask?.id === taskId ? updated : state.currentTask,
    }));
    get().groupByStatus();
    return updated;
  },

  deleteTask: async (taskId: string) => {
    await api(`/api/v1/tasks/${taskId}`, { method: "DELETE" });
    set((state) => ({
      tasks: state.tasks.filter((t) => t.id !== taskId),
      currentTask:
        state.currentTask?.id === taskId ? null : state.currentTask,
    }));
    get().groupByStatus();
  },

  duplicateTask: async (task: Task): Promise<Task> => {
    const req = buildDuplicateRequest(task);
    const newTask = await api<Task>(
      `/api/v1/projects/${task.project_id}/tasks`,
      { method: "POST", body: req },
    );
    set((state) => ({ tasks: [...state.tasks, newTask] }));
    get().groupByStatus();
    return newTask;
  },

  moveToProject: async (taskId: string, projectId: string): Promise<Task> => {
    const updated = await api<Task>(`/api/v1/tasks/${taskId}/move-to-project`, {
      method: "POST",
      body: { project_id: projectId },
    });
    // Remove task from current project's local list (it moved to another project)
    set((state) => ({
      tasks: state.tasks.filter((t) => t.id !== taskId),
      currentTask: state.currentTask?.id === taskId ? updated : state.currentTask,
    }));
    get().groupByStatus();
    return updated;
  },

  moveTask: async (taskId: string, req: MoveTaskRequest) => {
    await api(`/api/v1/tasks/${taskId}/move`, {
      method: "POST",
      body: req,
    });
    // Optimistic: update local state
    if (req.status_id) {
      set((state) => ({
        tasks: state.tasks.map((t) =>
          t.id === taskId
            ? {
                ...t,
                status_id: req.status_id!,
                position: req.position ?? t.position,
              }
            : t,
        ),
      }));
      get().groupByStatus();
    }
  },

  groupByStatus: () => {
    const { tasks } = get();
    const grouped: Record<string, Task[]> = {};
    for (const task of tasks) {
      if (!grouped[task.status_id]) {
        grouped[task.status_id] = [];
      }
      grouped[task.status_id]!.push(task);
    }
    // Sort tasks within each status by position
    for (const statusId of Object.keys(grouped)) {
      grouped[statusId]!.sort((a, b) => a.position - b.position);
    }
    set({ tasksByStatus: grouped });
  },
}));
