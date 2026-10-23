// Data hooks for dashboard widgets (spec §3). Views and a theme's own widgets
// read the core only through these: no fetch in a view, current user's rights.
import { useEffect, useMemo, useState } from "react";
import { useParams } from "react-router";
import { api } from "@/lib/api";
import { fetchMentionInbox, mentionHref, type MentionInboxItem, type MentionSource } from "@/lib/mentions/inbox";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { PaginatedResponse, Task, TeamDirectory, TeamDirectoryAgent } from "@/types";
import type { Async, MentionItem, TaskQuery, MentionsData, SessionItem, TasksData, WidgetViewDataMap } from "./types";

const PRIO = ["urgent", "high", "medium", "low", "none"];
const SESSIONS_POLL_MS = 30_000;

export function resolveAgentStatus(agent: TeamDirectoryAgent): string {
  if (agent.is_stale) return "offline";
  return (agent.heartbeat_status ?? agent.status ?? "offline").toLowerCase();
}

/** One load per workspace (+ optional polling). A failed read yields `empty`, as the dashboard always did. */
function useLoad<T>(load: (wsId: string) => Promise<T>, empty: T, refreshMs?: number): Async<T> {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  // Tagged with its workspace: another workspace's data is never shown as ready.
  const [loaded, setLoaded] = useState<{ wsId: string; value: T } | null>(null);
  useEffect(() => {
    if (!wsId) return;
    let live = true;
    const run = () =>
      load(wsId)
        .then((v) => live && setLoaded({ wsId, value: v }))
        .catch(() => live && setLoaded({ wsId, value: empty }));
    void run();
    const id = refreshMs ? setInterval(() => void run(), refreshMs) : undefined;
    return () => {
      live = false;
      if (id) clearInterval(id);
    };
    // load/empty are module-level constants at every call site.
  }, [wsId, load, empty, refreshMs]);
  return useMemo<Async<T>>(
    () => (loaded === null || loaded.wsId !== wsId ? { state: "loading" } : { state: "ready", value: loaded.value }),
    [loaded, wsId],
  );
}

interface MentionsRaw {
  items: MentionInboxItem[];
  failed: MentionSource[];
}
const EMPTY_MENTIONS: MentionsRaw = { items: [], failed: ["task", "document"] };
const EMPTY_TASKS: TasksData = { items: [], total: 0, projectNames: {} };
const EMPTY_SESSIONS = { items: [] as SessionItem[] };

const loadMentions = async (wsId: string): Promise<MentionsRaw> => {
  const { items, failed } = await fetchMentionInbox(wsId, 10);
  return { items: items.slice(0, 10), failed };
};

export function useMentions(): Async<MentionsData> {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const { projects } = useProjectStore();
  const r = useLoad(loadMentions, EMPTY_MENTIONS);
  return useMemo<Async<MentionsData>>(() => {
    if (r.state !== "ready") return r;
    const items = r.value.items.map((m): MentionItem => {
      const project = projects.find((p) => p.id === m.project_id);
      // Task mentions keep the /t/:id resolver, which finds its own slugs.
      // Documents have no such resolver: without the project there is nowhere
      // honest to point.
      const href =
        m.source === "task" ? `/t/${m.task_id}?comment=${m.comment_id}` : mentionHref(m, wsSlug, project?.slug);
      return {
        id: m.comment_id,
        source: m.source,
        title: m.title,
        at: m.extracted_at,
        seen: !!m.seen_at,
        href,
        target:
          m.source === "task"
            ? { kind: "task", taskId: m.task_id, commentId: m.comment_id }
            : { kind: "document", docId: m.document_id, projectSlug: project?.slug },
      };
    });
    return { state: "ready", value: { items, failedSources: r.value.failed } };
  }, [r, projects, wsSlug]);
}

const loadTriage = async (wsId: string): Promise<TasksData> => {
  const data = await api<PaginatedResponse<Task>>(`/api/v1/workspaces/${wsId}/triage`, { params: { per_page: 10 } });
  const sorted = [...(data?.items ?? [])].sort((a, b) => {
    const pa = PRIO.indexOf(a.priority);
    const pb = PRIO.indexOf(b.priority);
    if (pa !== pb) return pa - pb;
    return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
  });
  return { items: sorted.slice(0, 5), total: sorted.length, projectNames: {} };
};

const loadMine = async (wsId: string): Promise<TasksData> => {
  const data = await api<PaginatedResponse<Task>>("/api/v1/me/tasks", { params: { workspace_id: wsId, per_page: 5 } });
  const items = data?.items ?? [];
  return { items, total: items.length, projectNames: {} };
};

function withProjectNames(r: Async<TasksData>, projects: { id: string; name: string }[]): Async<TasksData> {
  if (r.state !== "ready") return r;
  return { state: "ready", value: { ...r.value, projectNames: Object.fromEntries(projects.map((p) => [p.id, p.name])) } };
}

export function useTriageTasks(): Async<TasksData> {
  const { projects } = useProjectStore();
  const r = useLoad(loadTriage, EMPTY_TASKS);
  return useMemo(() => withProjectNames(r, projects), [r, projects]);
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

const UNSUPPORTED: Async<never> = {
  state: "error",
  error: new Error("This task query is not available to dashboard widgets yet"),
  retry: () => {},
};

const loadQuery = (q: TaskQuery) => async (wsId: string): Promise<TasksData> => {
  const base = await (q.gated ? loadTriage(wsId) : loadMine(wsId));
  let items = base.items;
  if (q.priority?.length) items = items.filter((t) => q.priority!.includes(t.priority));
  if (q.dueBefore) items = items.filter((t) => t.due_date && t.due_date < q.dueBefore!);
  if (q.sort === "priority") items = [...items].sort((a, b) => PRIO.indexOf(a.priority) - PRIO.indexOf(b.priority));
  items = items.slice(0, Math.min(q.limit ?? 50, 50));
  return { items, total: items.length, projectNames: {} };
};

/** Tasks for a theme's own widget: closed filters, current user's rights, no fetch in the view. */
export function useTasks(q: TaskQuery): Async<TasksData> {
  const { projects } = useProjectStore();
  const key = JSON.stringify(q);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const load = useMemo(() => loadQuery(q), [key]);
  const supported = q.scope === "mine" || (q.scope === "workspace" && !!q.gated);
  const r = useLoad(supported ? load : loadNothing, EMPTY_TASKS);
  return useMemo(() => (supported ? withProjectNames(r, projects) : UNSUPPORTED), [supported, r, projects]);
}
const loadNothing = async (): Promise<TasksData> => EMPTY_TASKS;

const NO_DATA = (): Async<undefined> => ({ state: "ready", value: undefined });
const WIDGET_HOOKS: Record<string, () => Async<unknown>> = {
  mentions: useMentions,
  triage: useTriageTasks,
  "my-tasks": useMyTasks,
  sessions: useActiveSessions,
};

/**
 * The registry's dataHook for `widgetId`, one entry point. The id must stay the
 * same for the life of the calling component (the hook set depends on it);
 * `Widget` keys its inner component by id, so a reused `Widget` remounts.
 */
export function useWidgetData<K extends keyof WidgetViewDataMap>(widgetId: K): WidgetViewDataMap[K];
export function useWidgetData(widgetId: string): Async<unknown>;
export function useWidgetData(widgetId: string): Async<unknown> {
  return (WIDGET_HOOKS[widgetId] ?? NO_DATA)();
}
