// Views of the analytics and cost widgets. Each reads useAnalytics with the instance's filter (period and
// project): widgets with the same filter share one request. Nothing here fetches on its own.
import { useCallback } from "react";
import { useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { ArrowDownRight, ArrowUpRight, Minus } from "lucide-react";
import { TaskLink } from "@entire-vc/mesh-theme-sdk";
import { AgentShortTag } from "@/components/agent-short-tag";
import { ApiPriceNote } from "@/components/cost-quality-block";
import * as fmt from "@/i18n/format";
import { useProjectStore } from "@/stores/project";
import type { AnalyticsMetrics } from "@/types";
import { useTriageCount, useUnseenMentionCount } from "../../hooks";
import type { Async, WidgetViewProps } from "../../types";
import { previousPeriod, readQuery } from "./filters";
import { MemoryUsageView } from "./memory-view";
import { LIVE_METRIC_CHOICES, METRICS, eventsIncomplete, type LiveMetricId, type MetricDef } from "./metrics";
import { useAnalytics, type AnalyticsQuery } from "./use-analytics";
import {
  AnalyticsBody,
  BarList,
  ColumnChart,
  Donut,
  LineChart,
  NEUTRAL,
  Tile,
  WidgetFrame,
  formatCost,
  formatTokens,
  priorityColor,
  seriesColor,
  statusColor,
  topN,
  useAT,
  type AT,
  type Row,
} from "./ui";

/** The actions the host passes (WidgetActionsMap names); a view uses only those its descriptor lists. */
export interface AnalyticsActions {
  openTask?: (taskId: string, opts?: { newTab?: boolean }) => void;
  openAgent?: (agentId: string) => void;
  retry?: () => void;
}
export type AnalyticsViewProps = WidgetViewProps<Record<string, unknown>, AnalyticsActions>;

/** Rows of a breakdown before "Other". */
export const TOP_N = 6;

type Metrics = AnalyticsMetrics;
type Cost = Metrics["cost_metrics"];

const costIncomplete = (c: Cost) => (c.cost_incomplete || c.tokens_in_incomplete || c.tokens_out_incomplete ? null : false);
const humanize = (s: string) => s.replace(/_/g, " ");
const sum = (xs: number[]) => xs.reduce((a, b) => a + b, 0);

/** Project page of a project id, when the project is one the viewer has loaded. */
function useProjectHref(): (projectId: string) => string | undefined {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const projects = useProjectStore((s) => s.projects);
  return useCallback(
    (id: string) => {
      const slug = projects.find((p) => p.id === id)?.slug;
      return wsSlug && slug ? `/w/${wsSlug}/p/${slug}` : undefined;
    },
    [projects, wsSlug],
  );
}

const agentOpener = (actions: AnalyticsActions, id: string | null | undefined) =>
  id && actions.openAgent ? () => actions.openAgent!(id) : undefined;

// -- metric -------------------------------------------------------------------------------------------

/**
 * Counts that live outside analytics: the moment's queue, not a period, and their own endpoint (not the shared
 * analytics request). The project filter does not narrow them either, so the frame shows no period and no CSV.
 */
export interface LiveMetricDef {
  id: string;
  label: string;
  useCount: () => Async<number>;
}

const LIVE_COUNT: Record<LiveMetricId, () => Async<number>> = { triage_waiting: useTriageCount, mentions_unseen: useUnseenMentionCount };
export const LIVE_METRICS: LiveMetricDef[] = LIVE_METRIC_CHOICES.map((m) => ({ ...m, useCount: LIVE_COUNT[m.id] }));

/** The same metric over the previous period of equal length: a second request, shared like the first. */
function Comparison({ query, metric, current }: { query: AnalyticsQuery; metric: MetricDef; current: number }) {
  const at = useAT();
  const prev = useAnalytics({ ...query, period: previousPeriod(query.period!) });
  if (prev.state !== "ready") return null;
  // An undercounted previous period would show a growth that is not there.
  if (metric.incomplete && metric.incomplete(prev.value) !== false) {
    return <p className="truncate text-[11px] text-muted-foreground">{at("metric.noComparison", "No comparison: the previous period is incomplete")}</p>;
  }
  const before = metric.read(prev.value);
  const diff = current - before;
  const Icon = diff > 0 ? ArrowUpRight : diff < 0 ? ArrowDownRight : Minus;
  const sign = diff > 0 ? "+" : "−"; // the same minus sign for the count and the percentage
  const pct = before > 0 ? ` (${sign}${Math.abs(Math.round((diff / before) * 100))}%)` : "";
  const text =
    diff === 0
      ? at("metric.same", "Same as the previous period")
      : at("metric.delta", "{{delta}} vs previous period", { delta: `${sign}${fmt.formatNumber(Math.abs(diff))}${pct}` });
  return (
    <p data-testid="metric-comparison" className="flex min-w-0 items-center gap-1 text-[11px] text-muted-foreground" title={text}>
      <Icon className="h-3 w-3 shrink-0" />
      <span className="truncate">{text}</span>
    </p>
  );
}

function LiveMetricView({ metric }: { metric: LiveMetricDef }) {
  const at = useAT();
  const r = metric.useCount();
  const label = at(`metric.${metric.id}`, metric.label);
  // the hook's own retry: the host's re-reads analytics, which this count does not come from
  return (
    <WidgetFrame title={label}>
      <AnalyticsBody result={r} rows={1} isEmpty={() => false}>
        {(n) => (
          <div className="min-w-0">
            <p className="truncate text-3xl font-bold tracking-tight tabular-nums">{fmt.formatNumber(n)}</p>
            <p className="truncate text-[11px] text-muted-foreground">{at("metric.live", "Now, not limited to the period or project")}</p>
          </div>
        )}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

export function MetricView(props: AnalyticsViewProps) {
  const live = LIVE_METRICS.find((m) => m.id === props.settings.metric);
  // keyed: each live metric has its own hook, so switching between them remounts
  return live ? <LiveMetricView key={live.id} metric={live} /> : <AnalyticsMetricView {...props} />;
}

function AnalyticsMetricView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const query = readQuery(settings);
  const metric = METRICS.find((m) => m.id === settings.metric) ?? METRICS[0]!;
  const r = useAnalytics(query);
  const label = at(`metric.${metric.id}`, metric.label);
  return (
    <WidgetFrame title={label} query={query}>
      <AnalyticsBody result={r} retry={actions.retry} rows={1} isEmpty={() => false} incomplete={metric.incomplete}>
        {(m) => (
          <div className="min-w-0">
            <p className="truncate text-3xl font-bold tracking-tight tabular-nums">{fmt.formatNumber(metric.read(m))}</p>
            {metric.periodBound && metric.incomplete && metric.incomplete(m) !== false ? (
              // an undercounted current period would show a drop that is not there; no second request either
              <p className="truncate text-[11px] text-muted-foreground">{at("metric.noComparisonCurrent", "No comparison: this period is incomplete")}</p>
            ) : metric.periodBound ? (
              <Comparison query={query} metric={metric} current={metric.read(m)} />
            ) : (
              <p className="truncate text-[11px] text-muted-foreground">{at("metric.snapshot", "Now, not limited to the period")}</p>
            )}
          </div>
        )}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

// -- tasks-breakdown ----------------------------------------------------------------------------------

export type Dimension = "status" | "priority" | "assignee" | "project";

function breakdown(
  m: Metrics,
  dim: Dimension,
  ctx: { at: AT; priorityLabel: (p: string) => string; projectHref: (id: string) => string | undefined; actions: AnalyticsActions },
): Row[] {
  const tm = m.task_metrics;
  // The status split counts every task of the snapshot; assignee and project lists may be cut at 50 rows.
  const total = sum(Object.values(tm.by_status_category ?? {}));
  const other = ctx.at("other", "Other");
  switch (dim) {
    case "status":
      return topN(
        Object.entries(tm.by_status_category ?? {}).map(([k, v]) => ({ key: k, label: ctx.at(`status.${k}`, humanize(k)), value: v, color: statusColor(k) })),
        TOP_N,
        other,
      );
    case "priority":
      return topN(
        Object.entries(tm.by_priority ?? {}).map(([k, v]) => ({ key: k, label: ctx.priorityLabel(k), value: v, color: priorityColor(k) })),
        TOP_N,
        other,
      );
    case "assignee":
      return topN(
        (tm.by_assignee ?? []).map((a, i) => {
          const isAgent = a.assignee_type === "agent";
          return {
            key: a.assignee_id ?? `unassigned:${i}`,
            label: a.assignee_type === "unassigned" ? ctx.at("unassigned", "Unassigned") : a.name || ctx.at("unknown", "Unknown"),
            value: a.count,
            color: a.assignee_type === "unassigned" ? NEUTRAL : seriesColor(i),
            onOpen: isAgent ? agentOpener(ctx.actions, a.assignee_id) : undefined,
            suffix: isAgent && a.assignee_id ? <AgentShortTag id={a.assignee_id} /> : undefined,
          };
        }),
        TOP_N,
        other,
        total,
      );
    case "project":
      return topN(
        (tm.by_project ?? []).map((p, i) => ({ key: p.project_id, label: p.project_name, value: p.count, color: seriesColor(i), href: ctx.projectHref(p.project_id) })),
        TOP_N,
        other,
        total,
      );
  }
}

const DIMENSIONS: Dimension[] = ["status", "priority", "assignee", "project"];
const DIMENSION_TITLES: Record<Dimension, string> = {
  status: "Tasks by status",
  priority: "Tasks by priority",
  assignee: "Tasks by assignee",
  project: "Tasks by project",
};

export function TasksBreakdownView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const { t } = useTranslation("notifications");
  const projectHref = useProjectHref();
  const query = readQuery(settings);
  const r = useAnalytics(query);
  const dim = DIMENSIONS.find((d) => d === settings.dimension) ?? "status";
  const donut = settings.view === "donut";
  const title = at(`breakdown.${dim}`, DIMENSION_TITLES[dim]);
  const ctx = { at, priorityLabel: (p: string) => t(`dashboard.priority.${p}`, { defaultValue: p }), projectHref, actions };
  return (
    <WidgetFrame title={title} query={query}>
      <AnalyticsBody result={r} retry={actions.retry} rows={4} isEmpty={(m) => breakdown(m, dim, ctx).length === 0}>
        {(m) => (donut ? <Donut rows={breakdown(m, dim, ctx)} label={title} /> : <BarList rows={breakdown(m, dim, ctx)} />)}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

// -- tasks-timeline -----------------------------------------------------------------------------------

export function TasksTimelineView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const query = readQuery(settings);
  const r = useAnalytics(query);
  const title = at("timeline.title", "Created vs completed");
  return (
    <WidgetFrame title={title} query={query}>
      <AnalyticsBody result={r} retry={actions.retry} rows={4} isEmpty={(m) => (m.timeline ?? []).every((d) => d.created === 0 && d.completed === 0)}>
        {(m) => (
          <LineChart
            label={title}
            days={m.timeline.map((d) => d.date)}
            series={[
              { key: "created", label: at("timeline.created", "Created"), color: seriesColor(0), values: m.timeline.map((d) => d.created) },
              { key: "completed", label: at("timeline.completed", "Completed"), color: seriesColor(3), values: m.timeline.map((d) => d.completed) },
            ]}
          />
        )}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

// -- events-breakdown ---------------------------------------------------------------------------------

export function EventsBreakdownView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const query = readQuery(settings);
  const r = useAnalytics(query);
  const rows = (m: Metrics) =>
    topN(
      Object.entries(m.event_metrics.by_type ?? {}).map(([k, v], i) => ({ key: k, label: k, value: v, color: seriesColor(i) })),
      TOP_N,
      at("other", "Other"),
      m.event_metrics.total_events,
    );
  return (
    <WidgetFrame title={at("events.title", "Events by type")} query={query}>
      <AnalyticsBody result={r} retry={actions.retry} rows={4} isEmpty={(m) => rows(m).length === 0} incomplete={eventsIncomplete}>
        {(m) => <BarList rows={rows(m)} />}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

// -- agent-leaderboard --------------------------------------------------------------------------------

export function AgentLeaderboardView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const query = readQuery(settings);
  const r = useAnalytics(query);
  const rows = (m: Metrics): Row[] =>
    (m.agent_metrics.tasks_by_agent ?? [])
      .filter((a) => a.completed > 0)
      .sort((a, b) => b.completed - a.completed)
      .slice(0, 10)
      .map((a) => ({
        key: a.agent_id,
        label: a.agent_name,
        value: a.completed,
        color: seriesColor(3),
        onOpen: agentOpener(actions, a.agent_id),
        suffix: <AgentShortTag id={a.agent_id} />,
      }));
  return (
    <WidgetFrame title={at("leaderboard.title", "Completed by agent")} query={query}>
      <AnalyticsBody result={r} retry={actions.retry} rows={4} isEmpty={(m) => rows(m).length === 0}>
        {(m) => <BarList rows={rows(m)} rank />}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

// -- cost-kpis ----------------------------------------------------------------------------------------

/** Under every cost figure: the dollars are the API list price of the usage, not money spent. */
function CostBasisNote({ at }: { at: AT }) {
  return (
    <p className="text-[11px]">
      <ApiPriceNote
        label={at("cost.apiPrice", "at API list price")}
        hint={at("cost.apiPriceHint", "Notional cost at the API price list. A subscription spends window limits, not money")}
      />
    </p>
  );
}

export function CostKpisView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const query = readQuery(settings);
  const r = useAnalytics(query);
  return (
    <WidgetFrame title={at("cost.kpis", "Cost")} query={query}>
      <AnalyticsBody result={r} retry={actions.retry} rows={2} isEmpty={(m) => m.cost_metrics.session_count === 0} incomplete={(m) => costIncomplete(m.cost_metrics)}>
        {({ cost_metrics: c }) => (
          <div className="min-w-0 space-y-2">
            <div className="grid grid-cols-2 gap-2 @md:grid-cols-4">
              <Tile label={at("cost.reported", "Reported cost")} value={c.reported_session_count > 0 ? formatCost(c.total_cost) : "—"} />
              <Tile label={at("cost.tokens", "Tokens in / out")} value={`${formatTokens(c.total_tokens_in)} / ${formatTokens(c.total_tokens_out)}`} wrap />
              <Tile label={at("cost.sessions", "Sessions")} value={fmt.formatNumber(c.session_count)} />
              <Tile
                label={at("cost.avg", "Avg per session")}
                value={c.reported_session_count > 0 ? formatCost(c.total_cost / c.reported_session_count) : "—"}
              />
            </div>
            <p className="text-[11px] text-muted-foreground">
              {at("cost.reportedOf", "Reported {{reported}} of {{total}} sessions ({{percent}}%)", {
                reported: fmt.formatNumber(c.reported_session_count),
                total: fmt.formatNumber(c.session_count),
                percent: Math.round((c.reported_session_count / c.session_count) * 100),
              })}
            </p>
            <CostBasisNote at={at} />
          </div>
        )}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

// -- cost-by-agent / cost-by-project ------------------------------------------------------------------

export function CostByAgentView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const query = readQuery(settings);
  const r = useAnalytics(query);
  const rows = (c: Cost) =>
    topN(
      (c.by_agent ?? []).map((a, i) => ({
        key: a.agent_id,
        label: a.agent_name,
        value: a.cost,
        display: formatCost(a.cost),
        color: seriesColor(i),
        onOpen: agentOpener(actions, a.agent_id),
        suffix: <AgentShortTag id={a.agent_id} />,
      })),
      TOP_N,
      at("other", "Other"),
      undefined,
      formatCost,
    );
  return (
    <WidgetFrame title={at("cost.byAgent", "Cost by agent")} query={query}>
      <AnalyticsBody result={r} retry={actions.retry} rows={4} isEmpty={(m) => rows(m.cost_metrics).length === 0} incomplete={(m) => costIncomplete(m.cost_metrics)}>
        {(m) => (
          <div className="min-w-0 space-y-2">
            <BarList rows={rows(m.cost_metrics)} />
            <CostBasisNote at={at} />
          </div>
        )}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

export function CostByProjectView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const projectHref = useProjectHref();
  const query = readQuery(settings);
  const r = useAnalytics(query);
  const rows = (c: Cost) =>
    topN(
      (c.by_project ?? []).map((p, i) => ({
        key: p.project_id,
        label: p.project_name,
        value: p.cost,
        display: formatCost(p.cost),
        color: seriesColor(i),
        href: projectHref(p.project_id),
      })),
      TOP_N,
      at("other", "Other"),
      undefined,
      formatCost,
    );
  return (
    <WidgetFrame title={at("cost.byProject", "Cost by project")} query={query}>
      <AnalyticsBody result={r} retry={actions.retry} rows={4} isEmpty={(m) => rows(m.cost_metrics).length === 0} incomplete={(m) => costIncomplete(m.cost_metrics)}>
        {(m) => (
          <div className="min-w-0 space-y-2">
            <BarList rows={rows(m.cost_metrics)} />
            <CostBasisNote at={at} />
          </div>
        )}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

// -- cost-by-day --------------------------------------------------------------------------------------

export function CostByDayView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const query = readQuery(settings);
  const r = useAnalytics(query);
  const title = at("cost.byDay", "Cost by day");
  return (
    <WidgetFrame title={title} query={query}>
      <AnalyticsBody
        result={r}
        retry={actions.retry}
        rows={4}
        isEmpty={(m) => (m.cost_metrics.by_day ?? []).every((d) => d.cost === 0)}
        incomplete={(m) => costIncomplete(m.cost_metrics)}
      >
        {({ cost_metrics: c }) => (
          <div className="min-w-0">
            <ColumnChart label={title} days={c.by_day.map((d) => d.date)} values={c.by_day.map((d) => d.cost)} color={seriesColor(0)} format={formatCost} />
            <p className="mt-1 text-center text-xs text-muted-foreground">
              {at("cost.total", "Total {{cost}}", { cost: formatCost(sum(c.by_day.map((d) => d.cost))) })}{" "}
              <ApiPriceNote
                label={at("cost.apiPrice", "at API list price")}
                hint={at("cost.apiPriceHint", "Notional cost at the API price list. A subscription spends window limits, not money")}
              />
            </p>
          </div>
        )}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

// -- cost-top-tasks -----------------------------------------------------------------------------------

export type TopTasksSort = "cost" | "tokens";
type TopTask = Cost["top_tasks"][number];
const tokensOf = (t: TopTask) => t.tokens_in + t.tokens_out;

/** Rows in the chosen order; the server already ranks by it, this keeps the order whatever it sends. */
export function sortTopTasks(rows: readonly TopTask[], by: TopTasksSort): TopTask[] {
  const key = by === "tokens" ? tokensOf : (t: TopTask) => t.cost;
  return [...rows].sort((a, b) => key(b) - key(a));
}

export function CostTopTasksView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const sort: TopTasksSort = settings.sort === "tokens" ? "tokens" : "cost";
  // The sort is a server parameter (which ten tasks), so it is part of this widget's filter.
  const query: AnalyticsQuery = { ...readQuery(settings), ...(sort === "tokens" ? { topTasksSort: "tokens" as const } : {}) };
  const r = useAnalytics(query);
  return (
    <WidgetFrame title={sort === "tokens" ? at("top.titleTokens", "Top tasks by tokens") : at("top.titleCost", "Most expensive tasks")} query={query}>
      <AnalyticsBody result={r} retry={actions.retry} rows={5} isEmpty={(m) => (m.cost_metrics.top_tasks ?? []).length === 0} incomplete={(m) => costIncomplete(m.cost_metrics)}>
        {(m) => (
          <div className="min-w-0 space-y-2">
          <table className="w-full table-fixed text-xs">
            <thead>
              <tr className="text-left text-[11px] text-muted-foreground">
                <th className="pb-1 font-normal">{at("top.task", "Task")}</th>
                <th className="w-16 pb-1 text-right font-normal">{at("top.cost", "Cost")}</th>
                <th className="w-14 pb-1 text-right font-normal">{at("top.tokens", "Tokens")}</th>
                <th className="w-10 pb-1 text-right font-normal" title={at("top.sessions", "Sessions")}>
                  {at("top.sessionsShort", "Sess.")}
                </th>
              </tr>
            </thead>
            <tbody>
              {sortTopTasks(m.cost_metrics.top_tasks, sort).map((row) => {
                const title = row.task_title || `${row.task_id.slice(0, 8)}…`;
                return (
                  <tr key={row.task_id} data-row={row.task_id} className="border-t border-border/50">
                    <td className="truncate py-1 pr-2" title={title}>
                      {/* TaskLink opens the task through the host's openTask; a modified click is left to the browser. */}
                      <TaskLink taskId={row.task_id} className="block truncate hover:underline">
                        {title}
                      </TaskLink>
                    </td>
                    <td className="py-1 text-right tabular-nums">{formatCost(row.cost)}</td>
                    <td className="py-1 text-right tabular-nums">{formatTokens(tokensOf(row))}</td>
                    <td className="py-1 text-right tabular-nums">{fmt.formatNumber(row.session_count)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <CostBasisNote at={at} />
          </div>
        )}
      </AnalyticsBody>
    </WidgetFrame>
  );
}

/** The group's views by descriptor id: this file is the lazy chunk the registry loads on first draw. */
export const views = {
  metric: MetricView,
  "tasks-breakdown": TasksBreakdownView,
  "tasks-timeline": TasksTimelineView,
  "events-breakdown": EventsBreakdownView,
  "agent-leaderboard": AgentLeaderboardView,
  "cost-kpis": CostKpisView,
  "cost-by-agent": CostByAgentView,
  "cost-top-tasks": CostTopTasksView,
  "cost-by-project": CostByProjectView,
  "cost-by-day": CostByDayView,
  "memory-usage": MemoryUsageView,
} as const;
