// The metric widget's choices. Their ids are the server's (catalog.go metricIDs, copied into core-widgets.json
// `metrics`): PUT layout refuses any other, and a vitest holds METRIC_OPTIONS to that copy. A data file: index.ts builds the `metric` setting from it, so it holds only
// `import type` (registry contract C) and no hook, view or React; the views add the live counts' hooks.
import type { AnalyticsMetrics } from "@/types";

type Metrics = AnalyticsMetrics;

/** False when the event log covers the whole period; otherwise the date it is kept from. */
export const eventsIncomplete = (m: Metrics) => (m.event_metrics.period_fully_covered ? false : m.event_metrics.retained_since);

export interface MetricDef {
  id: string;
  label: string;
  read: (m: Metrics) => number;
  /** Counted within the period, so it has a previous period to compare with (a snapshot has not). */
  periodBound: boolean;
  incomplete?: (m: Metrics) => false | string | null;
}

export const METRICS: MetricDef[] = [
  { id: "tasks_closed", label: "Completed", read: (m) => m.task_metrics.completed_this_period, periodBound: true },
  { id: "tasks_created", label: "Created", read: (m) => m.task_metrics.created_this_period, periodBound: true },
  { id: "events_total", label: "Events", read: (m) => m.event_metrics.total_events, periodBound: true, incomplete: eventsIncomplete },
  { id: "tasks_total", label: "Total tasks", read: (m) => m.task_metrics.total, periodBound: false },
  { id: "tasks_overdue", label: "Overdue", read: (m) => m.task_metrics.overdue ?? 0, periodBound: false },
  { id: "agents_total", label: "Agents", read: (m) => m.agent_metrics.total_agents, periodBound: false },
  { id: "agents_active", label: "Active agents", read: (m) => m.agent_metrics.active_agents, periodBound: false },
];

export type LiveMetricId = "triage_waiting" | "mentions_unseen";

/** Counts that live outside analytics (the moment's queue, their own endpoint); views.tsx gives each its hook. */
export const LIVE_METRIC_CHOICES: { id: LiveMetricId; label: string }[] = [
  { id: "triage_waiting", label: "Awaiting triage" },
  { id: "mentions_unseen", label: "Unread mentions" },
];

/** Every metric the widget offers, in the order of its setting. */
export const METRIC_OPTIONS: { id: string; label: string }[] = [...METRICS, ...LIVE_METRIC_CHOICES].map(({ id, label }) => ({ id, label }));
