// The dashboard view of the statistics widget and the shared component other pages embed (a project's or an
// agent's page pins its filter, U5). The instance's settings seed the state; in-widget changes (a row's «Only
// this») are local to the visit, the instance keeps its saved settings — reconfiguring is the editor's job.
import { useMemo, useState } from "react";
import { ExternalLink } from "lucide-react";
import { useNavigate, useParams } from "react-router";
import { Card, CardContent } from "@/components/ui/card";
import { isUserUsage, stateFromSettings, stateToSearch, type StatsState } from "@/core/agent-usage/state";
import { StatsBody, sanitize, useCapabilities } from "@/core/agent-usage/stats-body";
import { useST, type ST } from "@/core/agent-usage/format";
import { viewLabel } from "@/core/agent-usage/labels";
import type { FilterKey } from "@/core/agent-usage/types";
import type { WidgetViewProps } from "../../types";
import { widgetTitle, type WidgetConfig } from "../../config";

/** One grid row of the dashboard = this many rem (events' cards use the same ruler). */
export const ROW_REM = 8;

/** What an embedding page can pin: the filters stay on for every read, the quota view can be switched off. */
export interface AgentUsageWidgetProps {
  settings?: Record<string, unknown>;
  fixedFilter?: Partial<Record<FilterKey, string[]>>;
  /** A pinned filter has no pools of its own: «Subscription limits» makes no sense on a project's page. */
  noQuota?: boolean;
  title?: string;
  /** «Open in statistics»: the host navigates; a dashboard view passes a navigate, an embed may omit it. */
  onOpenStats?: (st: StatsState) => void;
  st?: ST;
}

const pinnedKind = (f: AgentUsageWidgetProps["fixedFilter"]) => (f?.project ? "project" : f?.agent ? "agent" : undefined);

export function AgentUsageWidget({ settings, fixedFilter, noQuota, title, onOpenStats, st: stProp }: AgentUsageWidgetProps) {
  const ownSt = useST();
  const st = stProp ?? ownSt;
  const [live, setLive] = useState<{ baseKey: string; state: StatsState } | null>(null);
  const base = useMemo(() => {
    const s = sanitize(stateFromSettings(settings), undefined);
    const f = { ...s.f, ...fixedFilter };
    return { ...s, f, m: noQuota && s.m === "quota" ? "cost" : s.m } as StatsState;
  }, [settings, fixedFilter, noQuota]);
  const caps = useCapabilities(isUserUsage(base) ? "user" : "workspace");
  const baseKey = JSON.stringify(base);
  const seed = live?.baseKey === baseKey ? live.state : base;
  const state = useMemo(() => ({ ...seed, f: { ...seed.f, ...fixedFilter }, m: noQuota && seed.m === "quota" ? "cost" as const : seed.m }), [seed, fixedFilter, noQuota]);
  const current = useMemo(() => sanitize(state, caps), [state, caps]);
  // The instance title replaces the type name (§5.6); a blank one keeps the view's name.
  const heading = title?.trim() || widgetTitle((settings ?? {}) as WidgetConfig, viewLabel(current.m, st));

  return (
    <div className="flex min-h-0 min-w-0 flex-col gap-2" data-testid="agent-usage-widget">
      <div className="flex min-w-0 items-center justify-between gap-2">
        <h3 className="truncate text-sm font-semibold">{heading}</h3>
        {onOpenStats && (
          <button
            type="button"
            className="inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium text-primary hover:bg-primary/10"
            onClick={() => onOpenStats(current)}
          >
            <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
            {st("openStats", "Open in statistics")}
          </button>
        )}
      </div>
      <div className="min-h-0 min-w-0 flex-1">
        <StatsBody state={current} onChange={(next) => setLive({ baseKey, state: next })} mode="widget" links={{}} pinned={pinnedKind(fixedFilter)} st={st} />
      </div>
    </div>
  );
}

/** The dashboard cell: a card of the instance's height; longer tables scroll inside it. */
export function AgentUsageWidgetView({ settings, size }: WidgetViewProps<Record<string, unknown>>) {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const navigate = useNavigate();
  return (
    <Card className="flex flex-col overflow-hidden" style={{ height: `${size.h * ROW_REM}rem` }} data-testid="agent-usage-card">
      <CardContent className="flex min-h-0 flex-1 flex-col pt-4">
        <AgentUsageWidget settings={settings} onOpenStats={wsSlug ? (s) => navigate(statsHref(wsSlug, s)) : undefined} />
      </CardContent>
    </Card>
  );
}

/** The page URL of a state: the page and «Open in statistics» build the same link. */
export const statsHref = (wsSlug: string, st: StatsState): string => `/w/${wsSlug}/stats?${stateToSearch(st).toString()}`;
