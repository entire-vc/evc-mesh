// event-feed widget (PRD §5.8 «События»): everything of the old Events page —
// Live indicator, auto-scroll, refresh, in-widget filters (types, project,
// actor, dates, reset), live events over the history — on the server feed
// GET /workspaces/:ws/events. Sizes itself to its cell with container queries.
import * as i18nFmt from "@/i18n/format";
import type { TFunction } from "i18next";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
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
  User,
  Zap,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { useProjectStore } from "@/stores/project";
import { AgentShortTag } from "@/components/agent-short-tag";
import { TaskLink } from "@entire-vc/mesh-theme-sdk";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import type { EventType } from "@/types";
import {
  EVENT_TYPES,
  activeFilterCount,
  isDefaultFilters,
  useEvents,
  type ActorFilter,
  type EventFeedFilters,
  type FeedEvent,
} from "./use-events";
import { filtersFromSettings, type EventFeedSettings } from "./settings";

const EVENT_TYPE_CONFIG: Record<EventType, { color: string; icon: typeof Circle }> = {
  summary: { color: "text-tone-positive-500", icon: CheckCircle2 },
  status_change: { color: "text-tone-info-500", icon: RefreshCw },
  context_update: { color: "text-tone-violet-500", icon: Info },
  error: { color: "text-tone-danger-500", icon: AlertTriangle },
  dependency_resolved: { color: "text-tone-warning-500", icon: ArrowDownCircle },
  custom: { color: "text-tone-neutral-500", icon: Zap },
};

const ACTORS: readonly ActorFilter[] = ["all", "agent", "user", "system"];

function formatTime(iso: string): string {
  const d = new Date(iso);
  const today = new Date();
  const sameDay = d.toDateString() === today.toDateString();
  return i18nFmt.formatTime(iso, sameDay
    ? { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" }
    : { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
}

export function statusDelta(payload: Record<string, unknown>): { from: string; to: string } | null {
  const status = payload.status as { old?: string; new?: string } | undefined;
  return status?.old && status?.new ? { from: status.old, to: status.new } : null;
}

export function actionVerb(t: TFunction, eventType: EventType, subject: string): string {
  if (subject === "task.moved") return t("eventFeed.verb.moved");
  if (subject === "task.assigned") return t("eventFeed.verb.assigned");
  if (subject === "task.created") return t("eventFeed.verb.created");
  if (subject === "task.deleted") return t("eventFeed.verb.deleted");
  if (subject === "task.updated") return t("eventFeed.verb.updated");
  switch (eventType) {
    case "status_change":
      return t("eventFeed.verb.updated");
    case "summary":
      return t("eventFeed.verb.summarised");
    case "context_update":
      return t("eventFeed.verb.updatedContextOn");
    case "error":
      return t("eventFeed.verb.reportedErrorOn");
    case "dependency_resolved":
      return t("eventFeed.verb.resolvedDependencyOn");
    default:
      return t("eventFeed.verb.published");
  }
}

function EventRow({ event, live }: { event: FeedEvent; live: boolean }) {
  const { t } = useTranslation("integrations");
  const config = EVENT_TYPE_CONFIG[event.event_type] ?? EVENT_TYPE_CONFIG.custom;
  const Icon = config.icon;
  const actorId = event.actor_id ?? event.agent_id;
  const actorName =
    event.actor_name ?? (event.actor_type === "system" ? t("eventFeed.system") : (actorId?.slice(0, 8) ?? t("eventFeed.system")));
  const delta = statusDelta(event.payload);
  const taskShort = event.task_id?.slice(0, 8);

  return (
    <li
      data-event-id={event.id}
      data-live={live || undefined}
      className={cn(
        "flex items-start gap-3 rounded-lg border p-3 transition-colors",
        live ? "border-primary/20 bg-primary/5" : "border-border bg-card hover:bg-muted/30",
      )}
    >
      <div className={cn("mt-0.5 shrink-0", config.color)}>
        <Icon className="h-4 w-4" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5 text-sm">
          {live && <Radio className="h-3 w-3 shrink-0 animate-pulse text-primary" aria-label={t("eventFeed.live")} />}
          <span className="font-medium text-foreground">
            {actorName}
            {event.actor_type === "agent" && <AgentShortTag id={actorId} />}
          </span>
          {event.actor_type === "agent" && <Bot className="h-3 w-3 text-muted-foreground" />}
          {event.actor_type === "user" && <User className="h-3 w-3 text-muted-foreground" />}
          <span className="text-muted-foreground">{actionVerb(t, event.event_type, event.subject)}</span>
          {event.task_id ? (
            <TaskLink taskId={event.task_id} className="min-w-0 max-w-full truncate font-medium text-foreground hover:underline @md:max-w-[260px]">
              {event.task_title ?? taskShort}
            </TaskLink>
          ) : (
            <span className="min-w-0 max-w-full truncate italic text-muted-foreground @md:max-w-[260px]">{event.subject}</span>
          )}
          {delta && (
            <span className="flex items-center gap-1 text-xs">
              <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
                {delta.from}
              </Badge>
              <ChevronRight className="h-3 w-3 text-muted-foreground" />
              <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
                {delta.to}
              </Badge>
            </span>
          )}
        </div>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
          {event.project_name && <span className="font-medium">{event.project_name}</span>}
          {event.project_name && taskShort && <span>&middot;</span>}
          {taskShort && <span className="font-mono">{taskShort}</span>}
          {(event.project_name || taskShort) && <span>&middot;</span>}
          <time dateTime={event.created_at}>{formatTime(event.created_at)}</time>
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
    </li>
  );
}

export interface EventFeedWidgetProps {
  /** Instance settings: the defaults of the in-widget filters. */
  settings?: EventFeedSettings;
  /** Full-screen («Развернуть») mode: the list takes the viewport height. */
  expanded?: boolean;
  /**
   * Dashboard card: the filter panel starts closed behind a «Фильтры» button with the count of active filters, so the
   * card's height goes to the events. Without it the panel is always open.
   */
  collapsibleFilters?: boolean;
}

export function EventFeedWidget({ settings, expanded = false, collapsibleFilters = false }: EventFeedWidgetProps) {
  const { t } = useTranslation("integrations");
  const projects = useProjectStore((s) => s.projects);
  const initial = useMemo(() => filtersFromSettings(settings), [settings]);
  const [filters, setFilters] = useState<EventFeedFilters>(initial);
  useEffect(() => setFilters(initial), [initial]);
  const [autoScroll, setAutoScroll] = useState(settings?.auto_scroll ?? true);
  const live = settings?.auto_refresh ?? true;
  const { result, liveIds, connected, refreshing, loadingMore, refresh, loadMore } = useEvents(filters, { live });

  const listRef = useRef<HTMLDivElement>(null);
  const items = result.state === "ready" ? result.items : null;
  const newest = items?.[0]?.id;
  useEffect(() => {
    if (autoScroll && listRef.current) listRef.current.scrollTop = 0;
  }, [newest, autoScroll]);

  const toggleType = (type: EventType) =>
    setFilters((f) => ({
      ...f,
      eventTypes: f.eventTypes.includes(type) ? f.eventTypes.filter((x) => x !== type) : [...f.eventTypes, type],
    }));
  const reset = () => setFilters(initial);
  const filtered = !isDefaultFilters(filters);
  const changed = JSON.stringify(filters) !== JSON.stringify(initial);
  const activeFilters = activeFilterCount(filters);
  const [filtersOpen, setFiltersOpen] = useState(!collapsibleFilters);
  const showFilters = !collapsibleFilters || filtersOpen;
  const panelId = useId();

  return (
    <section data-widget="event-feed" className="@container flex h-full min-h-0 flex-col gap-3">
      {/* Toolbar */}
      <div className="flex flex-wrap items-center justify-between gap-2">
        {live && connected ? (
          <Badge variant="success" className="gap-1">
            <Radio className="h-3 w-3 animate-pulse" />
            {t("eventFeed.live")}
          </Badge>
        ) : (
          <Badge variant="secondary" className="gap-1">
            <Circle className="h-3 w-3" />
            {t("eventFeed.offline")}
          </Badge>
        )}
        <div className="flex flex-wrap items-center justify-end gap-2">
          <Button variant={autoScroll ? "default" : "outline"} size="sm" aria-pressed={autoScroll} onClick={() => setAutoScroll((v) => !v)}>
            {autoScroll ? t("eventFeed.autoScrollOn") : t("eventFeed.autoScrollOff")}
          </Button>
          <Button variant="outline" size="sm" onClick={refresh} disabled={refreshing || result.state === "loading"}>
            <RefreshCw className={cn("mr-1 h-3 w-3", refreshing && "animate-spin")} />
            {t("eventFeed.refresh")}
          </Button>
          {collapsibleFilters && (
            <Button
              variant={filtersOpen ? "default" : "outline"}
              size="sm"
              aria-expanded={filtersOpen}
              aria-controls={panelId}
              onClick={() => setFiltersOpen((v) => !v)}
            >
              <Filter className="mr-1 h-3 w-3" />
              {t("eventFeed.filters")}
              {activeFilters > 0 && (
                <Badge variant="secondary" className="ml-1 px-1.5 py-0 text-[10px]" aria-label={t("eventFeed.filtersActive", { count: activeFilters })}>
                  {activeFilters}
                </Badge>
              )}
            </Button>
          )}
        </div>
      </div>

      {/* Filters */}
      {showFilters && (
        <div id={panelId} className="space-y-2 rounded-lg border border-border bg-card p-3">
          <div className="flex items-center gap-2">
            <Filter className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">{t("eventFeed.filters")}</span>
            {changed && (
              <Button variant="ghost" size="sm" className="ml-auto h-6 px-2 text-xs" onClick={reset}>
                <RotateCcw className="mr-1 h-3 w-3" />
                {t("eventFeed.reset")}
              </Button>
            )}
          </div>
          <div className="flex flex-wrap gap-1.5" role="group" aria-label={t("eventFeed.filters")}>
            {EVENT_TYPES.map((type) => {
              const cfg = EVENT_TYPE_CONFIG[type];
              const Icon = cfg.icon;
              const selected = filters.eventTypes.includes(type);
              return (
                <button
                  key={type}
                  type="button"
                  aria-pressed={selected}
                  onClick={() => toggleType(type)}
                  className={cn(
                    "flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                    selected
                      ? "border-transparent bg-foreground text-background"
                      : "border-border bg-background text-muted-foreground hover:border-foreground/30 hover:text-foreground",
                  )}
                >
                  <Icon className={cn("h-3 w-3", selected ? "text-inherit" : cfg.color)} />
                  {t(`eventFeed.types.${type}`)}
                </button>
              );
            })}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Select
              value={filters.projectId}
              onChange={(e) => setFilters((f) => ({ ...f, projectId: e.target.value }))}
              className="h-8 w-full text-xs @md:w-44"
              aria-label={t("eventFeed.allProjects")}
            >
              <option value="all">{t("eventFeed.allProjects")}</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
            <div className="flex overflow-hidden rounded-lg border border-border text-xs" role="group">
              {ACTORS.map((a) => (
                <button
                  key={a}
                  type="button"
                  aria-pressed={filters.actorType === a}
                  onClick={() => setFilters((f) => ({ ...f, actorType: a }))}
                  className={cn(
                    "px-2.5 py-1.5 font-medium transition-colors",
                    filters.actorType === a ? "bg-foreground text-background" : "bg-background text-muted-foreground hover:bg-muted",
                  )}
                >
                  {t(`eventFeed.actors.${a}`)}
                </button>
              ))}
            </div>
            <div className="flex w-full min-w-0 items-center gap-2 @md:w-auto">
              <Input
                type="date"
                value={filters.dateFrom}
                onChange={(e) => setFilters((f) => ({ ...f, dateFrom: e.target.value }))}
                className="h-8 min-w-0 flex-1 text-xs @md:w-36 @md:flex-none"
                aria-label={t("eventFeed.dateFrom")}
              />
              <span className="text-xs text-muted-foreground">–</span>
              <Input
                type="date"
                value={filters.dateTo}
                onChange={(e) => setFilters((f) => ({ ...f, dateTo: e.target.value }))}
                className="h-8 min-w-0 flex-1 text-xs @md:w-36 @md:flex-none"
                aria-label={t("eventFeed.dateTo")}
              />
            </div>
          </div>
        </div>
      )}

      {/* List */}
      <div
        ref={listRef}
        className={cn("min-h-0 flex-1 overflow-y-auto", expanded && "max-h-[calc(100dvh-20rem)] min-h-[16rem]")}
      >
        {result.state === "loading" ? (
          <div className="space-y-2" aria-busy="true">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-16 w-full rounded-lg" />
            ))}
          </div>
        ) : result.state === "error" ? (
          <div role="alert" className="flex flex-col items-center justify-center gap-3 py-12 text-center">
            <AlertTriangle className="h-8 w-8 text-tone-danger-500" />
            <p className="text-sm text-muted-foreground">{t("eventFeed.error")}</p>
            <Button variant="outline" size="sm" onClick={result.retry}>
              <RotateCcw className="mr-2 h-4 w-4" />
              {t("eventFeed.retry")}
            </Button>
          </div>
        ) : result.items.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-12 text-center">
            <Activity className="mb-4 h-10 w-10 text-muted-foreground" />
            {filtered ? (
              <>
                <h3 className="mb-2 text-base font-semibold">{t("eventFeed.noMatch.title")}</h3>
                <p className="mb-4 text-sm text-muted-foreground">{t("eventFeed.noMatch.hint")}</p>
                <Button variant="outline" size="sm" onClick={() => setFilters((f) => ({ ...f, eventTypes: [], projectId: "all", actorType: "all", dateFrom: "", dateTo: "" }))}>
                  <RotateCcw className="mr-2 h-4 w-4" />
                  {t("eventFeed.noMatch.reset")}
                </Button>
              </>
            ) : (
              <>
                <h3 className="mb-2 text-base font-semibold">{t("eventFeed.empty.title")}</h3>
                <p className="text-sm text-muted-foreground">{t("eventFeed.empty.hint")}</p>
              </>
            )}
          </div>
        ) : (
          <>
            <ul className="space-y-2">
              {result.items.map((e) => (
                <EventRow key={e.id} event={e} live={liveIds.has(e.id)} />
              ))}
            </ul>
            {result.hasMore && (
              <div className="flex justify-center py-3">
                <Button variant="outline" size="sm" onClick={loadMore} disabled={loadingMore}>
                  {loadingMore && <RefreshCw className="mr-1 h-3 w-3 animate-spin" />}
                  {t("eventFeed.more")}
                </Button>
              </div>
            )}
          </>
        )}
      </div>
    </section>
  );
}
