import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router";
import {
  ArrowRight,
  AtSign,
  CheckSquare,
  Inbox,
  MonitorDot,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { api } from "@/lib/api";
import { formatRelative } from "@/lib/utils";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type {
  Mention,
  PaginatedResponse,
  Task,
  TeamDirectory,
  TeamDirectoryAgent,
} from "@/types";

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

const PRIORITY_COLORS: Record<string, string> = {
  urgent: "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400",
  high: "bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400",
  medium: "bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400",
  low: "bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400",
  none: "bg-gray-100 text-gray-600 dark:bg-gray-800/50 dark:text-gray-400",
};

function formatShort(isoStr: string): string {
  const diff = Date.now() - new Date(isoStr).getTime();
  if (diff < 60_000) return "now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h`;
  return `${Math.floor(diff / 86_400_000)}d`;
}

function WidgetSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-9 w-full rounded-md" />
      ))}
    </div>
  );
}

function WidgetEmpty({
  icon: Icon,
  message,
}: {
  icon: React.ElementType;
  message: string;
}) {
  return (
    <div className="flex flex-col items-center justify-center py-8 text-center text-muted-foreground">
      <Icon className="mb-2 h-6 w-6 opacity-30" />
      <p className="text-xs">{message}</p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Mentions Widget
// ---------------------------------------------------------------------------

function MentionsWidget() {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const [mentions, setMentions] = useState<Mention[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api<Mention[]>("/api/v1/me/mentions", { params: { limit: 10 } })
      .then((data) => {
        const sorted = [...(data ?? [])].sort((a, b) => {
          if (!a.seen_at && b.seen_at) return -1;
          if (a.seen_at && !b.seen_at) return 1;
          return (
            new Date(b.extracted_at).getTime() -
            new Date(a.extracted_at).getTime()
          );
        });
        setMentions(sorted);
      })
      .catch(() => setMentions([]))
      .finally(() => setLoading(false));
  }, []);

  const activityTo = wsSlug ? `/w/${wsSlug}/activity` : "/";

  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <Link
            to={activityTo}
            className="flex items-center gap-1.5 text-sm font-semibold hover:text-primary"
          >
            <AtSign className="h-4 w-4" />
            Mentions
            <ArrowRight className="h-3 w-3 opacity-50" />
          </Link>
          <div className="flex items-center gap-3 text-xs text-muted-foreground">
            <Link to={activityTo} className="hover:text-foreground">
              my comments
            </Link>
            <Link to={activityTo} className="hover:text-foreground">
              all recent
            </Link>
          </div>
        </div>

        {loading ? (
          <WidgetSkeleton rows={4} />
        ) : mentions.length === 0 ? (
          <WidgetEmpty icon={AtSign} message="No mentions yet" />
        ) : (
          <ul className="space-y-0.5">
            {mentions.map((m) => (
              <li key={m.comment_id}>
                <Link
                  to={`/t/${m.task_id}`}
                  className={cn(
                    "flex items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-muted",
                    !m.seen_at && "font-medium",
                  )}
                >
                  {!m.seen_at ? (
                    <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-red-500" />
                  ) : (
                    <span className="h-1.5 w-1.5 shrink-0" />
                  )}
                  <span
                    className={cn(
                      "min-w-0 flex-1 truncate",
                      m.seen_at && "text-muted-foreground",
                    )}
                  >
                    {m.task_title}
                  </span>
                  <span className="shrink-0 text-[10px] text-muted-foreground">
                    {formatShort(m.extracted_at)}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Triage Widget
// ---------------------------------------------------------------------------

function TriageWidget() {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const { currentWorkspace } = useWorkspaceStore();
  const { projects } = useProjectStore();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!currentWorkspace) return;
    api<PaginatedResponse<Task>>(
      `/api/v1/workspaces/${currentWorkspace.id}/triage`,
      { params: { per_page: 10 } },
    )
      .then((data) => {
        const PRIO = ["urgent", "high", "medium", "low", "none"];
        const sorted = [...(data?.items ?? [])].sort((a, b) => {
          const pa = PRIO.indexOf(a.priority);
          const pb = PRIO.indexOf(b.priority);
          if (pa !== pb) return pa - pb;
          return (
            new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
          );
        });
        setTasks(sorted.slice(0, 5));
      })
      .catch(() => setTasks([]))
      .finally(() => setLoading(false));
  }, [currentWorkspace]);

  const projectMap = Object.fromEntries(projects.map((p) => [p.id, p.name]));
  const triageTo = wsSlug ? `/w/${wsSlug}/triage` : "/";

  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <Link
            to={triageTo}
            className="flex items-center gap-1.5 text-sm font-semibold hover:text-primary"
          >
            <Inbox className="h-4 w-4" />
            Triage
            <ArrowRight className="h-3 w-3 opacity-50" />
          </Link>
        </div>

        {loading ? (
          <WidgetSkeleton rows={3} />
        ) : tasks.length === 0 ? (
          <WidgetEmpty icon={Inbox} message="Inbox is empty" />
        ) : (
          <ul className="space-y-0.5">
            {tasks.map((task) => (
              <li key={task.id}>
                <Link
                  to={triageTo}
                  className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-muted"
                >
                  <span
                    className={cn(
                      "shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium",
                      PRIORITY_COLORS[task.priority] ?? PRIORITY_COLORS.none,
                    )}
                  >
                    {task.priority}
                  </span>
                  <span className="min-w-0 flex-1 truncate font-medium">
                    {task.title}
                  </span>
                  <span className="shrink-0 truncate text-[10px] text-muted-foreground">
                    {projectMap[task.project_id] ?? ""}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// My Tasks Widget
// ---------------------------------------------------------------------------

function MyTasksWidget() {
  const { currentWorkspace } = useWorkspaceStore();
  const { projects } = useProjectStore();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!currentWorkspace) return;
    api<PaginatedResponse<Task>>("/api/v1/me/tasks", {
      params: { workspace_id: currentWorkspace.id, per_page: 5 },
    })
      .then((data) => setTasks(data?.items ?? []))
      .catch(() => setTasks([]))
      .finally(() => setLoading(false));
  }, [currentWorkspace]);

  const projectMap = Object.fromEntries(projects.map((p) => [p.id, p.name]));

  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <span className="flex items-center gap-1.5 text-sm font-semibold">
            <CheckSquare className="h-4 w-4" />
            My Tasks
          </span>
        </div>

        {loading ? (
          <WidgetSkeleton rows={3} />
        ) : tasks.length === 0 ? (
          <WidgetEmpty
            icon={CheckSquare}
            message="No active tasks assigned to you"
          />
        ) : (
          <ul className="space-y-0.5">
            {tasks.map((task) => (
              <li key={task.id}>
                <Link
                  to={`/t/${task.id}`}
                  className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-muted"
                >
                  <span
                    className={cn(
                      "shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium",
                      PRIORITY_COLORS[task.priority] ?? PRIORITY_COLORS.none,
                    )}
                  >
                    {task.priority}
                  </span>
                  <span className="min-w-0 flex-1 truncate font-medium">
                    {task.title}
                  </span>
                  <span className="shrink-0 truncate text-[10px] text-muted-foreground">
                    {projectMap[task.project_id] ?? ""}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Sessions Widget
// ---------------------------------------------------------------------------

const STATUS_DOT: Record<string, string> = {
  online: "bg-green-500",
  busy: "bg-yellow-500",
  offline: "bg-gray-400",
  error: "bg-red-500",
};

function resolveAgentStatus(agent: TeamDirectoryAgent): string {
  if (agent.is_stale) return "offline";
  return (agent.heartbeat_status ?? agent.status ?? "offline").toLowerCase();
}

function SessionsWidget() {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const { currentWorkspace } = useWorkspaceStore();
  const [agents, setAgents] = useState<TeamDirectoryAgent[]>([]);
  const [loading, setLoading] = useState(true);

  const fetchAgents = useCallback(
    async (wsId: string) => {
      try {
        const data = await api<TeamDirectory>(`/api/v1/workspaces/${wsId}/team`);
        const all = data?.agents ?? [];
        const active = all
          .filter((a) => {
            const s = resolveAgentStatus(a);
            return s !== "offline" && s !== "error";
          })
          .slice(0, 5);
        setAgents(active);
      } catch {
        setAgents([]);
      } finally {
        setLoading(false);
      }
    },
    [],
  );

  useEffect(() => {
    if (!currentWorkspace) return;
    void fetchAgents(currentWorkspace.id);
    const id = setInterval(() => void fetchAgents(currentWorkspace.id), 30_000);
    return () => clearInterval(id);
  }, [currentWorkspace, fetchAgents]);

  const sessionsTo = wsSlug ? `/w/${wsSlug}/sessions` : "/";

  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <Link
            to={sessionsTo}
            className="flex items-center gap-1.5 text-sm font-semibold hover:text-primary"
          >
            <MonitorDot className="h-4 w-4" />
            Sessions
            <ArrowRight className="h-3 w-3 opacity-50" />
          </Link>
        </div>

        {loading ? (
          <WidgetSkeleton rows={3} />
        ) : agents.length === 0 ? (
          <WidgetEmpty icon={MonitorDot} message="No agents online" />
        ) : (
          <ul className="space-y-0.5">
            {agents.map((agent) => {
              const status = resolveAgentStatus(agent);
              const dotColor = STATUS_DOT[status] ?? STATUS_DOT.offline;
              const msg = agent.heartbeat_message?.slice(0, 80) ?? null;
              const lastSeen = agent.last_heartbeat
                ? formatRelative(agent.last_heartbeat)
                : null;

              return (
                <li
                  key={agent.id}
                  className="rounded-md px-2 py-1.5 text-xs hover:bg-muted"
                >
                  <div className="flex items-center gap-2">
                    <span
                      className={cn("h-2 w-2 shrink-0 rounded-full", dotColor)}
                    />
                    <span className="font-medium">{agent.name}</span>
                    <span className="text-[10px] capitalize text-muted-foreground">
                      {status}
                    </span>
                    {lastSeen && (
                      <span className="ml-auto shrink-0 text-[10px] text-muted-foreground">
                        {lastSeen}
                      </span>
                    )}
                  </div>
                  {msg && (
                    <p className="mt-0.5 truncate pl-4 text-[10px] text-muted-foreground">
                      {msg}
                    </p>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// DashboardPage
// ---------------------------------------------------------------------------

export function DashboardPage() {
  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
      <MentionsWidget />
      <TriageWidget />
      <MyTasksWidget />
      <SessionsWidget />
    </div>
  );
}
