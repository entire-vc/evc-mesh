import { create } from "zustand";
import { api } from "@/lib/api";
import { buildDuplicateRequest } from "@/lib/utils";
import { apiErrorMessage } from "@/lib/api-error";
import type {
  CreateTaskRequest,
  MoveTaskRequest,
  PaginatedResponse,
  Task,
  UpdateTaskRequest,
} from "@/types";

function getErrorMessage(error: unknown): string {
  return apiErrorMessage(error, "Request failed");
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

// Monotonic id of the newest fetchTasks call. A response that is no longer the
// newest belongs to a project/filter the user already left (Lab board -> Keep
// board) and must not overwrite the list — it used to, when the Lab request
// was the slower one.
let latestFetchTasks = 0;

// Local status moves the server may not reflect yet. moveTask applies a move
// to the store BEFORE its POST returns (a dropped card stays where it was
// dropped instead of snapping back for the whole round-trip), so a task list
// fetched before the move was confirmed must not put the card back: every
// fetchTasks answer is overlaid with the moves its request could not have
// seen. `clock` orders fetch starts against move confirmations.
//
// One record per task, because a card can be dropped again before the first
// answer lands. What the card shows is always derived the same way: the
// newest move still in flight, else the newest move the server accepted,
// else where the card was before the first of them. A refusal therefore
// falls back to that, never to another move's optimistic destination.
let clock = 0;
type Placement = { status_id: string; position?: number };
interface LocalMoves {
  /** Where the card was before the first move of this record. */
  base: Placement;
  /** Moves in flight, by issue order. */
  pending: Map<number, Placement>;
  /** Newest accepted move (by issue order) and the clock when it was accepted. */
  accepted: { seq: number; at: number; to: Placement } | null;
}
let moveSeq = 0;
const localMoves = new Map<string, LocalMoves>();

function shownPlacement(rec: LocalMoves): Placement {
  if (rec.pending.size > 0) return rec.pending.get(Math.max(...rec.pending.keys()))!;
  return rec.accepted?.to ?? rec.base;
}

function overlayLocalMoves(items: Task[], fetchStartedAt: number): Task[] {
  for (const [id, rec] of localMoves) {
    // Nothing in flight and accepted before this fetch started: the answer
    // already carries it.
    if (rec.pending.size === 0 && (rec.accepted === null || rec.accepted.at < fetchStartedAt))
      localMoves.delete(id);
  }
  if (localMoves.size === 0) return items;
  return items.map((t) => {
    const rec = localMoves.get(t.id);
    if (!rec) return t;
    const to = shownPlacement(rec);
    return { ...t, status_id: to.status_id, position: to.position ?? t.position };
  });
}

/** Test hook: forget local moves between unit tests. */
export function __resetLocalMovesForTest(): void {
  localMoves.clear();
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
    const requestId = ++latestFetchTasks;
    const startedAt = ++clock;
    set({ isLoading: true, error: null });
    try {
      const data = await api<PaginatedResponse<Task>>(
        `/api/v1/projects/${projectId}/tasks`,
        // include_description=false: descriptions were 703 KB of the 917 KB this
        // call returned for a 200-task board, and nothing on a card renders them.
        // Callers that need the text fetch the task itself. An older API that does
        // not know the param ignores it and returns the full payload, so a
        // frontend deploy that lands before the backend one is merely slow.
        { params: { page_size: "200", include_description: "false", ...params } },
      );
      if (requestId !== latestFetchTasks) return;
      const items = overlayLocalMoves(data.items ?? [], startedAt);
      const prevById = get().tasksById;
      set({
        tasks: items,
        tasksById: {
          ...prevById,
          ...Object.fromEntries(
            items.map((task) => {
              // Merge rather than overwrite: these items carry no description, and
              // the slide-over renders currentTask.description from this cache
              // while its own fetch is still in flight. A blind overwrite blanks
              // the text of an already-opened task for that window.
              const known = prevById[task.id]?.description;
              return [
                task.id,
                task.description === undefined && known !== undefined
                  ? { ...task, description: known }
                  : task,
              ] as const;
            }),
          ),
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
      if (requestId !== latestFetchTasks) return;
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
    // A task object from a board/list fetch carries no description — the board
    // asks for the list without one. Copying straight from it would produce a
    // duplicate whose description is silently empty, and nothing would report
    // it. Today every caller happens to hold a task the slide-over already
    // fetched in full; re-reading here means that stays true for callers that
    // do not exist yet. `undefined` means "not sent"; an empty string is a real
    // value and needs no round-trip.
    const source = task.description === undefined ? await get().fetchTask(task.id) : task;
    const req = buildDuplicateRequest(source);
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
    // Optimistic: the local state changes first and is rolled back if the
    // server refuses. Waiting for the POST first left a dragged card in its
    // source column for the whole round-trip — on prod's slow tail long
    // enough for authed-e2e's board-drag-filter to see "still in the source
    // column" after 5s (jobs 155165, 160044, 177207), and on a loaded CI
    // runner long enough to flip perf-counters' board.drag into a second
    // render (448 DOM mutations / 16 layouts / ~100 style recalcs instead of
    // 447/13/~77 — 3 false reds 04–10.10, web/perf/README.md).
    const setStatus = (status_id: string, position: number | undefined) => {
      set((state) => ({
        tasks: state.tasks.map((t) =>
          t.id === taskId ? { ...t, status_id, position: position ?? t.position } : t,
        ),
        tasksById: state.tasksById[taskId]
          ? {
              ...state.tasksById,
              [taskId]: {
                ...state.tasksById[taskId],
                status_id,
                position: position ?? state.tasksById[taskId]!.position,
              },
            }
          : state.tasksById,
      }));
      get().groupByStatus();
    };

    let seq = 0;
    if (req.status_id) {
      let rec = localMoves.get(taskId);
      if (!rec) {
        const known = get().tasksById[taskId] ?? get().tasks.find((t) => t.id === taskId);
        rec = {
          base: known
            ? { status_id: known.status_id, position: known.position }
            : { status_id: req.status_id, position: req.position },
          pending: new Map(),
          accepted: null,
        };
        localMoves.set(taskId, rec);
      }
      seq = ++moveSeq;
      rec.pending.set(seq, { status_id: req.status_id, position: req.position });
      setStatus(req.status_id, req.position);
    }

    try {
      await api(`/api/v1/tasks/${taskId}/move`, {
        method: "POST",
        body: req,
      });
    } catch (error) {
      const rec = seq ? localMoves.get(taskId) : undefined;
      if (rec?.pending.delete(seq)) {
        const to = shownPlacement(rec);
        if (rec.pending.size === 0 && rec.accepted === null) localMoves.delete(taskId);
        setStatus(to.status_id, to.position);
      }
      throw error;
    }
    const rec = seq ? localMoves.get(taskId) : undefined;
    const to = rec?.pending.get(seq);
    if (rec && to) {
      rec.pending.delete(seq);
      if (rec.accepted === null || seq > rec.accepted.seq)
        rec.accepted = { seq, at: ++clock, to };
      // No store write here: an acceptance never changes what the card shows
      // (the newest move in flight, or this one), and a write would cost the
      // board a re-render on every drop.
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
