// mesh-default views of the four core widgets. Markup is the pre-T4b dashboard's,
// byte for byte; data and actions arrive through the slot contract.
import type { ElementType, MouseEvent, ReactNode } from "react";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { ArrowRight, AtSign, CheckSquare, FileText, Inbox, ListTodo, MonitorDot } from "lucide-react";
import i18n from "@/i18n";
import { AgentShortTag } from "@/components/agent-short-tag";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { TaskLink } from "@/core/primitives";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import type { Async, DashboardWidgetView } from "../types";

const PRIORITY_COLORS: Record<string, string> = {
  urgent: "bg-tone-danger-100 text-tone-danger-700 dark:bg-tone-danger-900/30 dark:text-tone-danger-400",
  high: "bg-tone-high-100 text-tone-high-700 dark:bg-tone-high-900/30 dark:text-tone-high-400",
  medium: "bg-tone-warning-100 text-tone-warning-700 dark:bg-tone-warning-900/30 dark:text-tone-warning-400",
  low: "bg-tone-info-100 text-tone-info-700 dark:bg-tone-info-900/30 dark:text-tone-info-400",
  none: "bg-tone-neutral-100 text-tone-neutral-600 dark:bg-tone-neutral-800/50 dark:text-tone-neutral-400",
};

const STATUS_DOT: Record<string, string> = {
  online: "bg-tone-success-500",
  busy: "bg-tone-attention-500",
  offline: "bg-tone-neutral-400",
  error: "bg-tone-danger-500",
};

function formatShort(isoStr: string): string {
  const diff = Date.now() - new Date(isoStr).getTime();
  if (diff < 60_000) return i18n.t("notifications:dashboard.short.now");
  if (diff < 3_600_000) return i18n.t("notifications:dashboard.short.minutes", { count: Math.floor(diff / 60_000) });
  if (diff < 86_400_000) return i18n.t("notifications:dashboard.short.hours", { count: Math.floor(diff / 3_600_000) });
  return i18n.t("notifications:dashboard.short.days", { count: Math.floor(diff / 86_400_000) });
}

type T = (k: string, o?: Record<string, unknown>) => string;
// Priority / heartbeat status are server enums; known values are labelled,
// anything new falls through as the raw value.
const priorityLabel = (t: T, priority: string) => t(`dashboard.priority.${priority}`, { defaultValue: priority });
const agentStatusLabel = (t: T, status: string) => t(`dashboard.agentStatus.${status}`, { defaultValue: status });

function WidgetSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-9 w-full rounded-md" />
      ))}
    </div>
  );
}

function WidgetEmpty({ icon: Icon, message }: { icon: ElementType; message: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-8 text-center text-muted-foreground">
      <Icon className="mb-2 h-6 w-6 opacity-30" />
      <p className="text-xs">{message}</p>
    </div>
  );
}

function WidgetError({ error, retry }: { error: Error; retry: () => void }) {
  return (
    <div role="alert" className="flex flex-col items-center justify-center gap-2 py-8 text-center text-xs text-muted-foreground">
      <p>{error.message}</p>
      <button type="button" className="underline hover:text-foreground" onClick={retry}>
        {i18n.t("common:retry", { defaultValue: "Retry" })}
      </button>
    </div>
  );
}

/** Card + body; loading / error / empty / list are four different things. */
function Body<D>({
  result,
  rows,
  isEmpty,
  empty,
  children,
}: {
  result: Async<D>;
  rows: number;
  isEmpty: (v: D) => boolean;
  empty: ReactNode;
  children: (v: D) => ReactNode;
}) {
  if (result.state === "loading") return <WidgetSkeleton rows={rows} />;
  if (result.state === "error") return <WidgetError error={result.error} retry={result.retry} />;
  return isEmpty(result.value) ? <>{empty}</> : <>{children(result.value)}</>;
}

const plainClick = (e: MouseEvent) => !(e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey);

export const MentionsWidgetView: DashboardWidgetView<"mentions"> = ({ data, actions, ctx }) => {
  const { t } = useTranslation("notifications");
  const activityTo = ctx.wsSlug ? `/w/${ctx.wsSlug}/activity` : "/";
  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <Link to={activityTo} className="flex items-center gap-1.5 text-sm font-semibold hover:text-primary">
            <AtSign className="h-4 w-4" />
            {t("dashboard.mentions.title")}
            <ArrowRight className="h-3 w-3 opacity-50" />
          </Link>
          <div className="flex items-center gap-3 text-xs text-muted-foreground">
            <Link to={activityTo} className="hover:text-foreground">
              {t("dashboard.mentions.myComments")}
            </Link>
            <Link to={activityTo} className="hover:text-foreground">
              {t("dashboard.mentions.allRecent")}
            </Link>
          </div>
        </div>
        <Body
          result={data.result}
          rows={4}
          isEmpty={(v) => v.items.length === 0}
          empty={
            <WidgetEmpty
              icon={AtSign}
              message={
                data.result.state === "ready" && data.result.value.failedSources.length > 0
                  ? t("dashboard.mentions.loadFailed")
                  : t("dashboard.mentions.empty")
              }
            />
          }
        >
          {(v) => (
            <ul className="space-y-0.5">
              {v.items.map((m) => {
                const SourceIcon = m.source === "document" ? FileText : ListTodo;
                return (
                  <li key={m.id}>
                    <Link
                      to={m.href ?? activityTo}
                      onClick={(e) => {
                        if (!plainClick(e)) return;
                        e.preventDefault();
                        actions.openMention(m);
                      }}
                      className={cn("flex items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-muted", !m.seen && "font-medium")}
                    >
                      {!m.seen ? (
                        <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-tone-danger-500" />
                      ) : (
                        <span className="h-1.5 w-1.5 shrink-0" />
                      )}
                      <SourceIcon
                        className="h-3 w-3 shrink-0 text-muted-foreground"
                        aria-label={m.source === "document" ? t("dashboard.mentions.document") : t("dashboard.mentions.task")}
                      />
                      <span className={cn("min-w-0 flex-1 truncate", m.seen && "text-muted-foreground")}>{m.title}</span>
                      <span className="shrink-0 text-[10px] text-muted-foreground">{formatShort(m.at)}</span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          )}
        </Body>
      </CardContent>
    </Card>
  );
};

export const TriageWidgetView: DashboardWidgetView<"triage"> = ({ data, ctx }) => {
  const { t } = useTranslation("notifications");
  const triageTo = ctx.wsSlug ? `/w/${ctx.wsSlug}/triage` : "/";
  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <Link to={triageTo} className="flex items-center gap-1.5 text-sm font-semibold hover:text-primary">
            <Inbox className="h-4 w-4" />
            {t("dashboard.triage.title")}
            <ArrowRight className="h-3 w-3 opacity-50" />
          </Link>
        </div>
        <Body result={data.result} rows={3} isEmpty={(v) => v.items.length === 0} empty={<WidgetEmpty icon={Inbox} message={t("dashboard.triage.empty")} />}>
          {(v) => (
            <ul className="space-y-0.5">
              {v.items.map((task) => (
                // The row opens the triage page, as it always did (not the task).
                <li key={task.id}>
                  <Link to={triageTo} className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-muted">
                    <span className={cn("shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium", PRIORITY_COLORS[task.priority] ?? PRIORITY_COLORS.none)}>
                      {priorityLabel(t, task.priority)}
                    </span>
                    <span className="min-w-0 flex-1 truncate font-medium">{task.title}</span>
                    <span className="shrink-0 truncate text-[10px] text-muted-foreground">{v.projectNames[task.project_id] ?? ""}</span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Body>
      </CardContent>
    </Card>
  );
};

export const MyTasksWidgetView: DashboardWidgetView<"my-tasks"> = ({ data }) => {
  const { t } = useTranslation("notifications");
  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <span className="flex items-center gap-1.5 text-sm font-semibold">
            <CheckSquare className="h-4 w-4" />
            {t("dashboard.myTasks.title")}
          </span>
        </div>
        <Body result={data.result} rows={3} isEmpty={(v) => v.items.length === 0} empty={<WidgetEmpty icon={CheckSquare} message={t("dashboard.myTasks.empty")} />}>
          {(v) => (
            <ul className="space-y-0.5">
              {v.items.map((task) => (
                <li key={task.id}>
                  <TaskLink taskId={task.id} className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-muted">
                    <span className={cn("shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium", PRIORITY_COLORS[task.priority] ?? PRIORITY_COLORS.none)}>
                      {priorityLabel(t, task.priority)}
                    </span>
                    <span className="min-w-0 flex-1 truncate font-medium">{task.title}</span>
                    <span className="shrink-0 truncate text-[10px] text-muted-foreground">{v.projectNames[task.project_id] ?? ""}</span>
                  </TaskLink>
                </li>
              ))}
            </ul>
          )}
        </Body>
      </CardContent>
    </Card>
  );
};

export const SessionsWidgetView: DashboardWidgetView<"sessions"> = ({ data, ctx }) => {
  const { t } = useTranslation("notifications");
  const sessionsTo = ctx.wsSlug ? `/w/${ctx.wsSlug}/sessions` : "/";
  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <Link to={sessionsTo} className="flex items-center gap-1.5 text-sm font-semibold hover:text-primary">
            <MonitorDot className="h-4 w-4" />
            {t("dashboard.sessions.title")}
            <ArrowRight className="h-3 w-3 opacity-50" />
          </Link>
        </div>
        <Body result={data.result} rows={3} isEmpty={(v) => v.items.length === 0} empty={<WidgetEmpty icon={MonitorDot} message={t("dashboard.sessions.empty")} />}>
          {(v) => (
            <ul className="space-y-0.5">
              {v.items.map(({ agent, status }) => {
                const dotColor = STATUS_DOT[status] ?? STATUS_DOT.offline;
                const msg = agent.heartbeat_message?.slice(0, 80) ?? null;
                const lastSeen = agent.last_heartbeat ? formatRelative(agent.last_heartbeat) : null;
                return (
                  <li key={agent.id} className="rounded-md px-2 py-1.5 text-xs hover:bg-muted">
                    <div className="flex items-center gap-2">
                      <span className={cn("h-2 w-2 shrink-0 rounded-full", dotColor)} />
                      <span className="font-medium">
                        {agent.name}
                        <AgentShortTag tag={agent.short_tag} />
                      </span>
                      <span className="text-[10px] capitalize text-muted-foreground">{agentStatusLabel(t, status)}</span>
                      {lastSeen && <span className="ml-auto shrink-0 text-[10px] text-muted-foreground">{lastSeen}</span>}
                    </div>
                    {msg && <p className="mt-0.5 truncate pl-4 text-[10px] text-muted-foreground">{msg}</p>}
                  </li>
                );
              })}
            </ul>
          )}
        </Body>
      </CardContent>
    </Card>
  );
};

export const MESH_DEFAULT_WIDGETS = {
  mentions: MentionsWidgetView,
  triage: TriageWidgetView,
  "my-tasks": MyTasksWidgetView,
  sessions: SessionsWidgetView,
} as const;
