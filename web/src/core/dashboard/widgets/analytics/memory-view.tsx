// «Память Mesh» (plan §7, mockup v3): does memory help the agents — do they find what they need, use what they
// find, how much context it costs and what in the base is dead weight, by agent, project and period. One request
// (GET …/analytics/memory) per filter; the header changes the filter in place, a click on an agent's row narrows
// the whole widget to that agent, the list links open the paged detail lists of §6.
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useParams } from "react-router";
import { ArrowDownRight, ArrowUpRight, ChevronDown, SlidersHorizontal, X } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import * as fmt from "@/i18n/format";
import { cn } from "@/lib/cn";
import { useAgentStore } from "@/stores/agent";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Async } from "../../types";
import {
  MEMORY_DEFAULT_CALLERS,
  activeFilterCount,
  memoryExportRequest,
  memoryDataParams,
  memoryParams,
  readMemoryFilter,
  toggle,
  type MemoryFilter,
  type MemoryView,
} from "./memory-filters";
import {
  CALLER_KINDS,
  MEMORY_GROUPS,
  MEMORY_OPS,
  MEMORY_READ_OPS,
  MEMORY_SOURCES,
  SERVED_SOURCES,
  type MemoryAnalytics,
  type MemoryDay,
  type MemoryDetailKind,
  type MemoryGroup,
  type MemoryKpis,
  type MemoryNullReason,
  type MemoryRow,
} from "./memory-types";
import { detailFromWire, fromWire, type WireList, type WireMemoryAnalytics } from "./memory-wire";
import { NEUTRAL, WidgetFrame, seriesColor, useAT, type AT } from "./ui";
import { useCachedGet, type AnalyticsPeriod } from "./use-analytics";
import type { AnalyticsViewProps } from "./views";

// -- numbers --------------------------------------------------------------------------------------------

const pct = (v: number) => `${fmt.formatNumber(v * 100, { maximumFractionDigits: 1 })}%`;
const num = (v: number) => fmt.formatNumber(v);
const approx = (v: number) => `≈${fmt.formatNumber(v, { notation: "compact", maximumFractionDigits: v >= 10_000 ? 0 : 1 })}`;
const shortDay = (d: string) => fmt.formatDate(`${d.slice(0, 10)}T00:00:00`, { day: "2-digit", month: "2-digit" });

/** Colour of each read operation on the chart (§7: the bars are read calls by `op`); an op the list does not know is neutral. */
export const OP_COLOR: Record<string, string> = Object.fromEntries(MEMORY_READ_OPS.map((op, i) => [op, seriesColor(i)]));
const opColor = (op: string) => OP_COLOR[op] ?? NEUTRAL;
/** The useful-recall line: a colour no operation bar uses. */
export const LINE_COLOR = "hsl(var(--chart-priority-high))";

function reasonText(at: AT, reason: MemoryNullReason | undefined, journalSince: string | null): string {
  if (reason === "no_journal_before")
    return journalSince
      ? at("memory.reason.journalSince", "the read journal is kept since {{date}}", { date: fmt.formatDate(journalSince) })
      : at("memory.reason.noJournal", "the read journal is not kept yet");
  if (reason === "not_collected") return at("memory.reason.notCollected", "not collected yet");
  if (reason === "not_applicable") return at("memory.reason.notApplicable", "only writes support this split");
  if (reason === "retention") return at("memory.reason.retention", "outside the detail retention period");
  if (reason === "text_expired") return at("memory.reason.textExpired", "query text has expired");
  if (reason === "excluded_by_filter") return at("memory.reason.excludedByFilter", "excluded by the operation filter");
  if (reason === "no_data") return at("memory.reason.noData", "no events in the window");
  if (reason === "detail_timeout") return at("memory.reason.detailTimeout", "the list took too long, retry");
  return reason ? reason.replace(/_/g, " ") : at("memory.reason.unknown", "the server did not count it");
}

// -- the widget ----------------------------------------------------------------------------------------

export function MemoryUsageView({ settings, actions }: AnalyticsViewProps) {
  const at = useAT();
  const settingsKey = JSON.stringify(settings ?? {});
  const [filter, setFilter] = useState<MemoryFilter>(() => readMemoryFilter(settings));
  // A saved settings change (the settings dialog) restarts the filter from it.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => setFilter(readMemoryFilter(settings)), [settingsKey]);
  const [detail, setDetail] = useState<{ kind: MemoryDetailKind; agentId?: string; agentName?: string } | null>(null);
  // Pinned per filter: a preset's comparison window is cut to the minute, so asking it anew on every render would
  // change the request key each minute and blank the widget into its skeleton.
  const params = useMemo(() => memoryDataParams(filter), [filter]);
  const wire = useCachedGet<WireMemoryAnalytics>("/analytics/memory", params);
  const r = useMemo<Async<MemoryAnalytics>>(() => (wire.state === "ready" ? { state: "ready", value: fromWire(wire.value, filter.groupBy) } : wire), [wire, filter.groupBy]);
  const update = (patch: Partial<MemoryFilter>) => setFilter((f) => ({ ...f, ...patch }));
  const agentNames = useAgentNames(r);

  return (
    <WidgetFrame
      title={at("memory.title", "Mesh memory: how it is read and written")}
      exportRequest={(wsId) => memoryExportRequest(wsId, filter)}
      toolbar={<HeaderControls filter={filter} update={update} />}
    >
      <div className="min-w-0 space-y-3" data-testid="memory-widget">
        <FilterBar filter={filter} update={update} agentNames={agentNames} />
        <MemoryBody
          result={r}
          retry={actions.retry ?? (r.state === "error" ? r.retry : undefined)}
          filter={filter}
          update={update}
          agentNames={agentNames}
          openDetail={(kind, agentId) => setDetail({ kind, agentId, agentName: agentId ? agentNames.get(agentId) : undefined })}
        />
      </div>
      {detail && <DetailDialog filter={filter} {...detail} onClose={() => setDetail(null)} />}
    </WidgetFrame>
  );
}

/** Agent names for the filter: the workspace's agents, and every agent a row has named (so a narrowed widget keeps them). */
function useAgentNames(r: Async<MemoryAnalytics>): Map<string, string> {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const agents = useAgentStore((s) => s.agents);
  const fetchAgents = useAgentStore((s) => s.fetchAgents);
  const [seen, setSeen] = useState<Map<string, string>>(new Map());
  useEffect(() => {
    if (wsId && agents.length === 0) void fetchAgents(wsId).catch(() => {});
  }, [wsId, agents.length, fetchAgents]);
  useEffect(() => {
    if (r.state !== "ready") return;
    const add = r.value.rows.filter((row) => row.agent_id && !seen.has(row.agent_id));
    if (add.length) setSeen((m) => new Map([...m, ...add.map((row) => [row.agent_id!, row.label] as const)]));
  }, [r, seen]);
  return useMemo(() => new Map([...seen, ...agents.map((a) => [a.id, a.name] as const)]), [seen, agents]);
}

// -- header: period and view ------------------------------------------------------------------------------

const PRESETS = [7, 30, 90] as const;

function HeaderControls({ filter, update }: { filter: MemoryFilter; update: (p: Partial<MemoryFilter>) => void }) {
  const at = useAT();
  const p = filter.period;
  const custom = !("days" in p);
  const [editing, setEditing] = useState(false);
  const today = new Date().toISOString().slice(0, 10);
  const [from, setFrom] = useState(custom ? p.from : "");
  const [to, setTo] = useState(custom ? p.to : today);
  const views: { id: MemoryView; label: string }[] = [
    { id: "chart", label: at("memory.view.chart", "Chart") },
    { id: "top", label: at("memory.view.top", "Top-N") },
    { id: "table", label: at("memory.view.table", "Table") },
  ];
  const chip = "rounded-full border border-border px-2.5 py-0.5 text-xs aria-pressed:border-primary aria-pressed:bg-primary aria-pressed:text-primary-foreground";
  return (
    <>
      <div role="group" aria-label={at("memory.period", "Period")} className="flex shrink-0 items-center gap-1" data-testid="memory-period">
        {PRESETS.map((d) => (
          <button key={d} type="button" aria-pressed={"days" in p && p.days === d} className={chip} onClick={() => (setEditing(false), update({ period: { days: d } }))}>
            {at("memory.days", "{{days}}d", { days: d })}
          </button>
        ))}
        <button type="button" aria-pressed={custom} className={chip} onClick={() => setEditing((v) => !v)}>
          {custom ? `${shortDay(p.from)}–${shortDay(p.to)}` : at("memory.custom", "Custom…")}
        </button>
      </div>
      {editing && (
        <form
          className="flex max-w-full flex-wrap items-center gap-1 text-xs"
          onSubmit={(e) => {
            e.preventDefault();
            if (from && to && from <= to) {
              update({ period: { from, to } as AnalyticsPeriod });
              setEditing(false);
            }
          }}
        >
          <input type="date" aria-label={at("memory.from", "From")} value={from} max={to || today} onChange={(e) => setFrom(e.target.value)} className="rounded-md border border-border bg-background px-1.5 py-0.5" />
          <span>–</span>
          <input type="date" aria-label={at("memory.to", "To")} value={to} min={from} max={today} onChange={(e) => setTo(e.target.value)} className="rounded-md border border-border bg-background px-1.5 py-0.5" />
          <button type="submit" disabled={!from || !to || from > to} className="rounded-md border border-border px-2 py-0.5 disabled:opacity-50">
            {at("memory.apply", "Apply")}
          </button>
        </form>
      )}
      <div role="group" aria-label={at("memory.viewLabel", "View")} className="inline-flex shrink-0 overflow-hidden rounded-md border border-border">
        {views.map((v) => (
          <button
            key={v.id}
            type="button"
            aria-pressed={filter.view === v.id}
            onClick={() => update({ view: v.id })}
            className="px-2.5 py-1 text-xs aria-pressed:bg-primary aria-pressed:text-primary-foreground"
          >
            {v.label}
          </button>
        ))}
      </div>
    </>
  );
}

// -- filter bar -------------------------------------------------------------------------------------------

function FilterMenu({ name, value, testId, children }: { name: string; value: string; testId: string; children: ReactNode }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        data-testid={testId}
        className="inline-flex max-w-full items-center gap-1 rounded-md border border-border bg-background px-2 py-1 text-xs hover:bg-muted"
      >
        <span className="text-muted-foreground">{name}:</span>
        <b className="max-w-48 truncate font-semibold">{value}</b>
        <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="max-h-72 overflow-y-auto">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** One choice of a single-value filter: picks and closes. */
function Pick({ checked, onPick, children }: { checked: boolean; onPick: () => void; children: ReactNode }) {
  return (
    <DropdownMenuItem role="menuitemradio" aria-checked={checked} onClick={onPick} className={cn("gap-2 text-xs", checked && "font-semibold")}>
      <span className="w-3">{checked ? "✓" : ""}</span>
      {children}
    </DropdownMenuItem>
  );
}

/** One choice of a list filter: toggles and stays open. */
function Check({ checked, onToggle, children }: { checked: boolean; onToggle: () => void; children: ReactNode }) {
  return (
    <div
      role="menuitemcheckbox"
      aria-checked={checked}
      tabIndex={0}
      onClick={onToggle}
      onKeyDown={(e) => (e.key === " " || e.key === "Enter") && (e.preventDefault(), onToggle())}
      className="flex cursor-pointer select-none items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-accent"
    >
      <input type="checkbox" readOnly checked={checked} tabIndex={-1} className="h-3.5 w-3.5" />
      {children}
    </div>
  );
}

const listValue = (at: AT, items: string[], all: string) => (items.length === 0 ? all : items.length <= 3 ? items.join(", ") : at("memory.nSelected", "{{n}} selected", { n: items.length }));

export function useMemoryLabels() {
  const at = useAT();
  return {
    op: (o: string) => at(`memory.op.${o}`, o.replace(/_/g, " ")),
    source: (s: string) => at(`memory.source.${s}`, s.replace(/_/g, " ")),
    caller: (c: string) => at(`memory.caller.${c}`, c.replace(/_/g, " ")),
    group: (g: string) => at(`memory.group.${g}`, g.replace(/_/g, " ")),
  };
}

function FilterBar({ filter, update, agentNames }: { filter: MemoryFilter; update: (p: Partial<MemoryFilter>) => void; agentNames: Map<string, string> }) {
  const at = useAT();
  const L = useMemoryLabels();
  const projects = useProjectStore((s) => s.projects);
  const [open, setOpen] = useState(false);
  const count = activeFilterCount(filter);
  const all = at("memory.all", "all");
  const projectName = filter.projectId ? (projects.find((p) => p.id === filter.projectId)?.name ?? filter.projectId.slice(0, 8)) : all;
  const agentList = [...agentNames].sort((a, b) => a[1].localeCompare(b[1]));
  const groupMenu = (
    <FilterMenu
      name={at("memory.filter.group", "Split")}
      testId="memory-filter-group"
      value={filter.groupBy2 ? `${L.group(filter.groupBy)} · ${L.group(filter.groupBy2)}` : L.group(filter.groupBy)}
    >
      <div role="group" aria-label={at("memory.filter.group", "Split")}>
        {MEMORY_GROUPS.map((g) => (
          <Pick key={g} checked={filter.groupBy === g} onPick={() => update({ groupBy: g, groupBy2: filter.groupBy2 === g ? undefined : filter.groupBy2 })}>
            {L.group(g)}
          </Pick>
        ))}
      </div>
      <div className="my-1 border-t border-border px-2 pt-1.5 text-[11px] text-muted-foreground">{at("memory.filter.group2", "Second split")}</div>
      <div role="group" aria-label={at("memory.filter.group2", "Second split")}>
        <Pick checked={!filter.groupBy2} onPick={() => update({ groupBy2: undefined })}>
          {at("memory.none", "none")}
        </Pick>
        {MEMORY_GROUPS.filter((g) => g !== filter.groupBy).map((g) => (
          <Pick key={g} checked={filter.groupBy2 === g} onPick={() => update({ groupBy2: g })}>
            {L.group(g)}
          </Pick>
        ))}
      </div>
    </FilterMenu>
  );
  return (
    <div className="min-w-0">
      <div className="flex flex-wrap items-center gap-1.5 @3xl:hidden">
        <button
          type="button"
          aria-expanded={open}
          data-testid="memory-filters-toggle"
          onClick={() => setOpen((v) => !v)}
          className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2.5 py-1 text-xs"
        >
          <SlidersHorizontal className="h-3.5 w-3.5" />
          {at("memory.filters", "Filters")} · {count}
        </button>
        {groupMenu}
      </div>
      <div className={cn("mt-1.5 flex-wrap items-center gap-1.5 @3xl:mt-0 @3xl:flex", open ? "flex" : "hidden")} data-testid="memory-filters">
        <FilterMenu name={at("memory.filter.project", "Project")} value={projectName} testId="memory-filter-project">
          <Pick checked={!filter.projectId} onPick={() => update({ projectId: undefined })}>
            {all}
          </Pick>
          {projects.map((p) => (
            <Pick key={p.id} checked={filter.projectId === p.id} onPick={() => update({ projectId: p.id })}>
              {p.name}
            </Pick>
          ))}
        </FilterMenu>
        <FilterMenu name={at("memory.filter.agent", "Agent")} value={listValue(at, filter.agents.map((id) => agentNames.get(id) ?? id.slice(0, 8)), all)} testId="memory-filter-agent">
          <Pick checked={filter.agents.length === 0} onPick={() => update({ agents: [] })}>
            {all}
          </Pick>
          {agentList.map(([id, name]) => (
            <Check key={id} checked={filter.agents.includes(id)} onToggle={() => update({ agents: toggle(filter.agents, id) })}>
              {name}
            </Check>
          ))}
        </FilterMenu>
        <FilterMenu name={at("memory.filter.op", "Operation")} value={L.op(filter.op)} testId="memory-filter-op">
          {MEMORY_OPS.map((o) => (
            <Pick key={o} checked={filter.op === o} onPick={() => update({ op: o })}>
              {L.op(o)}
            </Pick>
          ))}
        </FilterMenu>
        <FilterMenu name={at("memory.filter.source", "Source")} value={listValue(at, filter.sources.map(L.source), all)} testId="memory-filter-source">
          <Pick checked={filter.sources.length === 0} onPick={() => update({ sources: [] })}>
            {all}
          </Pick>
          {MEMORY_SOURCES.map((s) => (
            <Check key={s} checked={filter.sources.includes(s)} onToggle={() => update({ sources: toggle(filter.sources, s) })}>
              {L.source(s)}
            </Check>
          ))}
        </FilterMenu>
        <FilterMenu name={at("memory.filter.caller", "Called by")} value={listValue(at, filter.callers.map(L.caller), all)} testId="memory-filter-caller">
          <Pick checked={filter.callers.length === 0} onPick={() => update({ callers: [] })}>
            {all}
          </Pick>
          {CALLER_KINDS.map((c) => (
            <Check key={c} checked={filter.callers.includes(c)} onToggle={() => update({ callers: toggle(filter.callers, c) })}>
              {L.caller(c)}
            </Check>
          ))}
        </FilterMenu>
        <div className="hidden @3xl:contents">{groupMenu}</div>
        {count > 0 && (
          <button
            type="button"
            onClick={() => update({ projectId: undefined, agents: [], op: "all", sources: [], callers: [...MEMORY_DEFAULT_CALLERS], groupBy: "agent", groupBy2: undefined })}
            className="inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:text-foreground"
          >
            <X className="h-3 w-3" />
            {at("memory.reset", "Reset")}
          </button>
        )}
      </div>
    </div>
  );
}

// -- body ---------------------------------------------------------------------------------------------------

function MemorySkeleton() {
  const at = useAT();
  return (
    <div className="space-y-3" aria-busy="true" aria-label={at("loading", "Loading")} data-testid="memory-skeleton">
      <div className="grid grid-cols-2 gap-2 @3xl:grid-cols-3 @6xl:grid-cols-6">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-24 rounded-lg" />
        ))}
      </div>
      <Skeleton className="h-48 w-full rounded-lg" />
      <Skeleton className="h-32 w-full rounded-lg" />
    </div>
  );
}

function MemoryBody({
  result,
  retry,
  filter,
  update,
  agentNames,
  openDetail,
}: {
  result: Async<MemoryAnalytics>;
  retry?: () => void;
  filter: MemoryFilter;
  update: (p: Partial<MemoryFilter>) => void;
  agentNames: Map<string, string>;
  openDetail: (kind: MemoryDetailKind, agentId?: string) => void;
}) {
  const at = useAT();
  if (result.state === "loading") return <MemorySkeleton />;
  if (result.state === "error") {
    return (
      <div role="alert" className="flex flex-col items-center justify-center gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-6 text-center text-xs">
        <p className="font-medium text-foreground">{at("memory.error", "Could not load memory statistics")}</p>
        <p className="max-w-full break-words text-muted-foreground">{result.error.message}</p>
        <button type="button" className="underline hover:text-foreground" onClick={retry}>
          {at("retry", "Retry")}
        </button>
      </div>
    );
  }
  const m = result.value;
  const writesOnly = [filter.groupBy, filter.groupBy2].some((g) => g === "kind" || g === "scope");
  const journalSince = m.coverage.journal_since;
  const before = journalSince && m.period.from.slice(0, 10) < journalSince.slice(0, 10);
  const chartOnly = filter.view === "chart";
  return (
    <>
      {!writesOnly && (before || !journalSince) && (
        <p role="note" data-testid="memory-journal-note" className="rounded-md border border-tone-warning-500/30 bg-tone-warning-500/5 px-2.5 py-1.5 text-xs text-muted-foreground">
          {journalSince
            ? at("memory.journalSince", "The read journal is kept since {{date}}: earlier days of the period have no reads.", { date: fmt.formatDate(journalSince) })
            : at("memory.noJournal", "The read journal is not kept yet: read metrics show «no data».")}
        </p>
      )}
      {writesOnly && <p role="note" data-testid="memory-writes-note" className="text-xs text-muted-foreground">{at("memory.writesOnly", "Kind and scope support only writes. Read metrics are unavailable for these splits.")}</p>}
      <Kpis m={m} openDetail={openDetail} writesOnly={writesOnly} />
      {!writesOnly && <div className={cn("grid min-w-0 gap-3", !chartOnly && "@5xl:grid-cols-3")}>
        <section className={cn("min-w-0 rounded-lg border border-border p-3", !chartOnly && "@5xl:col-span-2")}>
          <h4 className="mb-2 text-xs font-semibold">{at("memory.chart.title", "Read calls by day and operation (bars, left axis) · useful recall, % (line, right axis)")}</h4>
          <ComboChart days={m.series} />
        </section>
        <Quality m={m} openDetail={openDetail} />
      </div>}
      {(!chartOnly || writesOnly) && <Breakdown m={m} filter={filter} update={update} agentNames={agentNames} openDetail={openDetail} writesOnly={writesOnly} />}
      {!writesOnly && <Coverage m={m} openDetail={openDetail} />}
    </>
  );
}

// -- tiles ---------------------------------------------------------------------------------------------------

type Better = "up" | "down" | "none";

function Delta({ now, prev, rate, better }: { now: number | null; prev: number | null | undefined; rate: boolean; better: Better }) {
  const at = useAT();
  if (now == null || prev == null) return null;
  const diff = now - prev;
  if (Math.abs(diff) < 1e-9) return <p className="text-[11px] text-muted-foreground">{at("memory.same", "same as before")}</p>;
  const sign = diff > 0 ? "+" : "−";
  const text = rate
    ? at("memory.deltaPp", "{{delta}} pp vs previous", { delta: `${sign}${fmt.formatNumber(Math.abs(diff) * 100, { maximumFractionDigits: 1 })}` })
    : at("memory.delta", "{{delta}} vs previous", { delta: `${sign}${num(Math.abs(Math.round(diff)))}` });
  const good = better === "none" ? null : (diff > 0) === (better === "up");
  const Icon = diff > 0 ? ArrowUpRight : ArrowDownRight;
  return (
    <p data-testid="memory-delta" className={cn("flex items-center gap-0.5 text-[11px]", good === null ? "text-muted-foreground" : good ? "text-tone-positive-600" : "text-tone-danger-600")}>
      <Icon className="h-3 w-3 shrink-0" />
      <span className="truncate">{text}</span>
    </p>
  );
}

function Tile({
  id,
  label,
  value,
  reason,
  children,
  delta,
}: {
  id: string;
  label: string;
  value: string | null;
  reason?: string;
  children?: ReactNode;
  delta?: ReactNode;
}) {
  const at = useAT();
  return (
    <div data-kpi={id} data-nodata={value === null ? "" : undefined} className="min-w-0 rounded-lg border border-border p-2.5">
      <p className="break-words text-[11px] uppercase leading-tight tracking-wide text-muted-foreground" title={label}>
        {label}
      </p>
      {value === null ? (
        <>
          <p className="mt-0.5 text-[15px] font-medium text-muted-foreground">{at("memory.noData", "no data")}</p>
          <p className="text-[11px] text-tone-warning-600">{reason}</p>
        </>
      ) : (
        <>
          <p className="mt-0.5 truncate text-[22px] font-semibold leading-tight tabular-nums" title={value}>
            {value}
          </p>
          <div className="space-y-0.5 text-[11px] leading-snug text-muted-foreground">{children}</div>
          {delta}
        </>
      )}
    </div>
  );
}

function LinkButton({ onClick, children, testId }: { onClick: () => void; children: ReactNode; testId?: string }) {
  return (
    <button type="button" data-testid={testId} onClick={onClick} className="text-[11px] text-primary hover:underline">
      {children}
    </button>
  );
}

function Kpis({ m, openDetail, writesOnly }: { m: MemoryAnalytics; openDetail: (kind: MemoryDetailKind) => void; writesOnly: boolean }) {
  const at = useAT();
  const L = useMemoryLabels();
  const k = m.kpis;
  const prev = m.previous_period?.kpis;
  const why = (key: keyof MemoryKpis) => reasonText(at, k.reasons?.[key], m.coverage.journal_since);
  const val = <K extends keyof MemoryKpis>(key: K, f: (v: number) => string) => {
    const v = k[key];
    return typeof v === "number" ? f(v) : null;
  };
  const byOp = Object.entries(k.reads_by_op ?? {}).filter(([, n]) => (n ?? 0) > 0);
  return (
    <div className="grid grid-cols-2 gap-2 @3xl:grid-cols-3 @6xl:grid-cols-6" data-testid="memory-kpis">
      {!writesOnly && <>
      <Tile id="useful_rate" label={at("memory.kpi.useful", "Useful recall")} value={val("useful_rate", pct)} reason={why("useful_rate")} delta={<Delta now={k.useful_rate} prev={prev?.useful_rate} rate better="up" />}>
        {k.useful != null && k.hits != null && <p>{at("memory.kpi.usefulOf", "{{useful}} of {{hits}} non-empty · a lower bound", { useful: num(k.useful), hits: num(k.hits) })}</p>}
        {k.hit_rate != null && (
          <p data-testid="memory-hit-rate">
            {at("memory.kpi.hitRate", "hit-rate {{rate}}: recalls with a non-pinned row", { rate: pct(k.hit_rate) })}
          </p>
        )}
      </Tile>
      <Tile id="miss_rate" label={at("memory.kpi.miss", "Search misses")} value={val("miss_rate", pct)} reason={why("miss_rate")} delta={<Delta now={k.miss_rate} prev={prev?.miss_rate} rate better="down" />}>
        <LinkButton onClick={() => openDetail("empty-queries")}>{at("memory.emptyQueries", "empty queries")}</LinkButton>
      </Tile>
      <Tile id="dead30" label={at("memory.kpi.dead30", "Dead weight 30d")} value={val("dead30", pct)} reason={why("dead30")} delta={<Delta now={k.dead30} prev={prev?.dead30} rate better="down" />}>
        {k.dead30_count != null && <p>{at("memory.kpi.deadCount", "{{n}} records not served in 30 days", { n: num(k.dead30_count) })}</p>}
        <LinkButton onClick={() => openDetail("dead-records")}>{at("memory.list", "list")}</LinkButton>
      </Tile>
      <Tile id="tokens_est" label={at("memory.kpi.tokens", "Tokens into context")} value={val("tokens_est", approx)} reason={why("tokens_est")} delta={<Delta now={k.tokens_est} prev={prev?.tokens_est} rate={false} better="down" />}>
        {k.tokens_per_read != null && <p>{at("memory.kpi.tokensPer", "≈{{n}} per read · estimate", { n: num(Math.round(k.tokens_per_read)) })}</p>}
      </Tile>
      <Tile id="reads" label={at("memory.kpi.reads", "Reads")} value={val("reads", num)} reason={why("reads")} delta={<Delta now={k.reads} prev={prev?.reads} rate={false} better="none" />}>
        {byOp.length > 0 && <p className="break-words" title={byOp.map(([op, n]) => `${L.op(op)} ${num(n ?? 0)}`).join(" · ")}>{byOp.map(([op, n]) => `${L.op(op)} ${num(n ?? 0)}`).join(" · ")}</p>}
      </Tile>
      </>}
      <Tile id="writes" label={at("memory.kpi.writes", "Writes")} value={val("writes", num)} reason={why("writes")} delta={<Delta now={k.writes} prev={prev?.writes} rate={false} better="none" />}>
        {k.writes != null && k.with_reason != null && (
          <p>{at("memory.kpi.writesReason", "with a reason {{n}} of {{writes}}", { n: num(k.with_reason), writes: num(k.writes) })}</p>
        )}
      </Tile>
    </div>
  );
}

// -- chart -----------------------------------------------------------------------------------------------------

/** Drawing width without a measurement (tests, the first paint); the chart then follows its frame. */
const CW = 700;
/** Pixel height at every width: at 393 a scaled 700-wide drawing would shrink to ~100 px and its labels to ~5 px. */
const CH = 210;
/** Below this width the line's percentages move into its points' tooltips and fewer days are labelled. */
const NARROW = 480;

/** The element's content width, followed through resizes; `fallback` until measured or without ResizeObserver. */
function useWidth<T extends Element>(fallback: number) {
  // a callback ref: the element mounts only once there are days to draw, after the first render
  const [el, ref] = useState<T | null>(null);
  const [width, setWidth] = useState(fallback);
  useEffect(() => {
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => {
      const w = Math.round(entries[0]?.contentRect.width ?? 0);
      if (w > 0) setWidth(w);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [el]);
  return [ref, width] as const;
}
const PAD = { top: 18, right: 44, bottom: 24, left: 36 };

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  const n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p;
}

/**
 * Read calls per day stacked by operation (left axis; the stack is the day's reads) and the day's useful recall as a
 * line (right axis), with one legend for both (§7). A bar's tooltip also names the rows the day served by source.
 */
export function ComboChart({ days }: { days: MemoryDay[] }) {
  const at = useAT();
  const L = useMemoryLabels();
  const [box, width] = useWidth<HTMLDivElement>(CW);
  const label = at("memory.chart.aria", "Read calls per day by operation and the share of useful recalls");
  if (days.length === 0) return <p className="py-6 text-center text-xs text-muted-foreground">{at("empty", "No data for this period")}</p>;
  const narrow = width < NARROW;
  // the server's own operations first in the §7 order, then any it adds
  const stack = [...MEMORY_READ_OPS, ...[...new Set(days.flatMap((d) => Object.keys(d.reads_by_op)))].filter((op) => !(MEMORY_READ_OPS as readonly string[]).includes(op))];
  const total = (d: MemoryDay) => stack.reduce((s, k) => s + (d.reads_by_op[k] ?? 0), 0);
  const max = niceMax(Math.max(...days.map(total)));
  const w = width - PAD.left - PAD.right;
  const h = CH - PAD.top - PAD.bottom;
  const slot = w / days.length;
  const bar = Math.max(2, Math.min(56, slot * 0.62));
  const cx = (i: number) => PAD.left + i * slot + slot / 2;
  const yR = (r: number) => PAD.top + h - r * h;
  // a day label every ≥48 px: 7 days fit at 393, 30 days are labelled every 4th
  const every = Math.max(1, Math.ceil(days.length / Math.max(1, Math.floor(w / 48))));
  const used = stack.filter((k) => days.some((d) => (d.reads_by_op[k] ?? 0) > 0));
  const points = days.map((d, i) => (d.useful_rate == null ? null : { x: cx(i), y: yR(d.useful_rate), r: d.useful_rate }));
  // a day without a rate breaks the line rather than drawing it through 0
  const segments: { x: number; y: number }[][] = [];
  let cur: { x: number; y: number }[] = [];
  for (const p of points) {
    if (p) cur.push(p);
    else if (cur.length) (segments.push(cur), (cur = []));
  }
  if (cur.length) segments.push(cur);
  const showLabels = !narrow && days.length <= 14;
  const served = (d: MemoryDay) => {
    const parts = SERVED_SOURCES.map((src) => [src, d.served_rows_by_source[src] ?? 0] as const).filter(([, n]) => n > 0);
    return parts.length ? `\n${at("memory.chart.served", "served rows")}: ${parts.map(([src, n]) => `${L.source(src)} ${num(n)}`).join(" · ")}` : "";
  };
  return (
    <div ref={box} className="min-w-0">
      <ul className="mb-1.5 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground" data-testid="memory-legend">
        {used.map((k) => (
          <li key={k} data-legend={k} className="flex items-center gap-1">
            <span className="inline-block h-2.5 w-2.5 rounded-sm" style={{ backgroundColor: opColor(k) }} />
            {L.op(k)}
          </li>
        ))}
        <li data-legend="useful-line" className="flex items-center gap-1">
          <svg width="16" height="8" aria-hidden="true">
            <line x1="0" x2="16" y1="4" y2="4" stroke={LINE_COLOR} strokeWidth="2.5" />
            <circle cx="8" cy="4" r="2.5" fill="white" stroke={LINE_COLOR} strokeWidth="1.5" />
          </svg>
          {at("memory.chart.line", "useful recall, %")}
        </li>
      </ul>
      <svg viewBox={`0 0 ${width} ${CH}`} height={CH} className="block w-full" role="img" aria-label={label} data-testid="memory-chart" data-narrow={narrow ? "" : undefined}>
        {[0, 0.5, 1].map((f) => (
          <g key={f}>
            <line x1={PAD.left} x2={PAD.left + w} y1={PAD.top + h - f * h} y2={PAD.top + h - f * h} stroke="currentColor" strokeOpacity={0.1} />
            <text x={PAD.left - 6} y={PAD.top + h - f * h + 4} fontSize={10} textAnchor="end" fill="currentColor" fillOpacity={0.55}>
              {num(Math.round(max * f))}
            </text>
            <text x={PAD.left + w + 6} y={PAD.top + h - f * h + 4} fontSize={10} fill={LINE_COLOR}>
              {`${Math.round(f * 100)}%`}
            </text>
          </g>
        ))}
        {days.map((d, i) => {
          let y = PAD.top + h;
          const tail = served(d);
          return (
            <g key={d.date} data-day={d.date} data-nodata={d.reads == null ? "" : undefined}>
              {stack.map((k) => {
                const v = d.reads_by_op[k] ?? 0;
                if (v <= 0) return null;
                const bh = (v / max) * h;
                y -= bh;
                return (
                  <rect key={k} data-op={k} x={cx(i) - bar / 2} y={y} width={bar} height={bh} fill={opColor(k)}>
                    <title>{`${shortDay(d.date)} · ${L.op(k)}: ${num(v)}${tail}`}</title>
                  </rect>
                );
              })}
              {i % every === 0 && (
                <text x={cx(i)} y={CH - 6} fontSize={11} textAnchor="middle" fill="currentColor" fillOpacity={0.55}>
                  {shortDay(d.date)}
                </text>
              )}
            </g>
          );
        })}
        {segments.map((s, i) => (
          <polyline key={i} data-testid="memory-useful-line" points={s.map((p) => `${p.x},${p.y}`).join(" ")} fill="none" stroke={LINE_COLOR} strokeWidth={2.5} strokeLinejoin="round" />
        ))}
        {points.map(
          (p, i) =>
            p && (
              <g key={i}>
                <circle cx={p.x} cy={p.y} r={3.5} fill="white" stroke={LINE_COLOR} strokeWidth={2}>
                  <title>{`${shortDay(days[i]!.date)} · ${at("memory.chart.line", "useful recall, %")}: ${pct(p.r)}`}</title>
                </circle>
                {showLabels && (
                  <text data-point-label x={p.x} y={p.y - 8} fontSize={10} fontWeight={600} textAnchor="middle" fill={LINE_COLOR}>
                    {`${Math.round(p.r * 100)}%`}
                  </text>
                )}
              </g>
            ),
        )}
      </svg>
    </div>
  );
}

// -- quality -----------------------------------------------------------------------------------------------------

function Quality({ m, openDetail }: { m: MemoryAnalytics; openDetail: (kind: MemoryDetailKind) => void }) {
  const at = useAT();
  const q = m.quality;
  const noData = (key: string, reasons: MemoryAnalytics["quality"]["reasons"]) => (
    <span className="font-medium text-muted-foreground" title={reasonText(at, reasons?.[key], m.coverage.journal_since)}>
      {at("memory.noData", "no data")}
      <span className="block text-[11px] font-normal text-tone-warning-600">{reasonText(at, reasons?.[key], m.coverage.journal_since)}</span>
    </span>
  );
  const line = (id: string, label: string, v: number | null, f: (v: number) => string, extra?: ReactNode, reasons = q.reasons) => (
    <li key={id} data-quality={id} className="flex items-center justify-between gap-2 border-b border-dashed border-border py-1.5 text-xs last:border-0">
      <span className="min-w-0">{label}</span>
      <span className="shrink-0 text-right">
        {v == null ? noData(id, reasons) : <b className="font-semibold tabular-nums">{f(v)}</b>}
        {v != null && extra}
      </span>
    </li>
  );
  // a count of the base and its share of the active records, when there are any
  const ofActive = (v: number) => (q.active ? `${num(v)} · ${pct(v / q.active)}` : num(v));
  const link = (kind: MemoryDetailKind, text: string) => (
    <button type="button" onClick={() => openDetail(kind)} className="ml-2 text-[11px] text-primary hover:underline">
      {text}
    </button>
  );
  return (
    <section className="min-w-0 rounded-lg border border-border p-3" data-testid="memory-quality">
      <h4 className="mb-1 text-xs font-semibold">{at("memory.quality.title", "Base quality")}</h4>
      <ul>
        {line("active", at("memory.quality.active", "Active records"), q.active, num)}
        {line("dup_groups", at("memory.quality.dups", "Duplicates (same simhash)"), q.dup_groups, (v) => `${num(v)} ${at("memory.quality.groups", "gr.")} / ${num(q.dup_rows ?? 0)}`, link("duplicates", at("memory.open", "open")))}
        {line("no_kind", at("memory.quality.noKind", "Without a kind: tag"), q.no_kind, ofActive)}
        {line("statuslog_like", at("memory.quality.statuslog", "Look like a status log"), q.statuslog_like, ofActive)}
        {line("superseded_active", at("memory.quality.superseded", "Superseded (active)"), q.superseded_active, num)}
        {line("superseded_served_rate", at("memory.quality.supersededServed", "Superseded in answers, % of rows"), m.kpis.superseded_served_rate, pct, undefined, m.kpis.reasons)}
        {line("conflicted", at("memory.quality.conflicted", "Contradictions (conflicted)"), q.conflicted, num)}
        {line("with_reason_rate", at("memory.quality.reason", "Writes with a reason"), q.with_reason_rate, pct)}
      </ul>
    </section>
  );
}

// -- breakdown -----------------------------------------------------------------------------------------------------

const rate = (v: number | null) => (v == null ? "—" : pct(v));
const perWrite = (v: number | null) => (v == null ? "—" : fmt.formatNumber(v, { maximumFractionDigits: 1 }));

function Breakdown({
  m,
  filter,
  update,
  agentNames,
  openDetail,
  writesOnly,
}: {
  m: MemoryAnalytics;
  filter: MemoryFilter;
  update: (p: Partial<MemoryFilter>) => void;
  agentNames: Map<string, string>;
  openDetail: (kind: MemoryDetailKind, agentId?: string) => void;
  writesOnly: boolean;
}) {
  const at = useAT();
  const L = useMemoryLabels();
  const byAgent = filter.groupBy === "agent";
  const narrowTo = (row: MemoryRow) => {
    if (!row.agent_id) return undefined;
    const only = filter.agents.length === 1 && filter.agents[0] === row.agent_id;
    return () => update({ agents: only ? [] : [row.agent_id!] });
  };
  const span = `${shortDay(m.period.from)}–${shortDay(m.period.to)}`;
  const title = byAgent
    ? at("memory.table.titleAgent", "By agent · {{span}} · a click on a row filters the whole widget", { span })
    : at("memory.table.title", "By {{group}} · {{span}}", { group: L.group(filter.groupBy), span });
  // the server names agents, projects, kinds and scopes; operations, sources, callers and days are named here
  const labelOf = (g: MemoryGroup, key: string | null | undefined, label: string | null | undefined): string => {
    if (g === "op" && key) return L.op(key);
    if (g === "source" && key) return key === "*" ? at("memory.source.any", "search, several sources") : L.source(key);
    if (g === "caller_kind" && key) return L.caller(key);
    if (g === "day" && key) return shortDay(key);
    return label || at("memory.unnamed", "not named");
  };
  const rows = m.rows.map((r) => ({ ...r, label: labelOf(filter.groupBy, r.key, r.label), label2: filter.groupBy2 ? labelOf(filter.groupBy2, r.key2, r.label2) : null }));
  const metricCell = (row: Omit<MemoryRow, "key" | "label">, key: "reads" | "writes" | "with_reason" | "hit_rate" | "useful_rate" | "tokens_est" | "reads_per_write", format: (v: number) => string) => {
    const value = row[key];
    return value == null ? <span title={reasonText(at, row.reasons?.[key], m.coverage.journal_since)} className="text-muted-foreground">{at("memory.noData", "no data")}<span className="block text-[11px] text-tone-warning-600">{reasonText(at, row.reasons?.[key], m.coverage.journal_since)}</span></span> : format(value);
  };
  const actionsOf = (row: MemoryRow) =>
    row.agent_id && !writesOnly ? (
      <span className="inline-flex gap-2">
        <LinkButton onClick={() => openDetail("empty-queries", row.agent_id!)}>{at("memory.emptyQueries", "empty queries")}</LinkButton>
        <LinkButton onClick={() => openDetail("top-records", row.agent_id!)}>{at("memory.topRecords", "top records")}</LinkButton>
      </span>
    ) : null;
  void agentNames;
  if (filter.view === "top") {
    const metric = (r: MemoryRow) => (writesOnly ? r.writes : r.reads);
    const top = [...rows].sort((a, b) => (metric(b) ?? -1) - (metric(a) ?? -1)).slice(0, 10);
    const max = Math.max(1, ...top.map((r) => metric(r) ?? 0));
    return (
      <section className="min-w-0 rounded-lg border border-border p-3" data-testid="memory-top">
        <h4 className="mb-2 text-xs font-semibold">{writesOnly ? at("memory.top.writesTitle", "Top {{n}} by writes · {{span}}", { n: top.length, span }) : at("memory.top.title", "Top {{n}} by reads · {{span}}", { n: top.length, span })}</h4>
        {top.length === 0 ? (
          <p className="py-4 text-center text-xs text-muted-foreground">{at("memory.table.none", "No rows for this filter")}</p>
        ) : (
          <ul className="space-y-1.5">
            {top.map((row, i) => {
              const open = narrowTo(row);
              return (
                <li key={`${row.key}|${row.key2 ?? ""}`} data-row={row.key} className="flex min-w-0 items-center gap-2 text-xs">
                  <span className="w-5 shrink-0 text-right tabular-nums text-muted-foreground">{i + 1}.</span>
                  {open ? (
                    <button type="button" onClick={open} className="w-2/5 shrink-0 truncate text-left hover:underline">
                      {row.label}
                      {row.label2 ? ` · ${row.label2}` : ""}
                    </button>
                  ) : (
                    <span className="w-2/5 shrink-0 truncate">{row.label}{row.label2 ? ` · ${row.label2}` : ""}</span>
                  )}
                  <div className="relative h-3 min-w-0 flex-1">
                    <div className="absolute inset-y-0 left-0 rounded-sm" style={{ width: `${((metric(row) ?? 0) / max) * 100}%`, backgroundColor: seriesColor(0), minWidth: (metric(row) ?? 0) > 0 ? 3 : 0 }} />
                  </div>
                  <span className="shrink-0 text-right font-medium tabular-nums">{metricCell(row, writesOnly ? "writes" : "reads", num)}</span>
                  {!writesOnly && <span className="w-14 shrink-0 text-right tabular-nums text-muted-foreground">{rate(row.useful_rate)}</span>}
                </li>
              );
            })}
          </ul>
        )}
      </section>
    );
  }
  const head = writesOnly ? [at("memory.table.writes", "Writes"), at("memory.table.withReason", "With a reason")] : [
    at("memory.table.reads", "Reads"),
    at("memory.table.writes", "Writes"),
    at("memory.table.hitRate", "Hit-rate"),
    at("memory.table.useful", "Useful recall"),
    at("memory.table.tokens", "Tokens into context"),
    at("memory.table.perWrite", "Reads per write"),
  ];
  const cells = (r: Omit<MemoryRow, "key" | "label">) => writesOnly
    ? [metricCell(r, "writes", num), metricCell(r, "with_reason", num)]
    : [metricCell(r, "reads", num), metricCell(r, "writes", num), metricCell(r, "hit_rate", pct), metricCell(r, "useful_rate", pct), metricCell(r, "tokens_est", approx), metricCell(r, "reads_per_write", perWrite)];
  return (
    <section className="min-w-0 rounded-lg border border-border p-3" data-testid="memory-breakdown">
      <h4 className="mb-1 text-xs font-semibold">{title}</h4>
      {rows.length === 0 ? (
        <p className="py-4 text-center text-xs text-muted-foreground">{at("memory.table.none", "No rows for this filter")}</p>
      ) : (
        <>
          {/* wide: a table with its own scroll and a pinned header */}
          <div className="hidden max-h-80 overflow-auto @3xl:block" data-testid="memory-table-scroll">
            <table className="w-full border-collapse text-xs" data-testid="memory-table">
              <thead className="sticky top-0 z-10 bg-card">
                <tr className="text-[11px] text-muted-foreground">
                  <th className="border-b border-border px-2 py-1.5 text-left font-medium">{L.group(filter.groupBy)}</th>
                  {filter.groupBy2 && <th className="border-b border-border px-2 py-1.5 text-left font-medium">{L.group(filter.groupBy2)}</th>}
                  {head.map((h) => (
                    <th key={h} className="whitespace-nowrap border-b border-border px-2 py-1.5 text-right font-medium">
                      {h}
                    </th>
                  ))}
                  <th className="border-b border-border" />
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const open = narrowTo(row);
                  const selected = Boolean(row.agent_id && filter.agents.includes(row.agent_id));
                  return (
                    <tr
                      key={`${row.key}|${row.key2 ?? ""}`}
                      data-row={row.key}
                      aria-selected={selected || undefined}
                      onClick={open}
                      className={cn("border-b border-border/70", open && "cursor-pointer hover:bg-muted/50", selected && "bg-primary/5")}
                    >
                      <td className="max-w-56 truncate px-2 py-1.5 text-left">
                        {open ? (
                          <button type="button" onClick={(e) => (e.stopPropagation(), open())} className="truncate text-left hover:underline" title={at("memory.table.filterBy", "Filter the widget by {{name}}", { name: row.label })}>
                            {row.label}
                          </button>
                        ) : (
                          row.label
                        )}
                      </td>
                      {filter.groupBy2 && <td className="max-w-40 truncate px-2 py-1.5 text-left text-muted-foreground">{row.label2 ?? "—"}</td>}
                      {cells(row).map((c, i) => (
                        <td key={i} className="whitespace-nowrap px-2 py-1.5 text-right tabular-nums">
                          {c}
                        </td>
                      ))}
                      <td className="whitespace-nowrap px-2 py-1.5 text-right" onClick={(e) => e.stopPropagation()}>
                        {actionsOf(row)}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
              {m.totals && (
                <tfoot className="sticky bottom-0 z-10 bg-card">
                  <tr data-total className="bg-muted/60 font-semibold">
                    <td className="px-2 py-1.5 text-left">{at("memory.table.total", "Total")}</td>
                    {filter.groupBy2 && <td />}
                    {cells(m.totals).map((c, i) => (
                      <td key={i} className="whitespace-nowrap px-2 py-1.5 text-right tabular-nums">
                        {c}
                      </td>
                    ))}
                    <td />
                  </tr>
                </tfoot>
              )}
            </table>
          </div>
          {/* narrow: one card per row */}
          <div className="max-h-[32rem] space-y-2 overflow-auto @3xl:hidden" data-testid="memory-cards">
            {[...rows.map((row) => ({ row, total: false })), ...(m.totals ? [{ row: { ...m.totals, key: "__total", label: at("memory.table.total", "Total") } as MemoryRow, total: true }] : [])].map(({ row, total }) => {
              const open = total ? undefined : narrowTo(row);
              return (
                <div key={`${row.key}|${row.key2 ?? ""}`} data-card={row.key} className={cn("rounded-lg border border-border p-2.5 text-xs", total && "bg-muted/50")}>
                  <div className="flex items-baseline justify-between gap-2 font-semibold">
                    {open ? (
                      <button type="button" onClick={open} className="truncate text-left hover:underline">
                        {row.label}
                        {row.label2 ? ` · ${row.label2}` : ""}
                      </button>
                    ) : (
                      <span className="truncate">{row.label}{row.label2 ? ` · ${row.label2}` : ""}</span>
                    )}
                    {!writesOnly && <span className="shrink-0">
                      {at("memory.card.useful", "useful {{rate}}", { rate: rate(row.useful_rate) })}
                    </span>}
                  </div>
                  <dl className="mt-1.5 grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 @xl:grid-cols-[auto_1fr_auto_1fr]">
                    {(writesOnly ? [0, 1] : [0, 1, 2, 4]).map((i) => <div key={i} className="contents"><dt className="text-muted-foreground">{head[i]}</dt><dd className="tabular-nums">{cells(row)[i]}</dd></div>)}
                  </dl>
                  {!total && actionsOf(row) && <div className="mt-1.5">{actionsOf(row)}</div>}
                </div>
              );
            })}
          </div>
        </>
      )}
    </section>
  );
}

function Coverage({ m, openDetail }: { m: MemoryAnalytics; openDetail: (kind: MemoryDetailKind) => void }) {
  const at = useAT();
  const L = useMemoryLabels();
  const kinds = Object.entries(m.coverage.caller_kind ?? {}).filter(([, n]) => (n ?? 0) > 0);
  return (
    <p className="text-[11px] text-muted-foreground" data-testid="memory-coverage">
      {kinds.length > 0 && (
        <>
          {at("memory.coverage.callers", "Called by")}: {kinds.map(([k, n]) => `${L.caller(k)} ${num(n ?? 0)}`).join(" · ")}
          {m.coverage.unknown_share != null && ` (${at("memory.coverage.unknown", "unknown {{share}}", { share: pct(m.coverage.unknown_share) })})`}
          {" · "}
        </>
      )}
      {(m.coverage.unknown_share ?? 0) > 0 && <><LinkButton onClick={() => openDetail("unknown-sources")}>{at("memory.coverage.unknownSources", "unknown sources")}</LinkButton>{" · "}</>}
      {at("memory.coverage.note", "«No data» is shown apart and never folded into 0. Useful recall is a lower bound: use without a reference to the key is not seen.")}
    </p>
  );
}

// -- detail lists --------------------------------------------------------------------------------------------------

const PAGE_SIZE = 20;

/** The orders each list's server knows (`sort`); the first is its default and is not sent. */
const SORTS: Record<MemoryDetailKind, { id: string; key: string; en: string }[]> = {
  "empty-queries": [
    { id: "", key: "sortMost", en: "Most first" },
    { id: "last_at", key: "sortRecent", en: "Recent first" },
  ],
  "top-records": [
    { id: "", key: "sortServed", en: "Most served" },
    { id: "useful", key: "sortUseful", en: "Most useful" },
  ],
  "dead-records": [
    { id: "", key: "sortOldest", en: "Oldest first" },
    { id: "newest", key: "sortNewest", en: "Newest first" },
  ],
  duplicates: [],
  "unknown-sources": [],
};

function DetailDialog({ filter, kind, agentId, agentName, onClose }: { filter: MemoryFilter; kind: MemoryDetailKind; agentId?: string; agentName?: string; onClose: () => void }) {
  const at = useAT();
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState("");
  const [page, setPage] = useState(1);
  // Only a changed search goes back to page 1: an unchanged one (mount, a space)
  // starts no timer, so a quick Next is not undone by the debounce.
  useEffect(() => {
    const next = q.trim();
    if (next === query) return;
    const t = setTimeout(() => (setQuery(next), setPage(1)), 300);
    return () => clearTimeout(t);
  }, [q, query]);
  const params = {
    ...memoryParams(agentId ? { ...filter, agents: [agentId] } : filter),
    page: String(page),
    page_size: String(PAGE_SIZE),
    ...(sort ? { sort } : {}),
    ...(query ? { q: query } : {}),
  };
  const wire = useCachedGet<WireList<unknown>>(`/analytics/memory/${kind}`, params);
  const r = useMemo(() => (wire.state === "ready" ? { state: "ready" as const, value: detailFromWire(kind, wire.value) } : wire), [wire, kind]);
  const title = at(`memory.detail.${kind}`, kind.replace(/-/g, " "));
  const pages = r.state === "ready" ? Math.max(1, Math.ceil(r.value.total / Math.max(1, r.value.page_size))) : 1;
  const sorts = SORTS[kind];
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-xl" data-testid="memory-detail">
        <DialogHeader>
          <DialogTitle>
            {title}
            {agentName ? ` · ${agentName}` : ""}
          </DialogTitle>
          <DialogDescription>{at("memory.detail.hint", "The widget's filter and period; the key opens the record in Memory.")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-wrap gap-2">
          <input
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={at("memory.detail.search", "Search")}
            aria-label={at("memory.detail.search", "Search")}
            className="h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-sm"
          />
          {sorts.length > 0 && (
            <select
              value={sort}
              onChange={(e) => (setSort(e.target.value), setPage(1))}
              aria-label={at("memory.detail.sort", "Sort")}
              className="h-8 rounded-md border border-border bg-background px-2 text-sm"
            >
              {sorts.map((o) => (
                <option key={o.id} value={o.id}>
                  {at(`memory.detail.${o.key}`, o.en)}
                </option>
              ))}
            </select>
          )}
        </div>
        {r.state === "ready" && r.value.coverage?.detail_since && (
          <p role="note" className="text-xs text-muted-foreground">{at("memory.detail.since", "Details are available since {{date}}", { date: fmt.formatDate(r.value.coverage.detail_since) })}{r.value.coverage.reason ? ` · ${reasonText(at, r.value.coverage.reason, r.value.coverage.detail_since)}` : ""}</p>
        )}
        <div className="max-h-[50vh] min-h-24 overflow-auto">
          {r.state === "loading" && <Skeleton className="h-24 w-full" />}
          {r.state === "error" && (
            <div role="alert" className="py-4 text-center text-xs text-muted-foreground">
              <p>{r.error.message || at("error", "Could not load the data")}</p>
              <button type="button" className="underline" onClick={r.retry}>
                {at("retry", "Retry")}
              </button>
            </div>
          )}
          {r.state === "ready" &&
            (r.value.items.length === 0 ? (
              <p className="py-6 text-center text-xs text-muted-foreground">
                {r.value.reason ? `${at("memory.noData", "no data")}: ${reasonText(at, r.value.reason, r.value.coverage?.detail_since ?? null)}` : at("memory.detail.empty", "Nothing here for this filter")}
              </p>
            ) : (
              <ul className="divide-y divide-border">
                {r.value.items.map((it) => {
                  const facts = [
                    ...it.extra,
                    [it.kind, it.scope].filter(Boolean).join(" · "),
                    it.useful != null ? at("memory.detail.useful", "useful {{n}}", { n: num(it.useful) }) : "",
                    it.at ? fmt.formatDate(it.at) : "",
                  ].filter(Boolean);
                  return (
                    <li key={it.id} data-detail-item className="flex items-start justify-between gap-3 py-1.5 text-xs">
                      <div className="min-w-0">
                        {it.label && it.key && wsSlug ? (
                          <a href={`/w/${wsSlug}/memories?q=${encodeURIComponent(it.key)}`} className="block break-all text-primary hover:underline">
                            {it.label}
                          </a>
                        ) : it.label ? (
                          <span className="block break-all">{it.label}</span>
                        ) : (
                          <span className="block break-all italic text-muted-foreground">
                            {it.redacted ? at("memory.detail.redacted", "hidden from you") : it.reason ? reasonText(at, it.reason, null) : at("memory.noData", "no data")}
                          </span>
                        )}
                        {facts.length > 0 && <span className="block text-[11px] text-muted-foreground">{facts.join(" · ")}</span>}
                      </div>
                      {it.count != null && <span className="shrink-0 font-semibold tabular-nums">{num(it.count)}</span>}
                    </li>
                  );
                })}
              </ul>
            ))}
        </div>
        {r.state === "ready" && pages > 1 && (
          <div className="flex items-center justify-between text-xs">
            <button type="button" disabled={page <= 1} onClick={() => setPage((p) => p - 1)} className="rounded-md border border-border px-2 py-1 disabled:opacity-50">
              {at("memory.detail.prev", "Previous")}
            </button>
            <span className="text-muted-foreground">
              {page} / {pages} · {num(r.value.total)}
            </span>
            <button type="button" disabled={page >= pages} onClick={() => setPage((p) => p + 1)} className="rounded-md border border-border px-2 py-1 disabled:opacity-50">
              {at("memory.detail.next", "Next")}
            </button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

export type { MemoryGroup };
