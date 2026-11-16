// Data hooks of the "Tasks and triage" widget group (PRD §5.8, §6.3): my-tasks, triage-queue, task-list and the
// "awaiting triage" metric. Every filter runs on the server over the projects the viewer may read, so `total` is the
// real number of matches; a task.* event on the workspace channel reloads the result in place.
// The core runs them (hooks.ts), so they live outside the lazy group folder widgets/tasks/, which holds views only.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/lib/api";
import { useRealtime } from "@/core/primitives/use-realtime";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { RealtimeEvent } from "@entire-vc/mesh-theme-sdk";
import type { PaginatedResponse, Task } from "@/types";
import type { Async, TaskQuery, TasksData } from "../types";

/** Rows one task widget may ask for (server cap of GET .../tasks/query). */
export const TASK_QUERY_MAX = 50;
const REFRESH_DEBOUNCE_MS = 250;

/**
 * A task changed somewhere in the workspace. The bus sends `{type: status_change|context_update|custom,
 * data.subject: "task.moved"…}`; a direct `task.*` type is accepted too.
 */
export function isTaskEvent(e: RealtimeEvent): boolean {
  if (e.type.startsWith("task.")) return true;
  const subject = (e.data as { subject?: unknown } | undefined)?.subject;
  return typeof subject === "string" && subject.startsWith("task.");
}

type Loaded<T> = { key: string; value: T } | { key: string; error: Error };

/**
 * One server read per (workspace, key), reloaded on task events. A reload keeps the rows on screen until the new
 * answer arrives (no skeleton flash, no remount), and a failed reload keeps the last good answer.
 */
function useLiveTasksRead<T>(key: string, load: (wsId: string) => Promise<T>): Async<T> {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const fullKey = `${wsId ?? ""}|${key}`;
  const [loaded, setLoaded] = useState<Loaded<T> | null>(null);
  const [generation, setGeneration] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    if (!wsId) return;
    let live = true;
    loadRef
      .current(wsId)
      .then((value) => live && setLoaded({ key: fullKey, value }))
      .catch((e: unknown) => {
        if (!live) return;
        const error = e instanceof Error ? e : new Error(String(e));
        setLoaded((prev) => (prev && prev.key === fullKey && "value" in prev ? prev : { key: fullKey, error }));
      });
    return () => {
      live = false;
    };
  }, [wsId, fullKey, generation]);

  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const onEvent = useCallback((e: RealtimeEvent) => {
    if (!isTaskEvent(e)) return;
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setGeneration((n) => n + 1), REFRESH_DEBOUNCE_MS);
  }, []);
  useRealtime({ kind: "workspace" }, onEvent);

  return useMemo<Async<T>>(() => {
    if (loaded === null || loaded.key !== fullKey) return { state: "loading" };
    if ("error" in loaded)
      return {
        state: "error",
        error: loaded.error,
        retry: () => {
          setLoaded(null);
          setGeneration((n) => n + 1);
        },
      };
    return { state: "ready", value: loaded.value };
  }, [loaded, fullKey]);
}

type ProjectLabels = Pick<TasksData, "projectNames" | "projectIcons">;

function useProjectNames(): ProjectLabels {
  const projects = useProjectStore((s) => s.projects);
  return useMemo(
    () => ({
      projectNames: Object.fromEntries(projects.map((p) => [p.id, p.name])),
      projectIcons: Object.fromEntries(projects.map((p) => [p.id, p.icon ?? ""])),
    }),
    [projects],
  );
}

function withNames(r: Async<Omit<TasksData, "projectNames">>, labels: ProjectLabels): Async<TasksData> {
  return r.state === "ready" ? { state: "ready", value: { ...r.value, ...labels } } : r;
}

const clampLimit = (n: number | undefined, fallback: number) =>
  Math.min(TASK_QUERY_MAX, Math.max(1, Math.floor(n ?? fallback)));

const page = (data: PaginatedResponse<Task> | undefined) => ({
  items: data?.items ?? [],
  total: data?.total_count ?? data?.items?.length ?? 0,
});

/** Query string of GET /workspaces/:ws/tasks/query for a TaskQuery. */
export function taskQueryParams(q: TaskQuery): Record<string, string | number | undefined> {
  return {
    scope: q.scope,
    project_id: q.projectId || undefined,
    status_category: q.statusCategories?.length ? q.statusCategories.join(",") : undefined,
    priority: q.priority?.length ? q.priority.join(",") : undefined,
    due_before: q.dueBefore || undefined,
    gated: q.gated ? "true" : undefined,
    sort: q.sort,
    limit: clampLimit(q.limit, TASK_QUERY_MAX),
  };
}

/** Tasks for a widget or a theme's own widget: the closed TaskQuery, current user's rights, filtered on the server. */
export function useTasks(q: TaskQuery): Async<TasksData> {
  const params = taskQueryParams(q);
  const key = JSON.stringify(params);
  const load = useCallback(
    async (wsId: string) =>
      page(await api<PaginatedResponse<Task>>(`/api/v1/workspaces/${wsId}/tasks/query`, { params })),
    // params is rebuilt every render; key is its value
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key],
  );
  const r = useLiveTasksRead(`tasks|${key}`, load);
  const names = useProjectNames();
  return useMemo(() => withNames(r, names), [r, names]);
}

export interface TriageQueueOptions {
  limit?: number;
  projectId?: string;
  /** Only live hard human gates. */
  hardGatesOnly?: boolean;
}

/** The triage inbox (`GET /workspaces/:ws/triage`) in its server order; `total` is the whole queue, not the page. */
export function useTriageQueue(opts: TriageQueueOptions = {}): Async<TasksData> {
  const params = {
    page_size: clampLimit(opts.limit, 10),
    project_id: opts.projectId || undefined,
    hard_gates_only: opts.hardGatesOnly ? "true" : undefined,
  };
  const key = JSON.stringify(params);
  const load = useCallback(
    async (wsId: string) => page(await api<PaginatedResponse<Task>>(`/api/v1/workspaces/${wsId}/triage`, { params })),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key],
  );
  const r = useLiveTasksRead(`triage|${key}`, load);
  const names = useProjectNames();
  return useMemo(() => withNames(r, names), [r, names]);
}

/** "Awaiting triage" for the metric tile: the queue's total_count, one row fetched. */
export function useTriageCount(opts: { projectId?: string } = {}): Async<number> {
  const params = { page_size: 1, project_id: opts.projectId || undefined };
  const key = JSON.stringify(params);
  const load = useCallback(
    async (wsId: string) => page(await api<PaginatedResponse<Task>>(`/api/v1/workspaces/${wsId}/triage`, { params })).total,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key],
  );
  return useLiveTasksRead(`triage-count|${key}`, load);
}
