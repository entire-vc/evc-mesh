// Data hooks for dashboard widgets (spec §3). Views and a theme's own widgets
// read the core only through these: no fetch in a view, current user's rights.
import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { PaginatedResponse, Task, TeamDirectory, TeamDirectoryAgent } from "@/types";
import { useMentions, useMyComments, useRecentComments, useUnseenMentionCount } from "./widgets/comms/hooks";
import type { Async, SessionItem, TasksData, WidgetViewDataMap } from "./types";
import { useTasks, useTriageQueue } from "./tasks/hooks";
import { myTasksQuery, taskListQuery, triageQueueOptions } from "./widgets/tasks/query";

export { useMentions, useMyComments, useRecentComments, useUnseenMentionCount };

const PRIO = ["urgent", "high", "medium", "low", "none"];
const SESSIONS_POLL_MS = 30_000;

export function resolveAgentStatus(agent: TeamDirectoryAgent): string {
  if (agent.is_stale) return "offline";
  return (agent.heartbeat_status ?? agent.status ?? "offline").toLowerCase();
}

/**
 * One load per workspace (+ optional polling). A failed read yields `empty`, as the dashboard always did, unless
 * `surfaceErrors`: then it is an `error` state with a retry (a theme's own widget must not show an outage as "nothing here").
 */
function useLoad<T>(load: (wsId: string) => Promise<T>, empty: T, refreshMs?: number, surfaceErrors = false): Async<T> {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  // Tagged with its workspace: another workspace's data is never shown as ready.
  const [loaded, setLoaded] = useState<{ wsId: string; value: T } | { wsId: string; error: Error } | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!wsId) return;
    let live = true;
    const run = () =>
      load(wsId)
        .then((v) => live && setLoaded({ wsId, value: v }))
        .catch((e) => live && setLoaded(surfaceErrors ? { wsId, error: e instanceof Error ? e : new Error(String(e)) } : { wsId, value: empty }));
    void run();
    const id = refreshMs ? setInterval(() => void run(), refreshMs) : undefined;
    return () => {
      live = false;
      if (id) clearInterval(id);
    };
    // load/empty are module-level constants at every call site.
  }, [wsId, load, empty, refreshMs, surfaceErrors, attempt]);
  return useMemo<Async<T>>(() => {
    if (loaded === null || loaded.wsId !== wsId) return { state: "loading" };
    if ("error" in loaded) return { state: "error", error: loaded.error, retry: () => { setLoaded(null); setAttempt((n) => n + 1); } };
    return { state: "ready", value: loaded.value };
  }, [loaded, wsId]);
}

const EMPTY_TASKS: TasksData = { items: [], total: 0, projectNames: {} };
const EMPTY_SESSIONS = { items: [] as SessionItem[] };

const loadTriage = async (wsId: string): Promise<TasksData> => {
  // The server orders the queue by gate, not by priority: read the whole window (50) so the
  // priority sort below never drops an urgent row that sits past the first page.
  const data = await api<PaginatedResponse<Task>>(`/api/v1/workspaces/${wsId}/triage`, { params: { page_size: 50 } });
  const sorted = [...(data?.items ?? [])].sort((a, b) => {
    const pa = PRIO.indexOf(a.priority);
    const pb = PRIO.indexOf(b.priority);
    if (pa !== pb) return pa - pb;
    return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
  });
  // the whole queue, not the ten rows read
  return { items: sorted.slice(0, 5), total: data?.total_count ?? sorted.length, projectNames: {} };
};

const loadMine = async (wsId: string, perPage = 5): Promise<TasksData> => {
  const data = await api<PaginatedResponse<Task>>("/api/v1/me/tasks", { params: { workspace_id: wsId, per_page: perPage } });
  const items = data?.items ?? [];
  return { items, total: items.length, projectNames: {} };
};

function withProjectNames(r: Async<TasksData>, projects: { id: string; name: string; icon?: string }[]): Async<TasksData> {
  if (r.state !== "ready") return r;
  return {
    state: "ready",
    value: {
      ...r.value,
      projectNames: Object.fromEntries(projects.map((p) => [p.id, p.name])),
      projectIcons: Object.fromEntries(projects.map((p) => [p.id, p.icon ?? ""])),
    },
  };
}

export function useTriageTasks(): Async<TasksData> {
  const { projects } = useProjectStore();
  const r = useLoad(loadTriage, EMPTY_TASKS);
  return useMemo(() => withProjectNames(r, projects), [r, projects]);
}

const loadTriageCount = async (wsId: string): Promise<number> => {
  // the queue's own total, not the length of the five rows the triage card draws
  const data = await api<PaginatedResponse<Task>>(`/api/v1/workspaces/${wsId}/triage`, { params: { per_page: 1 } });
  return data?.total_count ?? 0;
};

/** Cards waiting in the triage queue: the «Ждут триажа» metric (W4). A failed read is an error, never a zero. */
export function useTriageCount(): Async<number> {
  return useLoad(loadTriageCount, 0, undefined, true);
}

export function useMyTasks(): Async<TasksData> {
  const { projects } = useProjectStore();
  const r = useLoad(loadMine, EMPTY_TASKS);
  return useMemo(() => withProjectNames(r, projects), [r, projects]);
}

const loadSessions = async (wsId: string) => {
  const data = await api<TeamDirectory>(`/api/v1/workspaces/${wsId}/team`);
  const items = (data?.agents ?? [])
    .map((agent) => ({ agent, status: resolveAgentStatus(agent) }))
    .filter((s) => s.status !== "offline" && s.status !== "error")
    .slice(0, 5);
  return { items };
};

export function useActiveSessions(): Async<{ items: SessionItem[] }> {
  return useLoad(loadSessions, EMPTY_SESSIONS, SESSIONS_POLL_MS);
}

// useTasks lives with the task widgets (widgets/tasks): every TaskQuery field is filtered on the server.
export { useTasks } from "./tasks/hooks";

type Settings = Readonly<Record<string, unknown>>;
const NO_SETTINGS: Settings = {};
const NO_DATA = (): Async<undefined> => ({ state: "ready", value: undefined });
// A hook gets the instance settings; the four pre-registry hooks have none and ignore them.
const WIDGET_HOOKS: Record<string, (settings: Settings) => Async<unknown>> = {
  mentions: useMentions,
  triage: useTriageTasks,
  sessions: useActiveSessions,
  "my-tasks": (s) => useTasks(myTasksQuery(s)),
  "triage-queue": (s) => useTriageQueue(triageQueueOptions(s)),
  "task-list": (s) => useTasks(taskListQuery(s)),
  "my-comments": useMyComments,
  "recent-comments": useRecentComments,
};

/**
 * The registry's dataHook for `widgetId`, one entry point. The id must stay the
 * same for the life of the calling component (the hook set depends on it);
 * `Widget` keys its inner component by id, so a reused `Widget` remounts.
 */
export function useWidgetData<K extends keyof WidgetViewDataMap>(widgetId: K, settings?: Settings): WidgetViewDataMap[K];
export function useWidgetData(widgetId: string, settings?: Settings): Async<unknown>;
export function useWidgetData(widgetId: string, settings: Settings = NO_SETTINGS): Async<unknown> {
  return (WIDGET_HOOKS[widgetId] ?? NO_DATA)(settings);
}
