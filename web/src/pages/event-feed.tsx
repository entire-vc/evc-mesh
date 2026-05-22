import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import {
  Activity,
  AlertTriangle,
  ArrowDownCircle,
  Bot,
  CheckCircle2,
  ChevronRight,
  Circle,
  Filter,
  Info,
  Radio,
  RefreshCw,
  RotateCcw,
  Zap,
} from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { useProjectStore } from "@/stores/project";
import { useWebSocketStore } from "@/stores/websocket";
import { useWebSocket } from "@/hooks/use-websocket";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import type {
  EventBusMessage,
  EventType,
  PaginatedResponse,
  WSMessage,
} from "@/types";

// ---------------------------------------------------------------------------
// Event type display config
// ---------------------------------------------------------------------------

const EVENT_TYPE_CONFIG: Record<
  EventType,
  { label: string; color: string; icon: typeof Circle }
> = {
  summary: { label: "Summary", color: "text-emerald-500", icon: CheckCircle2 },
  status_change: {
    label: "Status Change",
    color: "text-blue-500",
    icon: RefreshCw,
  },
  context_update: {
    label: "Context Update",
    color: "text-violet-500",
    icon: Info,
  },
  error: {
    label: "Error",
    color: "text-red-500",
    icon: AlertTriangle,
  },
  dependency_resolved: {
    label: "Dependency Resolved",
    color: "text-amber-500",
    icon: ArrowDownCircle,
  },
  custom: { label: "Custom", color: "text-gray-500", icon: Zap },
};

const ALL_EVENT_TYPES = Object.keys(EVENT_TYPE_CONFIG) as EventType[];

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatTime(iso: string): string {
  return new Date(iso).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function getStatusDelta(
  payload: Record<string, unknown>,
): { from: string; to: string } | null {
  const status = payload.status as
    | { old?: string; new?: string }
    | undefined;
  if (status?.old && status?.new) {
    return { from: status.old, to: status.new };
  }
  return null;
}

function getActionVerb(eventType: EventType, subject: string): string {
  if (subject === "task.moved") return "moved";
  if (subject === "task.assigned") return "assigned";
  if (subject === "task.created") return "created";
  if (subject === "task.deleted") return "deleted";
  if (subject === "task.updated") return "updated";
  switch (eventType) {
    case "status_change":
      return "updated";
    case "summary":
      return "summarised";
    case "context_update":
      return "updated context on";
    case "error":
      return "reported error on";
    case "dependency_resolved":
      return "resolved dependency on";
    default:
      return "published";
  }
}

// ---------------------------------------------------------------------------
// Event row component (historical, enriched)
// ---------------------------------------------------------------------------

interface EventRowProps {
  event: EventBusMessage;
}

function EventRow({ event }: EventRowProps) {
  const config = EVENT_TYPE_CONFIG[event.event_type] ?? EVENT_TYPE_CONFIG.custom;
  const Icon = config.icon;

  const actorName =
    event.actor_name ??
    (event.agent_id ? event.agent_id.slice(0, 8) : "System");
  const isAgent = !!event.agent_id;
  const taskTitle = event.task_title;
  const taskShort = event.task_id?.slice(0, 8);
  const projectName = event.project_name;
  const delta = getStatusDelta(event.payload);
  const verb = getActionVerb(event.event_type, event.subject);

  return (
    <div className="flex items-start gap-3 rounded-lg border border-border bg-card p-3 transition-colors hover:bg-muted/30">
      <div className={cn("mt-0.5 shrink-0", config.color)}>
        <Icon className="h-4 w-4" />
      </div>

      <div className="min-w-0 flex-1">
        {/* Main line */}
        <div className="flex flex-wrap items-center gap-1.5 text-sm">
          <span className="font-medium text-foreground">{actorName}</span>
          {isAgent && <Bot className="h-3 w-3 text-muted-foreground" />}
          <span className="text-muted-foreground">{verb}</span>
          {event.task_id ? (
            <Link
              to={`/t/${event.task_id}`}
              className="font-medium text-foreground hover:underline truncate max-w-[260px]"
            >
              {taskTitle ?? event.task_id.slice(0, 8)}
            </Link>
          ) : (
            <span className="truncate text-muted-foreground italic max-w-[260px]">
              {event.subject}
            </span>
          )}
          {delta && (
            <span className="flex items-center gap-1 text-xs">
              <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                {delta.from}
              </Badge>
              <ChevronRight className="h-3 w-3 text-muted-foreground" />
              <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                {delta.to}
              </Badge>
            </span>
          )}
        </div>

        {/* Meta line */}
        <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
          {projectName && (
            <span className="font-medium text-muted-foreground">{projectName}</span>
          )}
          {projectName && taskShort && <span>&middot;</span>}
          {taskShort && <span className="font-mono">{taskShort}</span>}
          {(projectName || taskShort) && <span>&middot;</span>}
          <time>{formatTime(event.created_at)}</time>
          {event.tags.length > 0 && (
            <>
              <span>&middot;</span>
              {event.tags.slice(0, 2).map((tag) => (
                <Badge key={tag} variant="secondary" className="text-[10px]">
                  {tag}
                </Badge>
              ))}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Real-time event row (from WebSocket)
// ---------------------------------------------------------------------------

interface RealtimeEventRowProps {
  event: WSMessage;
}

function RealtimeEventRow({ event }: RealtimeEventRowProps) {
  const eventType = event.type as EventType;
  const config = EVENT_TYPE_CONFIG[eventType] ?? EVENT_TYPE_CONFIG.custom;
  const Icon = config.icon;
  const data = event.data;

  const timeStr = formatTime(event.timestamp);
  const agentName =
    data.agent_id
      ? (data.agent_name as string) || (data.agent_id as string)?.slice(0, 8)
      : "System";
  const isAgent = !!data.agent_id;
  const tags = (data.tags as string[]) || [];
  const taskTitle = data.task_title as string | undefined;
  const taskId = data.task_id as string | undefined;
  const projectName = data.project_name as string | undefined;
  const delta = getStatusDelta(data as Record<string, unknown>);
  const subject = (data.subject as string) || event.type;

  return (
    <div className="flex items-start gap-3 rounded-lg border border-primary/20 bg-primary/5 p-3 transition-colors">
      <div className={cn("mt-0.5 shrink-0", config.color)}>
        <Icon className="h-4 w-4" />
      </div>

      <div className="min-w-0 flex-1">
        {/* Main line */}
        <div className="flex flex-wrap items-center gap-1.5 text-sm">
          <Radio className="h-3 w-3 shrink-0 animate-pulse text-primary" />
          <span className="font-medium text-foreground">{agentName}</span>
          {isAgent && <Bot className="h-3 w-3 text-muted-foreground" />}
          {taskId ? (
            <Link
              to={`/t/${taskId}`}
              className="font-medium text-foreground hover:underline truncate max-w-[260px]"
            >
              {taskTitle ?? taskId.slice(0, 8)}
            </Link>
          ) : (
            <span className="truncate text-muted-foreground italic max-w-[260px]">
              {subject}
            </span>
          )}
          {delta && (
            <span className="flex items-center gap-1 text-xs">
              <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                {delta.from}
              </Badge>
              <ChevronRight className="h-3 w-3 text-muted-foreground" />
              <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                {delta.to}
              </Badge>
            </span>
          )}
        </div>

        {/* Meta line */}
        <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
          {projectName && (
            <span className="font-medium text-muted-foreground">{projectName}</span>
          )}
          {projectName && taskId && <span>&middot;</span>}
          {taskId && <span className="font-mono">{taskId.slice(0, 8)}</span>}
          <span>&middot;</span>
          <time>{timeStr}</time>
          {tags.length > 0 && (
            <>
              <span>&middot;</span>
              {tags.slice(0, 2).map((tag) => (
                <Badge key={tag} variant="secondary" className="text-[10px]">
                  {tag}
                </Badge>
              ))}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Filter state
// ---------------------------------------------------------------------------

interface FilterState {
  eventTypes: EventType[];
  actorType: "all" | "agent" | "system";
  projectId: string;
  dateFrom: string;
  dateTo: string;
}

const DEFAULT_FILTERS: FilterState = {
  eventTypes: [],
  actorType: "all",
  projectId: "all",
  dateFrom: "",
  dateTo: "",
};

function isDefaultFilters(f: FilterState): boolean {
  return (
    f.eventTypes.length === 0 &&
    f.actorType === "all" &&
    f.projectId === "all" &&
    f.dateFrom === "" &&
    f.dateTo === ""
  );
}

// ---------------------------------------------------------------------------
// Event Feed page
// ---------------------------------------------------------------------------

export function EventFeedPage() {
  const { wsSlug } = useParams();
  const { projects } = useProjectStore();
  const eventLog = useWebSocketStore((s) => s.eventLog);
  const isConnected = useWebSocketStore((s) => s.isConnected);

  const [filters, setFilters] = useState<FilterState>(DEFAULT_FILTERS);
  const [autoScroll, setAutoScroll] = useState(true);
  const [historicalEvents, setHistoricalEvents] = useState<EventBusMessage[]>([]);
  const [isLoading, setIsLoading] = useState(false);

  const scrollContainerRef = useRef<HTMLDivElement>(null);

  useWebSocket({ workspaceSlug: wsSlug });

  function buildParams(f: FilterState): Record<string, string> {
    const p: Record<string, string> = { per_page: "100" };
    // Single event_type goes to server; multi-type handled client-side.
    if (f.eventTypes.length === 1) p.event_type = f.eventTypes[0]!;
    if (f.actorType === "agent") p.actor_type = "agent";
    if (f.dateFrom) p.date_from = new Date(f.dateFrom).toISOString();
    if (f.dateTo) {
      const d = new Date(f.dateTo);
      d.setHours(23, 59, 59, 999);
      p.date_to = d.toISOString();
    }
    return p;
  }

  const fetchHistoricalEvents = useCallback(
    async (f: FilterState) => {
      if (f.projectId === "all") {
        if (projects.length === 0) return;
        setIsLoading(true);
        try {
          const allEvents: EventBusMessage[] = [];
          const params = buildParams(f);
          for (const project of projects.slice(0, 5)) {
            try {
              const data = await api<PaginatedResponse<EventBusMessage>>(
                `/api/v1/projects/${project.id}/events`,
                { params },
              );
              allEvents.push(...data.items);
            } catch {
              // Skip projects with errors.
            }
          }
          allEvents.sort(
            (a, b) =>
              new Date(b.created_at).getTime() -
              new Date(a.created_at).getTime(),
          );
          setHistoricalEvents(allEvents.slice(0, 200));
        } finally {
          setIsLoading(false);
        }
      } else {
        setIsLoading(true);
        try {
          const data = await api<PaginatedResponse<EventBusMessage>>(
            `/api/v1/projects/${f.projectId}/events`,
            { params: buildParams(f) },
          );
          setHistoricalEvents(data.items);
        } catch {
          setHistoricalEvents([]);
        } finally {
          setIsLoading(false);
        }
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [projects],
  );

  // Re-fetch when server-side filter params change.
  useEffect(() => {
    fetchHistoricalEvents(filters);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters.projectId, filters.actorType, filters.dateFrom, filters.dateTo, projects]);

  useEffect(() => {
    if (autoScroll && scrollContainerRef.current) {
      scrollContainerRef.current.scrollTop = 0;
    }
  }, [eventLog.length, autoScroll]);

  // Client-side multi-type filter for historical events.
  const filteredHistoricalEvents = useMemo(() => {
    if (filters.eventTypes.length <= 1) return historicalEvents;
    return historicalEvents.filter((e) =>
      filters.eventTypes.includes(e.event_type),
    );
  }, [historicalEvents, filters.eventTypes]);

  // Real-time events: filter by type, actor, project.
  const filteredRealtimeEvents = useMemo(() => {
    return eventLog.filter((event) => {
      if (
        filters.eventTypes.length > 0 &&
        !filters.eventTypes.includes(event.type as EventType)
      )
        return false;
      if (filters.actorType === "agent" && !event.data.agent_id) return false;
      if (filters.actorType === "system" && event.data.agent_id) return false;
      if (filters.projectId !== "all") {
        const d = event.data as Record<string, unknown>;
        if (d.project_id && d.project_id !== filters.projectId) return false;
      }
      return true;
    });
  }, [eventLog, filters]);

  function toggleEventType(type: EventType) {
    setFilters((prev) => {
      const has = prev.eventTypes.includes(type);
      return {
        ...prev,
        eventTypes: has
          ? prev.eventTypes.filter((t) => t !== type)
          : [...prev.eventTypes, type],
      };
    });
  }

  function resetFilters() {
    setFilters(DEFAULT_FILTERS);
  }

  const hasActiveFilters = !isDefaultFilters(filters);
  const totalVisible =
    filteredRealtimeEvents.length + filteredHistoricalEvents.length;

  return (
    <div className="space-y-4">
      {/* Toolbar */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          {isConnected ? (
            <Badge variant="success" className="gap-1">
              <Radio className="h-3 w-3 animate-pulse" />
              Live
            </Badge>
          ) : (
            <Badge variant="secondary" className="gap-1">
              <Circle className="h-3 w-3" />
              Offline
            </Badge>
          )}
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant={autoScroll ? "default" : "outline"}
            size="sm"
            onClick={() => setAutoScroll((prev) => !prev)}
          >
            Auto-scroll {autoScroll ? "ON" : "OFF"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => fetchHistoricalEvents(filters)}
            disabled={isLoading}
          >
            <RefreshCw
              className={cn("mr-1 h-3 w-3", isLoading && "animate-spin")}
            />
            Refresh
          </Button>
        </div>
      </div>

      {/* Filter bar */}
      <div className="space-y-2 rounded-lg border border-border bg-card p-3">
        <div className="flex items-center gap-2">
          <Filter className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
            Filters
          </span>
          {hasActiveFilters && (
            <Button
              variant="ghost"
              size="sm"
              className="ml-auto h-6 px-2 text-xs"
              onClick={resetFilters}
            >
              <RotateCcw className="mr-1 h-3 w-3" />
              Reset
            </Button>
          )}
        </div>

        {/* Event type multi-select chips */}
        <div className="flex flex-wrap gap-1.5">
          {ALL_EVENT_TYPES.map((type) => {
            const cfg = EVENT_TYPE_CONFIG[type];
            const Icon = cfg.icon;
            const selected = filters.eventTypes.includes(type);
            return (
              <button
                key={type}
                onClick={() => toggleEventType(type)}
                className={cn(
                  "flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                  selected
                    ? "border-transparent bg-foreground text-background"
                    : "border-border bg-background text-muted-foreground hover:border-foreground/30 hover:text-foreground",
                )}
              >
                <Icon
                  className={cn(
                    "h-3 w-3",
                    selected ? "text-inherit" : cfg.color,
                  )}
                />
                {cfg.label}
              </button>
            );
          })}
        </div>

        {/* Row 2: project, actor, date range */}
        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={filters.projectId}
            onChange={(e) =>
              setFilters((prev) => ({ ...prev, projectId: e.target.value }))
            }
            className="h-8 w-44 text-xs"
          >
            <option value="all">All Projects</option>
            {projects.map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </Select>

          {/* Actor type toggle */}
          <div className="flex overflow-hidden rounded-lg border border-border text-xs">
            {(["all", "agent", "system"] as const).map((a) => (
              <button
                key={a}
                onClick={() =>
                  setFilters((prev) => ({ ...prev, actorType: a }))
                }
                className={cn(
                  "px-3 py-1.5 font-medium transition-colors",
                  filters.actorType === a
                    ? "bg-foreground text-background"
                    : "bg-background text-muted-foreground hover:bg-muted",
                )}
              >
                {a === "all"
                  ? "All actors"
                  : a === "agent"
                    ? "🤖 Agents"
                    : "System"}
              </button>
            ))}
          </div>

          <Input
            type="date"
            value={filters.dateFrom}
            onChange={(e) =>
              setFilters((prev) => ({ ...prev, dateFrom: e.target.value }))
            }
            className="h-8 w-36 text-xs"
          />
          <span className="text-xs text-muted-foreground">–</span>
          <Input
            type="date"
            value={filters.dateTo}
            onChange={(e) =>
              setFilters((prev) => ({ ...prev, dateTo: e.target.value }))
            }
            className="h-8 w-36 text-xs"
          />
        </div>
      </div>

      {/* Event list */}
      <div
        ref={scrollContainerRef}
        className="max-h-[calc(100vh-20rem)] space-y-2 overflow-y-auto"
      >
        {/* Real-time events */}
        {filteredRealtimeEvents.length > 0 && (
          <div className="space-y-2">
            <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Real-time
            </p>
            {filteredRealtimeEvents.map((event) => (
              <RealtimeEventRow
                key={`rt-${event.timestamp}-${event.type}`}
                event={event}
              />
            ))}
          </div>
        )}

        {/* Historical */}
        {isLoading ? (
          <div className="space-y-2">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-16 w-full rounded-lg" />
            ))}
          </div>
        ) : filteredHistoricalEvents.length > 0 ? (
          <div className="space-y-2">
            {filteredRealtimeEvents.length > 0 && (
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                History
              </p>
            )}
            {filteredHistoricalEvents.map((event) => (
              <EventRow key={event.id} event={event} />
            ))}
          </div>
        ) : totalVisible === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <Activity className="mb-4 h-12 w-12 text-muted-foreground" />
            {hasActiveFilters ? (
              <>
                <h3 className="mb-2 text-lg font-semibold">
                  No events match your filters
                </h3>
                <p className="mb-4 text-sm text-muted-foreground">
                  Try adjusting or resetting the filters.
                </p>
                <Button variant="outline" size="sm" onClick={resetFilters}>
                  <RotateCcw className="mr-2 h-4 w-4" />
                  Reset filters
                </Button>
              </>
            ) : (
              <>
                <h3 className="mb-2 text-lg font-semibold">No events yet</h3>
                <p className="text-sm text-muted-foreground">
                  Events from agent activity will appear here in real-time.
                </p>
              </>
            )}
          </div>
        ) : null}
      </div>
    </div>
  );
}
