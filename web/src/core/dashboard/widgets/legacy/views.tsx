// mesh-default views of the legacy core widgets (mentions, triage, sessions). Markup is the pre-T4b dashboard's,
// byte for byte; data and actions arrive through the slot contract. my-tasks lives with the tasks group, which
// shares the priority chips and the card parts (./parts) with these views.
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { ArrowRight, Inbox, MonitorDot } from "lucide-react";
import { AgentShortTag } from "@/components/agent-short-tag";
import { Card, CardContent } from "@/components/ui/card";
import { ProjectLabel } from "@/components/ui/project-label";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import type { DashboardWidgetView } from "../../types";
import { useWidgetTitle } from "../../config";
import { MentionsWidgetView } from "../comms/views";
import { Body, WidgetEmpty, plainClick } from "./parts";

export const PRIORITY_COLORS: Record<string, string> = {
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

type T = (k: string, o?: Record<string, unknown>) => string;
// Priority / heartbeat status are server enums; known values are labelled,
// anything new falls through as the raw value.
export const priorityLabel = (t: T, priority: string) => t(`dashboard.priority.${priority}`, { defaultValue: priority });
const agentStatusLabel = (t: T, status: string) => t(`dashboard.agentStatus.${status}`, { defaultValue: status });

export const TriageWidgetView: DashboardWidgetView<"triage"> = ({ data, actions }) => {
  const { t } = useTranslation("notifications");
  const title = useWidgetTitle(t("dashboard.triage.title"));
  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <h3 className="flex items-center gap-1.5 text-sm font-semibold">
            <Inbox className="h-4 w-4" />
            {title}
          </h3>
        </div>
        <Body result={data.result} rows={3} isEmpty={(v) => v.items.length === 0} empty={<WidgetEmpty icon={Inbox} message={t("dashboard.triage.empty")} />}>
          {(v) => (
            <ul className="space-y-0.5">
              {v.items.map((task) => (
                // The row opens the task (the triage page is a dashboard template now, §5.9).
                <li key={task.id}>
                  <Link
                    to={`/t/${task.id}`}
                    onClick={(e) => {
                      if (!plainClick(e)) return;
                      e.preventDefault();
                      actions.openTask(task.id);
                    }}
                    className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-muted">
                    <span className={cn("shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium", PRIORITY_COLORS[task.priority] ?? PRIORITY_COLORS.none)}>
                      {priorityLabel(t, task.priority)}
                    </span>
                    <span className="min-w-0 flex-1 truncate font-medium">{task.title}</span>
                    <ProjectLabel icon={v.projectIcons?.[task.project_id]} name={v.projectNames[task.project_id] ?? ""} className="max-w-40 shrink-0 text-[10px] text-muted-foreground" />
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

export const SessionsWidgetView: DashboardWidgetView<"sessions"> = ({ data, ctx }) => {
  const { t } = useTranslation("notifications");
  const title = useWidgetTitle(t("dashboard.sessions.title"));
  // The agents are in Team now that the Sessions page is a dashboard template (§5.9).
  const sessionsTo = ctx.wsSlug ? `/w/${ctx.wsSlug}/org-chart` : "/";
  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex items-center justify-between">
          <Link to={sessionsTo} className="flex items-center gap-1.5 text-sm font-semibold hover:text-primary">
            <MonitorDot className="h-4 w-4" />
            {title}
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
  sessions: SessionsWidgetView,
} as const;

export const views = MESH_DEFAULT_WIDGETS;
