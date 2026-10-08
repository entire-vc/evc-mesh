import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { Task, TaskDependency, TaskDependencyList, TaskStatus } from "@/types";

export type DependencyGroup = "blocked_by" | "blocks" | "relates_to" | "child_of" | "parent_of";
export interface DependencyRow {
  dep: TaskDependency;
  group: DependencyGroup;
  ownerTaskId: string;
  relatedTaskId: string;
  task?: Task;
  status?: TaskStatus;
  metadataError?: boolean;
}
interface DependencyState {
  taskId: string | null;
  phase: "loading" | "ready" | "error";
  rows: DependencyRow[];
  checking: boolean;
}

function rowsFor(data: TaskDependencyList): DependencyRow[] {
  return [
    ...(data.outgoing ?? []).map((dep): DependencyRow => ({
      dep,
      ownerTaskId: dep.task_id,
      relatedTaskId: dep.depends_on_task_id,
      group: dep.dependency_type === "blocks" ? "blocked_by" :
        dep.dependency_type === "is_child_of" ? "child_of" : "relates_to",
    })),
    ...(data.incoming ?? []).map((dep): DependencyRow => ({
      dep,
      ownerTaskId: dep.task_id,
      relatedTaskId: dep.task_id,
      group: dep.dependency_type === "blocks" ? "blocks" :
        dep.dependency_type === "is_child_of" ? "parent_of" : "relates_to",
    })),
  ].filter((row, index, rows) => rows.findIndex(other => other.dep.id === row.dep.id) === index);
}

// Each open full/slide panel has its own source. Notify every source for BOTH
// endpoints; inactive endpoints are also refetched without keeping a cache.
const listeners = new Map<string, Set<() => Promise<void>>>();
export async function refreshTaskDependencies(...taskIds: string[]) {
  await Promise.allSettled([...new Set(taskIds)].flatMap(id => {
    const sources = listeners.get(id);
    return sources?.size ? [...sources].map(reload => reload()) : [api<TaskDependencyList>(`/api/v1/tasks/${id}/dependencies`).then(() => undefined)];
  }));
}

// Task-scoped cache: share promises within a load, bound concurrency to four,
// and refresh on retry/mutation so terminal status changes cannot stay cached.
export function useTaskDependencies(taskId: string | null) {
  const latestTask = useRef(taskId);
  latestTask.current = taskId;
  const generation = useRef(0);
  const [state, setState] = useState<DependencyState>({ taskId, phase: "loading", rows: [], checking: false });
  const [revision, setRevision] = useState(0);

  const reload = useCallback(async () => {
    if (!taskId || latestTask.current !== taskId) return;
    const request = ++generation.current;
    const current = () => generation.current === request && latestTask.current === taskId;
    setState(prev => ({ taskId, phase: "loading", rows: prev.taskId === taskId ? prev.rows : [], checking: false }));
    try {
      const data = await api<TaskDependencyList>(`/api/v1/tasks/${taskId}/dependencies`);
      if (!current()) return;
      if (!data || !Array.isArray(data.outgoing) || !Array.isArray(data.incoming)) {
        throw new Error("Invalid dependencies response");
      }
      const rows = rowsFor(data);
      setState({ taskId, phase: "ready", rows: rows.map(row => ({ ...row })), checking: rows.length > 0 });
      const tasks = new Map<string, Promise<Task>>();
      const projects = new Map<string, Promise<TaskStatus[]>>();
      let next = 0;
      await Promise.all(Array.from({ length: Math.min(4, rows.length) }, async () => {
        while (next < rows.length && current()) {
          const row = rows[next++]!;
          try {
            if (!tasks.has(row.relatedTaskId)) {
              tasks.set(row.relatedTaskId, api<Task>(`/api/v1/tasks/${row.relatedTaskId}`));
            }
            row.task = await tasks.get(row.relatedTaskId)!;
            if (!current()) return;
            if (!row.task?.project_id) throw new Error("Missing related task");
            const projectId = row.task.project_id;
            if (!projects.has(projectId)) {
              projects.set(projectId, api<TaskStatus[]>(`/api/v1/projects/${projectId}/statuses`));
            }
            const statuses = await projects.get(projectId)!;
            row.status = statuses.find(status => status.id === row.task!.status_id);
            row.metadataError = !row.status;
          } catch {
            row.metadataError = true;
          }
        }
      }));
      if (current()) setState({ taskId, phase: "ready", rows: [...rows], checking: false });
    } catch {
      if (current()) setState({ taskId, phase: "error", rows: [], checking: false });
    }
  }, [taskId]);

  useEffect(() => {
    latestTask.current = taskId;
    void reload();
    return () => { latestTask.current = null; generation.current++; };
  }, [reload]);

  useEffect(() => {
    if (!taskId) return;
    const refresh = async () => { setRevision(value => value + 1); await reload(); };
    const sources = listeners.get(taskId) ?? new Set<() => Promise<void>>();
    sources.add(refresh); listeners.set(taskId, sources);
    return () => { sources.delete(refresh); if (!sources.size) listeners.delete(taskId); };
  }, [taskId, reload]);

  const update = useCallback((dep: TaskDependency | string) => {
    if (latestTask.current !== taskId || !taskId) return;
    generation.current++;
    setState(prev => {
      if (prev.taskId !== taskId) return prev;
      const rows = typeof dep === "string" ? prev.rows.filter(row => row.dep.id !== dep) :
        [...prev.rows.filter(row => row.dep.id !== dep.id), ...rowsFor({ outgoing: [dep], incoming: [] })];
      return { ...prev, phase: "ready", rows, checking: false };
    });
  }, [taskId]);

  const isCurrent = useCallback(() => Boolean(taskId) && latestTask.current === taskId, [taskId]);

  const visible: DependencyState = state.taskId === taskId ? state : { taskId, phase: "loading", rows: [], checking: false };
  const blockers = visible.rows.filter(row => row.group === "blocked_by");
  const openBlockers = blockers.filter(row => row.status && !["done", "cancelled"].includes(row.status.category)).length;
  const unknownBlockers = blockers.filter(row => !row.status).length;
  return { ...visible, openBlockers, unknownBlockers, revision, reload, update, isCurrent };
}

export type TaskDependenciesSource = ReturnType<typeof useTaskDependencies>;
