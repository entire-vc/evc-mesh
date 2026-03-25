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

function getErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

interface TaskState {
  tasks: Task[];
  tasksById: Record<string, Task>;
  tasksByStatus: Record<string, Task[]>;
  isLoading: boolean;
  error: string | null;
  total: number;
  page: number;
  perPage: number;
  hasMore: boolean;

  fetchTasks: (
    projectId: string,
    params?: Record<string, string | number | undefined>,
  ) => Promise<void>;
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
  tasksById: {},
  tasksByStatus: {},
  isLoading: false,
  error: null,
  total: 0,
  page: 1,
  perPage: 50,
  hasMore: false,

  fetchTasks: async (
    projectId: string,
    params?: Record<string, string | number | undefined>,
  ) => {
    set({ isLoading: true, error: null });
    try {
      const data = await api<PaginatedResponse<Task>>(
        `/api/v1/projects/${projectId}/tasks`,
        { params: { page_size: "200", ...params } },
      );
      const items = data.items ?? [];
      set({
        tasks: items,
        tasksById: {
          ...get().tasksById,
          ...Object.fromEntries(items.map((task) => [task.id, task])),
        },
        total: data.total_count ?? data.total ?? 0,
        page: data.page,
        perPage: data.per_page ?? data.page_size ?? 50,
        hasMore: data.has_more,
        isLoading: false,
        error: null,
      });
      get().groupByStatus();
    } catch (error) {
      set({
        tasks: [],
        tasksByStatus: {},
        isLoading: false,
        error: getErrorMessage(error),
        total: 0,
        page: 1,
        perPage: 50,
        hasMore: false,
      });
    }
  },

  fetchTask: async (taskId: string): Promise<Task> => {
    set({ error: null });
    try {
      const task = await api<Task>(`/api/v1/tasks/${taskId}`);
      set((state) => ({
        tasksById: {
          ...state.tasksById,
          [task.id]: task,
        },
        error: null,
      }));
      return task;
    } catch (error) {
      set((state) => ({
        tasksById: Object.fromEntries(
          Object.entries(state.tasksById).filter(([id]) => id !== taskId),
        ),
        error: getErrorMessage(error),
      }));
      throw error;
    }
  },

  createTask: async (
    projectId: string,
    req: CreateTaskRequest,
  ): Promise<Task> => {
    const task = await api<Task>(`/api/v1/projects/${projectId}/tasks`, {
      method: "POST",
      body: req,
    });
    set((state) => ({
      tasks: [...state.tasks, task],
      tasksById: {
        ...state.tasksById,
        [task.id]: task,
      },
    }));
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
      tasksById: {
        ...state.tasksById,
        [taskId]: updated,
      },
    }));
    get().groupByStatus();
    return updated;
  },

  deleteTask: async (taskId: string) => {
    await api(`/api/v1/tasks/${taskId}`, { method: "DELETE" });
    set((state) => ({
      tasks: state.tasks.filter((t) => t.id !== taskId),
      tasksById: Object.fromEntries(
        Object.entries(state.tasksById).filter(([id]) => id !== taskId),
      ),
    }));
    get().groupByStatus();
  },

  duplicateTask: async (task: Task): Promise<Task> => {
    const req = buildDuplicateRequest(task);
    const newTask = await api<Task>(
      `/api/v1/projects/${task.project_id}/tasks`,
      { method: "POST", body: req },
    );
    set((state) => ({
      tasks: [...state.tasks, newTask],
      tasksById: {
        ...state.tasksById,
        [newTask.id]: newTask,
      },
    }));
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
      tasksById: {
        ...state.tasksById,
        [taskId]: updated,
      },
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
        tasksById: state.tasksById[taskId]
          ? {
              ...state.tasksById,
              [taskId]: {
                ...state.tasksById[taskId],
                status_id: req.status_id!,
                position: req.position ?? state.tasksById[taskId]!.position,
              },
            }
          : state.tasksById,
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
