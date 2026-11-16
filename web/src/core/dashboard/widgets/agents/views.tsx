// mesh-default views of the agent widgets. Each one sizes to its grid cell
// through CSS container queries, never the window, so a 3-cell column on a
// wide screen and a phone draw the same way.
import type { ElementType, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Bot, MonitorDot, Users } from "lucide-react";
import i18n from "@/i18n";
import { AgentShortTag } from "@/components/agent-short-tag";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import type { Async, WidgetViewProps } from "../../types";
import { AgentSessionCard, statusConfig, useAgentCardDirectory, type AgentStatusEntry } from "./agent-card";
import {
  clampLimit,
  filterAgents,
  useActiveAgents,
  useAgentsStatus,
  useAgentsSummary,
  type ActiveAgent,
  type AgentStatus,
  type AgentsStatusFilter,
  type AgentsSummary,
} from "./use-agents";

const NS = "integrations";

function Skeletons({ rows, className }: { rows: number; className: string }) {
  return (
    <div className="space-y-2" data-testid="widget-loading">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className={className} />
      ))}
    </div>
  );
}

function Empty({ icon: Icon, message }: { icon: ElementType; message: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-6 text-center text-muted-foreground">
      <Icon className="mb-2 h-6 w-6 opacity-30" aria-hidden="true" />
      <p className="text-xs">{message}</p>
    </div>
  );
}

function Failed({ retry }: { retry: () => void }) {
  return (
    <div role="alert" className="flex flex-col items-center justify-center gap-2 py-6 text-center text-xs text-muted-foreground">
      <AlertTriangle className="h-5 w-5 opacity-50" aria-hidden="true" />
      <p>{i18n.t(`${NS}:sessions.widgets.loadFailed`)}</p>
      <button type="button" className="min-h-11 px-3 underline hover:text-foreground" onClick={retry}>
        {i18n.t("common:retry", { defaultValue: "Retry" })}
      </button>
    </div>
  );
}

/** Loading, error and empty are three different views; only error has a retry. */
function Body<D>({
  result,
  loading,
  isEmpty,
  empty,
  children,
}: {
  result: Async<D>;
  loading: ReactNode;
  isEmpty: (v: D) => boolean;
  empty: ReactNode;
  children: (v: D) => ReactNode;
}) {
  if (result.state === "loading") return <>{loading}</>;
  if (result.state === "error") return <Failed retry={result.retry} />;
  return isEmpty(result.value) ? <>{empty}</> : <>{children(result.value)}</>;
}

function Frame({ icon: Icon, title, aside, children }: { icon: ElementType; title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <Card className="@container h-full min-w-0">
      <CardContent className="pt-4">
        <div className="mb-3 flex min-w-0 flex-wrap items-center justify-between gap-x-2 gap-y-1">
          <h3 className="flex min-w-0 items-center gap-1.5 text-sm font-semibold">
            <Icon className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span className="break-words">{title}</span>
          </h3>
          {aside}
        </div>
        {children}
      </CardContent>
    </Card>
  );
}

const DOT: Record<string, string> = Object.fromEntries(Object.entries(statusConfig).map(([k, v]) => [k, v.dotColor]));
const statusLabel = (s: string) => i18n.t(`${NS}:sessions.status.${s}`, { defaultValue: s });

// ---------------------------------------------------------------- agents-active

export interface AgentsActiveSettings {
  title?: string;
  limit?: number;
}

export function AgentsActiveBody({ result }: { result: Async<{ items: ActiveAgent[]; total: number }> }) {
  const { t } = useTranslation(NS);
  return (
    <Body
      result={result}
      loading={<Skeletons rows={3} className="h-9 w-full rounded-md" />}
      isEmpty={(v) => v.items.length === 0}
      empty={<Empty icon={MonitorDot} message={t("sessions.widgets.activeEmpty")} />}
    >
      {(v) => (
        <>
          <ul className="space-y-0.5">
            {v.items.map(({ agent, status }) => (
              <li key={agent.id} className="rounded-md px-2 py-1.5 text-xs hover:bg-muted">
                <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
                  <span className={cn("h-2 w-2 shrink-0 rounded-full", DOT[status] ?? DOT.offline)} aria-hidden="true" />
                  <span className="min-w-0 break-words font-medium">
                    {agent.name}
                    <AgentShortTag tag={agent.short_tag} />
                  </span>
                  <span className="text-[10px] text-muted-foreground">{statusLabel(status)}</span>
                  {agent.last_heartbeat && (
                    <span className="ml-auto shrink-0 text-[10px] text-muted-foreground">{formatRelative(agent.last_heartbeat)}</span>
                  )}
                </div>
                {agent.heartbeat_message && (
                  <p className="mt-0.5 line-clamp-2 break-words pl-4 text-[10px] text-muted-foreground">{agent.heartbeat_message}</p>
                )}
              </li>
            ))}
          </ul>
          {v.total > v.items.length && (
            <p className="mt-2 px-2 text-[10px] text-muted-foreground">{t("sessions.widgets.activeMore", { count: v.total - v.items.length })}</p>
          )}
        </>
      )}
    </Body>
  );
}

export function AgentsActiveWidget({ settings }: Partial<WidgetViewProps<AgentsActiveSettings>>) {
  const { t } = useTranslation(NS);
  const result = useActiveAgents(clampLimit(settings?.limit));
  return (
    <Frame icon={MonitorDot} title={settings?.title || t("sessions.widgets.activeTitle")}>
      <AgentsActiveBody result={result} />
    </Frame>
  );
}

// ---------------------------------------------------------------- agents-status

export interface AgentsStatusSettings extends AgentsStatusFilter {
  title?: string;
}

export function AgentsStatusBody({ result, filter }: { result: Async<AgentStatusEntry[]>; filter: AgentsStatusFilter }) {
  const { t } = useTranslation(NS);
  useAgentCardDirectory();
  return (
    <Body
      result={result}
      loading={
        <div className="grid grid-cols-1 gap-3 @xl:grid-cols-2 @4xl:grid-cols-3" data-testid="widget-loading">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-36 w-full rounded-xl" />
          ))}
        </div>
      }
      isEmpty={(all) => filterAgents(all, filter).length === 0}
      empty={
        result.state === "ready" && result.value.length > 0 ? (
          <Empty icon={Bot} message={t("sessions.widgets.statusFilteredOut", { count: result.value.length })} />
        ) : (
          <Empty icon={Bot} message={t("sessions.noAgents")} />
        )
      }
    >
      {(all) => (
        <div className="grid grid-cols-1 gap-3 @xl:grid-cols-2 @4xl:grid-cols-3">
          {filterAgents(all, filter).map((agent) => (
            <AgentSessionCard key={agent.id} agent={agent} />
          ))}
        </div>
      )}
    </Body>
  );
}

export function AgentsStatusWidget({ settings }: Partial<WidgetViewProps<AgentsStatusSettings>>) {
  const { t } = useTranslation(NS);
  const result = useAgentsStatus();
  const filter: AgentsStatusFilter = { statuses: settings?.statuses, onlyWithTask: settings?.onlyWithTask };
  return (
    <Frame icon={Bot} title={settings?.title || t("sessions.widgets.statusTitle")}>
      <AgentsStatusBody result={result} filter={filter} />
    </Frame>
  );
}

// ---------------------------------------------------------------- agents-summary

const CHIPS: { key: keyof Omit<AgentsSummary, "total">; dot: string; label: string }[] = [
  { key: "online", dot: statusConfig.online.dotColor, label: "sessions.chips.online" },
  { key: "busy", dot: statusConfig.busy.dotColor, label: "sessions.chips.busy" },
  { key: "offline", dot: statusConfig.offline.dotColor, label: "sessions.chips.offlineError" },
];

export function AgentsSummaryBody({ result }: { result: Async<AgentsSummary> }) {
  const { t } = useTranslation(NS);
  return (
    <Body
      result={result}
      loading={
        <div className="flex flex-wrap gap-2" data-testid="widget-loading">
          {CHIPS.map((c) => (
            <Skeleton key={c.key} className="h-9 w-24 rounded-lg" />
          ))}
        </div>
      }
      isEmpty={(v) => v.total === 0}
      empty={<Empty icon={Users} message={t("sessions.noAgents")} />}
    >
      {(v) => (
        <ul className="flex flex-wrap gap-2">
          {CHIPS.map((c) => (
            <li key={c.key} className="flex items-center gap-2 rounded-lg border bg-card px-3 py-2 text-sm" data-agents-count={c.key}>
              <span className={cn("h-2 w-2 shrink-0 rounded-full", c.dot)} aria-hidden="true" />
              <span className="font-medium tabular-nums">{v[c.key]}</span>
              <span className="text-muted-foreground">{t(c.label)}</span>
            </li>
          ))}
        </ul>
      )}
    </Body>
  );
}

export function AgentsSummaryWidget({ settings }: Partial<WidgetViewProps<{ title?: string }>>) {
  const { t } = useTranslation(NS);
  const result = useAgentsSummary();
  return (
    <Frame icon={Users} title={settings?.title || t("sessions.widgets.summaryTitle")}>
      <AgentsSummaryBody result={result} />
    </Frame>
  );
}

/** The group's views by descriptor id: the registry loads this file the first time an agent widget is drawn. */
export const views = {
  "agents-active": AgentsActiveWidget,
  "agents-status": AgentsStatusWidget,
  "agents-summary": AgentsSummaryWidget,
};

export type { AgentStatus };
