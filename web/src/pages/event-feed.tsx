import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router";
import {
  Activity,
  AlertTriangle,
  ArrowDownCircle,
  CheckCircle2,
  Circle,
  Filter,
  Info,
  Radio,
  RefreshCw,
  Zap,
} from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { useProjectStore } from "@/stores/project";
import { useWebSocketStore } from "@/stores/websocket";
import { useWebSocket } from "@/hooks/use-websocket";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
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

// ---------------------------------------------------------------------------
// Event row component
// ---------------------------------------------------------------------------

interface EventRowProps {
  event: EventBusMessage;
}

function EventRow({ event }: EventRowProps) {
  const config = EVENT_TYPE_CONFIG[event.event_type] ?? EVENT_TYPE_CONFIG.custom;
  const Icon = config.icon;

  const timeStr = new Date(event.created_at).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });

  const agentName =
    event.agent_id
      ? (event.payload?.agent_name as string) || event.agent_id.slice(0, 8)
      : "System";

  return (
    <div className="flex items-start gap-3 rounded-lg border border-border bg-card p-3 transition-colors hover:bg-muted/30">
      <div className={cn("mt-0.5 shrink-0", config.color)}>
        <Icon className="h-4 w-4" />
      </div>

      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <Badge variant="outline" className="shrink-0 text-[10px]">
            {config.label}
          </Badge>
          <span className="truncate text-sm font-medium">{event.subject}</span>
        </div>

        <div className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
          <span>{agentName}</span>
          <span>&middot;</span>
          <time>{timeStr}</time>
          {event.tags.length > 0 && (
            <>
              <span>&middot;</span>
              {event.tags.slice(0, 3).map((tag) => (
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

  const timeStr = new Date(event.timestamp).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });

  const subject = (data.subject as string) || event.type;
  const agentName =
    data.agent_id
      ? (data.agent_name as string) || (data.agent_id as string)?.slice(0, 8)
      : "System";

  const tags = (data.tags as string[]) || [];

  return (
    <div className="flex items-start gap-3 rounded-lg border border-primary/20 bg-primary/5 p-3 transition-colors">
      <div className={cn("mt-0.5 shrink-0", config.color)}>
        <Icon className="h-4 w-4" />
      </div>

      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <Radio className="h-3 w-3 shrink-0 animate-pulse text-primary" />
          <Badge variant="outline" className="shrink-0 text-[10px]">
            {config.label}
          </Badge>
          <span className="truncate text-sm font-medium">{subject}</span>
        </div>

        <div className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
          <span>{agentName}</span>
          <span>&middot;</span>
          <time>{timeStr}</time>
          {tags.length > 0 && (
            <>
              <span>&middot;</span>
              {tags.slice(0, 3).map((tag) => (
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
// Event Feed page
// ---------------------------------------------------------------------------

export function EventFeedPage() {
  const { wsSlug } = useParams();
  const { projects } = useProjectStore();
  const eventLog = useWebSocketStore((s) => s.eventLog);
  const isConnected = useWebSocketStore((s) => s.isConnected);

  // Filter state
  const [typeFilter, setTypeFilter] = useState<string>("all");
  const [projectFilter, setProjectFilter] = useState<string>("all");
  const [autoScroll, setAutoScroll] = useState(true);

  // Historical events from REST API
  const [historicalEvents, setHistoricalEvents] = useState<EventBusMessage[]>(
    [],
  );
  const [isLoading, setIsLoading] = useState(false);

  const scrollContainerRef = useRef<HTMLDivElement>(null);

  // Use WebSocket for real-time events.
  useWebSocket({ workspaceSlug: wsSlug });

  // Fetch historical events from REST API.
  const fetchHistoricalEvents = useCallback(async () => {
    if (projectFilter === "all") {
      // If no project is selected, try to fetch from all projects.
      if (projects.length === 0) return;

      setIsLoading(true);
      try {
        const allEvents: EventBusMessage[] = [];
        for (const project of projects.slice(0, 5)) {
          try {
            const data = await api<PaginatedResponse<EventBusMessage>>(
              `/api/v1/projects/${project.id}/events`,
              { params: { per_page: "50" } },
            );
            allEvents.push(...data.items);
          } catch {
            // Skip projects with errors.
          }
        }
        // Sort by created_at desc.
        allEvents.sort(
          (a, b) =>
            new Date(b.created_at).getTime() -
            new Date(a.created_at).getTime(),
        );
        setHistoricalEvents(allEvents.slice(0, 100));
      } finally {
        setIsLoading(false);
      }
    } else {
      setIsLoading(true);
      try {
        const data = await api<PaginatedResponse<EventBusMessage>>(
          `/api/v1/projects/${projectFilter}/events`,
          { params: { per_page: "100" } },
        );
        setHistoricalEvents(data.items);
      } catch {
        setHistoricalEvents([]);
      } finally {
        setIsLoading(false);
      }
    }
  }, [projectFilter, projects]);

  useEffect(() => {
    fetchHistoricalEvents();
  }, [fetchHistoricalEvents]);

  // Auto-scroll to top when new events arrive.
  useEffect(() => {
    if (autoScroll && scrollContainerRef.current) {
      scrollContainerRef.current.scrollTop = 0;
    }
  }, [eventLog.length, autoScroll]);

  // Filter real-time events.
  const filteredRealtimeEvents = useMemo(() => {
    return eventLog.filter((event) => {
      if (typeFilter !== "all" && event.type !== typeFilter) {
        return false;
      }
      if (projectFilter !== "all") {
        const data = event.data as Record<string, unknown>;
        if (data.project_id && data.project_id !== projectFilter) {
          return false;
        }
      }
      return true;
    });
  }, [eventLog, typeFilter, projectFilter]);

  // Filter historical events.
  const filteredHistoricalEvents = useMemo(() => {
    return historicalEvents.filter((event) => {
      if (typeFilter !== "all" && event.event_type !== typeFilter) {
        return false;
      }
      if (
        projectFilter !== "all" &&
        event.project_id !== projectFilter
      ) {
        return false;
      }
      return true;
    });
  }, [historicalEvents, typeFilter, projectFilter]);

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
            onClick={fetchHistoricalEvents}
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
      <div className="flex flex-wrap items-center gap-3">
        <Filter className="h-4 w-4 text-muted-foreground" />

        <Select
          value={typeFilter}
          onChange={(e) => setTypeFilter(e.target.value)}
          className="w-44"
        >
          <option value="all">All Event Types</option>
          <option value="summary">Summary</option>
          <option value="status_change">Status Change</option>
          <option value="context_update">Context Update</option>
          <option value="error">Error</option>
          <option value="dependency_resolved">Dependency Resolved</option>
          <option value="custom">Custom</option>
        </Select>

        <Select
          value={projectFilter}
          onChange={(e) => setProjectFilter(e.target.value)}
          className="w-48"
        >
          <option value="all">All Projects</option>
          {projects.map((project) => (
            <option key={project.id} value={project.id}>
              {project.name}
            </option>
          ))}
        </Select>
      </div>

      {/* Event list */}
      <div
        ref={scrollContainerRef}
        className="max-h-[calc(100vh-14rem)] space-y-2 overflow-y-auto"
      >
        {/* Real-time events first */}
        {filteredRealtimeEvents.length > 0 && (
          <div className="space-y-2">
            <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Real-time
            </p>
            {filteredRealtimeEvents.map((event) => (
              <RealtimeEventRow key={`rt-${event.timestamp}-${event.type}`} event={event} />
            ))}
          </div>
        )}

        {/* Historical events */}
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
        ) : filteredRealtimeEvents.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <Activity className="mb-4 h-12 w-12 text-muted-foreground" />
            <h3 className="mb-2 text-lg font-semibold">No events yet</h3>
            <p className="text-sm text-muted-foreground">
              Events from agent activity will appear here in real-time.
            </p>
          </div>
        ) : null}
      </div>
    </div>
  );
}
