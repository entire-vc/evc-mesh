import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router";
import { CalendarDays, Download, Printer, TrendingUp, Users, Zap } from "lucide-react";
import { api, getAccessToken } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import type { AnalyticsMetrics } from "@/types";

// ---------------------------------------------------------------------------
// Palette for charts
// ---------------------------------------------------------------------------

// Colors use Tailwind utility classes applied via className; these CSS-variable-
// based values are safe in both light and dark mode.
const STATUS_COLORS: Record<string, string> = {
  backlog: "hsl(var(--muted-foreground))",
  todo: "hsl(var(--chart-1, 210 100% 66%))",
  in_progress: "hsl(var(--chart-2, 38 92% 50%))",
  review: "hsl(var(--chart-3, 265 83% 73%))",
  done: "hsl(var(--chart-4, 158 64% 52%))",
  cancelled: "hsl(var(--destructive))",
};

const PRIORITY_COLORS: Record<string, string> = {
  urgent: "hsl(var(--destructive))",
  high: "hsl(var(--chart-2, 24 95% 53%))",
  medium: "hsl(var(--chart-5, 45 93% 47%))",
  low: "hsl(var(--chart-4, 142 71% 45%))",
  none: "hsl(var(--muted-foreground))",
};

// ---------------------------------------------------------------------------
// SVG bar chart
// ---------------------------------------------------------------------------

interface BarChartProps {
  data: Array<{ label: string; value: number; color?: string }>;
  maxWidth?: number;
  height?: number;
}

function HorizontalBarChart({ data, height = 20 }: BarChartProps) {
  const max = Math.max(...data.map((d) => d.value), 1);
  return (
    <div className="space-y-2">
      {data.map(({ label, value, color }) => (
        <div key={label} className="flex items-center gap-2">
          <span className="w-24 shrink-0 text-right text-xs capitalize text-muted-foreground">
            {label.replace("_", " ")}
          </span>
          <div className="relative flex-1" style={{ height }}>
            <div
              className="absolute inset-y-0 left-0 rounded-sm"
              style={{
                width: `${(value / max) * 100}%`,
                backgroundColor: color ?? "hsl(var(--primary))",
                minWidth: value > 0 ? 4 : 0,
              }}
            />
          </div>
          <span className="w-8 text-right text-xs font-medium">{value}</span>
        </div>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Timeline chart (simple SVG area)
// ---------------------------------------------------------------------------

interface TimelineChartProps {
  data: Array<{ date: string; created: number; completed: number }>;
}

function TimelineChart({ data }: TimelineChartProps) {
  if (data.length === 0) {
    return (
      <p className="py-8 text-center text-xs text-muted-foreground">
        No data in selected period.
      </p>
    );
  }

  const W = 600;
  const H = 140;
  const PAD = { top: 8, right: 16, bottom: 24, left: 28 };
  const chartW = W - PAD.left - PAD.right;
  const chartH = H - PAD.top - PAD.bottom;

  const maxVal = Math.max(...data.flatMap((d) => [d.created, d.completed]), 1);
  const xStep = data.length > 1 ? chartW / (data.length - 1) : chartW;

  const toPath = (key: "created" | "completed") => {
    const pts = data.map((d, i) => {
      const x = PAD.left + i * xStep;
      const y = PAD.top + chartH - (d[key] / maxVal) * chartH;
      return `${x},${y}`;
    });
    return `M ${pts.join(" L ")}`;
  };

  const toArea = (key: "created" | "completed") => {
    const pts = data.map((d, i) => {
      const x = PAD.left + i * xStep;
      const y = PAD.top + chartH - (d[key] / maxVal) * chartH;
      return `${x},${y}`;
    });
    const last = PAD.left + (data.length - 1) * xStep;
    const baseline = PAD.top + chartH;
    return `M ${PAD.left},${baseline} L ${pts.join(" L ")} L ${last},${baseline} Z`;
  };

  // X-axis labels — show at most 8 ticks
  const tickInterval = Math.max(1, Math.ceil(data.length / 8));
  const ticks = data
    .map((d, i) => ({ x: PAD.left + i * xStep, label: d.date.slice(5), i }))
    .filter((t) => t.i % tickInterval === 0);

  return (
    <div className="overflow-x-auto">
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full min-w-[280px]">
        {/* Y gridlines */}
        {[0, 0.25, 0.5, 0.75, 1].map((frac) => {
          const y = PAD.top + chartH - frac * chartH;
          return (
            <line
              key={frac}
              x1={PAD.left}
              x2={PAD.left + chartW}
              y1={y}
              y2={y}
              stroke="currentColor"
              strokeOpacity={0.1}
              strokeWidth={1}
            />
          );
        })}

        {/* Area fills */}
        <path d={toArea("created")} fill="hsl(var(--primary))" fillOpacity={0.15} />
        <path d={toArea("completed")} fill="hsl(var(--chart-3, 265 83% 73%))" fillOpacity={0.15} />

        {/* Lines */}
        <path
          d={toPath("created")}
          fill="none"
          stroke="hsl(var(--primary))"
          strokeWidth={1.5}
          strokeLinejoin="round"
          strokeLinecap="round"
        />
        <path
          d={toPath("completed")}
          fill="none"
          stroke="hsl(var(--chart-3, 265 83% 73%))"
          strokeWidth={1.5}
          strokeLinejoin="round"
          strokeLinecap="round"
        />

        {/* X axis */}
        <line
          x1={PAD.left}
          x2={PAD.left + chartW}
          y1={PAD.top + chartH}
          y2={PAD.top + chartH}
          stroke="currentColor"
          strokeOpacity={0.2}
        />

        {/* X ticks */}
        {ticks.map(({ x, label }) => (
          <text
            key={label}
            x={x}
            y={H - 4}
            textAnchor="middle"
            fontSize={8}
            fill="currentColor"
            fillOpacity={0.5}
          >
            {label}
          </text>
        ))}
      </svg>

      {/* Legend */}
      <div className="mt-1 flex gap-4 justify-center">
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <span className="inline-block h-2 w-4 rounded-sm bg-teal-400" />
          Created
        </div>
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <span className="inline-block h-2 w-4 rounded-sm bg-violet-400" />
          Completed
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

const PRESET_RANGES = [
  { label: "Last 7 days", days: 7 },
  { label: "Last 30 days", days: 30 },
  { label: "Last 90 days", days: 90 },
];

// ---------------------------------------------------------------------------
// CSV download helper
// ---------------------------------------------------------------------------

async function downloadCSV(url: string, filename: string): Promise<void> {
  const token = getAccessToken();
  const headers: Record<string, string> = {};
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }

  const res = await fetch(url, { headers });
  if (!res.ok) {
    throw new Error(`Export failed: ${res.statusText}`);
  }

  const blob = await res.blob();
  const objectUrl = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = objectUrl;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  document.body.removeChild(anchor);
  URL.revokeObjectURL(objectUrl);
}

export function AnalyticsPage() {
  useParams();
  const { currentWorkspace } = useWorkspaceStore();
  const { projects } = useProjectStore();

  const [metrics, setMetrics] = useState<AnalyticsMetrics | null>(null);
  const [loading, setLoading] = useState(true);
  const [rangeDays, setRangeDays] = useState(30);
  const [projectFilter, setProjectFilter] = useState<string>("");
  const [exporting, setExporting] = useState(false);

  // Keep query param values in a ref so the export callback can read them
  // without becoming a dependency of fetchMetrics.
  const exportParamsRef = useRef<{ from: string; to: string; project_id?: string }>({
    from: "",
    to: "",
  });

  const fetchMetrics = useCallback(async () => {
    if (!currentWorkspace) return;
    setLoading(true);
    try {
      const to = new Date();
      const from = new Date();
      from.setDate(from.getDate() - rangeDays);

      const params: Record<string, string> = {
        from: from.toISOString().slice(0, 10),
        to: to.toISOString().slice(0, 10),
      };
      if (projectFilter) params.project_id = projectFilter;

      exportParamsRef.current = {
        from: params.from ?? "",
        to: params.to ?? "",
        project_id: projectFilter || undefined,
      };

      const qs = new URLSearchParams(params).toString();
      const data = await api<AnalyticsMetrics>(
        `/api/v1/workspaces/${currentWorkspace.id}/analytics?${qs}`,
      );
      setMetrics(data);
    } catch {
      setMetrics(null);
    } finally {
      setLoading(false);
    }
  }, [currentWorkspace, rangeDays, projectFilter]);

  useEffect(() => {
    void fetchMetrics();
  }, [fetchMetrics]);

  const handleExportCSV = useCallback(async () => {
    if (!currentWorkspace) return;
    setExporting(true);
    try {
      const p = exportParamsRef.current;
      const qs = new URLSearchParams({
        format: "csv",
        from: p.from,
        to: p.to,
        ...(p.project_id ? { project_id: p.project_id } : {}),
      }).toString();
      const url = `/api/v1/workspaces/${currentWorkspace.id}/analytics/export?${qs}`;
      const filename = `analytics-${p.from || new Date().toISOString().slice(0, 10)}.csv`;
      await downloadCSV(url, filename);
    } catch (err) {
      console.error("CSV export failed:", err);
    } finally {
      setExporting(false);
    }
  }, [currentWorkspace]);

  const statusData = metrics
    ? Object.entries(metrics.task_metrics.by_status_category ?? {}).map(
        ([label, value]) => ({ label, value, color: STATUS_COLORS[label] }),
      )
    : [];

  const priorityData = metrics
    ? Object.entries(metrics.task_metrics.by_priority ?? {}).map(([label, value]) => ({
        label,
        value,
        color: PRIORITY_COLORS[label],
      }))
    : [];

  return (
    <div className="flex h-full flex-col">
      {/* Toolbar */}
      <div className="flex items-center justify-end gap-3 pb-4">
        <Select
          value={projectFilter}
          onChange={(e) => setProjectFilter(e.target.value)}
          className="w-48"
        >
          <option value="">All Projects</option>
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </Select>
        <Select
          value={String(rangeDays)}
          onChange={(e) => setRangeDays(Number(e.target.value))}
          className="w-40"
        >
          {PRESET_RANGES.map(({ label, days }) => (
            <option key={days} value={days}>
              {label}
            </option>
          ))}
        </Select>

        {/* Export buttons */}
        <button
          type="button"
          onClick={() => void handleExportCSV()}
          disabled={exporting || loading}
          className="inline-flex items-center gap-1.5 rounded-md border border-input bg-background px-3 py-1.5 text-xs font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50"
          title="Export data as CSV"
        >
          <Download className="h-3.5 w-3.5" />
          {exporting ? "Exporting..." : "Export CSV"}
        </button>
        <button
          type="button"
          onClick={() => window.print()}
          className="inline-flex items-center gap-1.5 rounded-md border border-input bg-background px-3 py-1.5 text-xs font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground"
          title="Print or save as PDF"
        >
          <Printer className="h-3.5 w-3.5" />
          Print / PDF
        </button>
      </div>

      <div className="flex-1 overflow-y-auto space-y-6">
        {/* KPI row */}
        {loading ? (
          <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-24 rounded-xl" />
            ))}
          </div>
        ) : metrics ? (
          <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
            <KPICard
              icon={<TrendingUp className="h-5 w-5 text-teal-500" />}
              label="Total Tasks"
              value={metrics.task_metrics.total}
            />
            <KPICard
              icon={<CalendarDays className="h-5 w-5 text-blue-500" />}
              label="Created (period)"
              value={metrics.task_metrics.created_this_period}
            />
            <KPICard
              icon={<TrendingUp className="h-5 w-5 text-emerald-500" />}
              label="Completed (period)"
              value={metrics.task_metrics.completed_this_period}
            />
            <KPICard
              icon={<Zap className="h-5 w-5 text-amber-500" />}
              label="Total Events"
              value={metrics.event_metrics.total_events}
            />
          </div>
        ) : null}

        {/* Charts row */}
        <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="text-sm">Tasks by Status</CardTitle>
            </CardHeader>
            <CardContent>
              {loading ? (
                <Skeleton className="h-32 w-full" />
              ) : (
                <HorizontalBarChart data={statusData} />
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="text-sm">Tasks by Priority</CardTitle>
            </CardHeader>
            <CardContent>
              {loading ? (
                <Skeleton className="h-32 w-full" />
              ) : (
                <HorizontalBarChart data={priorityData} />
              )}
            </CardContent>
          </Card>
        </div>

        {/* Activity timeline */}
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-sm">Activity Timeline</CardTitle>
          </CardHeader>
          <CardContent>
            {loading ? (
              <Skeleton className="h-40 w-full" />
            ) : (
              <TimelineChart data={metrics?.timeline ?? []} />
            )}
          </CardContent>
        </Card>

        {/* Agent leaderboard */}
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-sm">
              <Users className="h-4 w-4" />
              Agent Leaderboard
            </CardTitle>
          </CardHeader>
          <CardContent>
            {loading ? (
              <Skeleton className="h-24 w-full" />
            ) : metrics && (metrics.agent_metrics.tasks_by_agent ?? []).length > 0 ? (
              <div className="space-y-1">
                <div className="grid grid-cols-3 text-xs font-medium text-muted-foreground border-b pb-2 mb-2">
                  <span className="col-span-2">Agent</span>
                  <span className="text-right">Completed</span>
                </div>
                {(metrics.agent_metrics.tasks_by_agent ?? []).map((row, idx) => (
                  <div
                    key={row.agent_id}
                    className="grid grid-cols-3 items-center py-1.5 text-sm"
                  >
                    <span className="col-span-2 flex items-center gap-2 truncate">
                      <span className="w-5 shrink-0 text-xs text-muted-foreground">
                        {idx + 1}.
                      </span>
                      {row.agent_name}
                    </span>
                    <span className="text-right font-medium">{row.completed}</span>
                  </div>
                ))}
              </div>
            ) : !loading ? (
              <p className="text-center text-xs text-muted-foreground py-4">
                No agent task data yet.
              </p>
            ) : null}
          </CardContent>
        </Card>

        {/* Agent counts + Event breakdown */}
        <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="text-sm">Agents</CardTitle>
            </CardHeader>
            <CardContent>
              {loading ? (
                <Skeleton className="h-16 w-full" />
              ) : metrics ? (
                <div className="flex gap-8">
                  <Stat label="Total" value={metrics.agent_metrics.total_agents} />
                  <Separator orientation="vertical" className="h-12" />
                  <Stat label="Active" value={metrics.agent_metrics.active_agents} />
                </div>
              ) : null}
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="text-sm">Events by Type</CardTitle>
            </CardHeader>
            <CardContent>
              {loading ? (
                <Skeleton className="h-24 w-full" />
              ) : metrics &&
                Object.keys(metrics.event_metrics.by_type ?? {}).length > 0 ? (
                <HorizontalBarChart
                  data={Object.entries(metrics.event_metrics.by_type ?? {}).map(
                    ([label, value]) => ({ label, value }),
                  )}
                />
              ) : (
                <p className="text-center text-xs text-muted-foreground py-4">
                  No events yet.
                </p>
              )}
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

function KPICard({
  icon,
  label,
  value,
}: {
  icon: React.ReactNode;
  label: string;
  value: number;
}) {
  return (
    <Card>
      <CardContent className="flex flex-col gap-2 p-4">
        <div className="flex items-center gap-2">
          {icon}
          <span className="text-xs text-muted-foreground">{label}</span>
        </div>
        <span className="text-3xl font-bold tracking-tight">{value.toLocaleString()}</span>
      </CardContent>
    </Card>
  );
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-2xl font-bold">{value}</span>
    </div>
  );
}
