// Instance settings of the event-feed widget → the filters it opens with.
// The user changes filters inside the widget; settings only set the defaults.
import type { EventType } from "@/types";
import { DEFAULT_FILTERS, EVENT_TYPES, type ActorFilter, type EventFeedFilters } from "./use-events";

export interface EventFeedSettings {
  event_types?: EventType[];
  /** A project id; empty or absent = every project the viewer may see. */
  project_id?: string | null;
  actor_type?: ActorFilter;
  /** `period` control value: the last N days, or an explicit range (yyyy-mm-dd). */
  period?: { days: number } | { from: string; to: string } | null;
  auto_scroll?: boolean;
  /** Common widget setting: live updates (on by default). */
  auto_refresh?: boolean;
}

const ACTORS: readonly ActorFilter[] = ["all", "agent", "user", "system"];

function day(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export function filtersFromSettings(s: EventFeedSettings | undefined, now: Date = new Date()): EventFeedFilters {
  const f: EventFeedFilters = { ...DEFAULT_FILTERS };
  if (!s) return f;
  if (Array.isArray(s.event_types)) {
    f.eventTypes = s.event_types.filter((t): t is EventType => (EVENT_TYPES as readonly string[]).includes(t));
  }
  if (typeof s.project_id === "string" && s.project_id) f.projectId = s.project_id;
  if (s.actor_type && ACTORS.includes(s.actor_type)) f.actorType = s.actor_type;
  const period = s.period;
  if (period && "days" in period && period.days > 0) {
    const from = new Date(now);
    from.setDate(from.getDate() - (period.days - 1));
    f.dateFrom = day(from);
  } else if (period && "from" in period) {
    f.dateFrom = period.from ?? "";
    f.dateTo = period.to ?? "";
  }
  return f;
}
