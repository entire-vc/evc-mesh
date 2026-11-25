// The settings of the statistics page: view, breakdown, «inside», token kind, period, limit window and filter chips.
// Wide: a panel in rows. Narrow (393): one strip of compact selects that scrolls sideways inside itself, so the
// page never does; active filters wrap in their own row under it, always on screen.
import { useEffect, useRef, useState, type ComponentProps } from "react";
import { Plus, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { Select } from "@/components/ui/select";
import { useAgentStore } from "@/stores/agent";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import * as fmt from "@/i18n/format";
import { stepWord, tokenKindLabel, type ST } from "./format";
import { dimensionLabel, filterLabel, viewLabel, windowLabel } from "./labels";
import { Segmented } from "./parts";
import { allowedDimensions } from "./stats-body";
import { FILTER_KEYS, PRESETS, TOKEN_KINDS, TZ, VIEWS, WINDOWS, parsePeriod, periodDays, periodParam, withoutFilter, type StatsPeriod, type StatsState, type StatsView } from "./state";
import type { FilterKey, UsageCapabilities, UsageDimension, UsageStep } from "./types";

export function availableViews(caps: UsageCapabilities | undefined): StatsView[] {
  return VIEWS.filter((v) => {
    if (v === "quota") return caps?.quota === true;
    if (v === "cost") return caps ? caps.cost : true;
    return true;
  });
}

/** The standard Select in a settings row: keeps its own width (never squeezed by the row), a pill in the narrow strip. */
function Pick({ compact, className, ...props }: ComponentProps<typeof Select> & { compact?: boolean }) {
  return (
    <div className="shrink-0">
      <Select {...props} className={cn("w-auto", compact && "rounded-full", className)} />
    </div>
  );
}

const presetLabel = (p: string, st: ST) => (p === "1d" ? st("period.1d", "1 d") : p === "7d" ? st("period.7d", "7 d") : st("period.30d", "30 d"));

/** Days of the period in the query's zone (presets end today), and the step the server picks for it (`auto`). */
export function periodRange(p: StatsPeriod, now = new Date()): { from: string; to: string; step: UsageStep } {
  const today = fmt.calendarDayIn(TZ, now);
  const days = periodDays(p);
  const step: UsageStep = days <= 2 ? "hour" : days <= 60 ? "day" : "week";
  if (typeof p !== "string") return { ...p, step };
  const from = new Date(Date.parse(`${today}T00:00:00Z`) - (days - 1) * 86_400_000).toISOString().slice(0, 10);
  return { from, to: today, step };
}

function rangeText(p: StatsPeriod, st: ST): string {
  const r = periodRange(p);
  const d = (x: string) => fmt.formatDate(`${x}T12:00:00Z`, { day: "numeric", month: "long" });
  const span = r.from === r.to ? d(r.from) : `${d(r.from)} – ${d(r.to)}`;
  return st("period.describe", "{{span}}, step: {{step}}, Moscow time", { span, step: stepWord(r.step, st) });
}

function PeriodPicker({ value, onChange, st, compact }: { value: StatsPeriod; onChange: (p: StatsPeriod) => void; st: ST; compact?: boolean }) {
  const [custom, setCustom] = useState(typeof value !== "string");
  const r = periodRange(value);
  const [from, setFrom] = useState(r.from);
  const [to, setTo] = useState(r.to);
  useEffect(() => {
    const x = periodRange(value);
    setFrom(x.from);
    setTo(x.to);
    if (typeof value !== "string") setCustom(true);
  }, [value]);
  const apply = (a: string, b: string) => {
    const p = parsePeriod(`${a}..${b}`);
    if (typeof p !== "string") onChange(p);
  };
  if (compact) {
    return (
      <div className="flex shrink-0 items-center gap-2">
        <Pick
          compact
          aria-label={st("period.label", "Period")}
          value={typeof value === "string" ? value : "custom"}
          onChange={(e) => { if (e.target.value === "custom") setCustom(true); else { setCustom(false); onChange(e.target.value as StatsPeriod); } }}
        >
          {PRESETS.map((p) => (
            <option key={p} value={p}>
              {presetLabel(p, st)}
            </option>
          ))}
          <option value="custom">{typeof value === "string" ? st("period.pick", "Pick…") : periodParam(value).replace("..", " – ")}</option>
        </Pick>
        {custom && <>
          <input type="date" aria-label={st("period.from", "From")} className="h-9 rounded-full border border-border bg-background px-2 text-sm" value={from} max={to} onChange={(e) => { setFrom(e.target.value); apply(e.target.value, to); }} />
          <input type="date" aria-label={st("period.to", "To")} className="h-9 rounded-full border border-border bg-background px-2 text-sm" value={to} min={from} onChange={(e) => { setTo(e.target.value); apply(from, e.target.value); }} />
        </>}
      </div>
    );
  }
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2">
      <Segmented
        label={st("period.label", "Period")}
        value={custom ? ("custom" as const) : typeof value === "string" ? value : null}
        options={[...PRESETS.map((p) => ({ id: p, label: presetLabel(p, st) })), { id: "custom" as const, label: st("period.pick", "Pick…") }]}
        onChange={(v) => {
          if (v === "custom") setCustom(true);
          else {
            setCustom(false);
            onChange(v);
          }
        }}
      />
      {custom && (
        <span className="flex items-center gap-1 text-sm">
          <input type="date" aria-label={st("period.from", "From")} className="h-8 rounded-md border border-border bg-background px-2" value={from} max={to} onChange={(e) => { setFrom(e.target.value); apply(e.target.value, to); }} />
          <span aria-hidden="true">–</span>
          <input type="date" aria-label={st("period.to", "To")} className="h-8 rounded-md border border-border bg-background px-2" value={to} min={from} onChange={(e) => { setTo(e.target.value); apply(from, e.target.value); }} />
        </span>
      )}
    </div>
  );
}

function useFilterNames() {
  const agents = useAgentStore((s) => s.agents);
  const projects = useProjectStore((s) => s.projects);
  return (k: FilterKey, v: string) => {
    if (k === "agent") return agents.find((a) => a.id === v)?.name ?? v;
    if (k === "project") return projects.find((p) => p.id === v)?.name ?? v;
    return v;
  };
}

function AddFilter({ onChange, st, caps }: { onChange: (fn: (s: StatsState) => StatsState) => void; st: ST; caps: UsageCapabilities | undefined }) {
  const [open, setOpen] = useState(false);
  const keys = FILTER_KEYS.filter((k) => k !== "account" || (caps?.group_by.includes("account") ?? false));
  const [key, setKey] = useState<FilterKey>("agent");
  const [value, setValue] = useState("");
  const agents = useAgentStore((s) => s.agents);
  const fetchAgents = useAgentStore((s) => s.fetchAgents);
  const projects = useProjectStore((s) => s.projects);
  const ws = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (open && ws && agents.length === 0) void fetchAgents(ws);
  }, [open, ws, agents.length, fetchAgents]);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);
  const choices: { id: string; label: string }[] | null =
    key === "agent" ? agents.map((a) => ({ id: a.id, label: a.name })) : key === "project" ? projects.map((p) => ({ id: p.id, label: p.name })) : key === "billing_mode" ? [{ id: "subscription", label: st("billing.subscription", "Subscription") }, { id: "api", label: st("billing.api", "API key") }] : null;
  const add = () => {
    const v = value.trim();
    if (!v) return;
    onChange((s) => {
      const cur = s.f[key] ?? [];
      return { ...s, f: { ...s.f, [key]: cur.includes(v) ? cur : [...cur, v] } };
    });
    setValue("");
    setOpen(false);
  };
  return (
    <div ref={ref} className="relative shrink-0">
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="inline-flex h-8 items-center gap-1 rounded-full border border-dashed border-border px-3 text-sm text-muted-foreground hover:text-foreground">
        <Plus className="h-3.5 w-3.5" />
        {st("filter.add", "filter")}
      </button>
      {open && (
        <div className="absolute left-0 z-30 mt-2 w-72 max-w-[calc(100vw-2rem)] space-y-2 rounded-lg border border-border bg-popover p-3 shadow-lg" role="dialog" aria-label={st("filter.add", "filter")}>
          <Select aria-label={st("filter.key", "Filter by")} value={key} onChange={(e) => { setKey(e.target.value as FilterKey); setValue(""); }}>
            {keys.map((k) => (
              <option key={k} value={k}>
                {filterLabel(k, st)}
              </option>
            ))}
          </Select>
          {choices ? (
            <Select aria-label={st("filter.value", "Value")} value={value} onChange={(e) => setValue(e.target.value)}>
              <option value="">{st("filter.choose", "Choose…")}</option>
              {choices.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.label}
                </option>
              ))}
            </Select>
          ) : (
            <input aria-label={st("filter.value", "Value")} className="h-9 w-full rounded-md border border-border bg-background px-2 text-sm" value={value} onChange={(e) => setValue(e.target.value)} onKeyDown={(e) => e.key === "Enter" && add()} placeholder={key === "model" ? "claude-opus-5-5" : key === "account" ? "claude/team" : "anthropic"} />
          )}
          <button type="button" className="h-8 w-full rounded-md bg-primary text-sm font-medium text-primary-foreground disabled:opacity-50" disabled={!value.trim()} onClick={add}>
            {st("filter.apply", "Apply")}
          </button>
        </div>
      )}
    </div>
  );
}

const activeFilters = (state: StatsState) => FILTER_KEYS.flatMap((k) => (state.f[k] ?? []).map((v) => ({ k, v })));

/** `fill`: the chips have a row of their own (narrow), so a chip takes up to the row's width instead of 16rem. */
function FilterChips({ state, onChange, st, fill }: { state: StatsState; onChange: (fn: (s: StatsState) => StatsState) => void; st: ST; fill?: boolean }) {
  const name = useFilterNames();
  const chips = activeFilters(state);
  if (!chips.length) return <span className="inline-flex h-8 shrink-0 items-center rounded-full bg-primary/10 px-3 text-sm font-medium text-primary">{st("filter.all", "All projects")}</span>;
  return (
    <>
      {chips.map(({ k, v }) => (
        <span key={`${k}:${v}`} data-testid="filter-chip" className={cn("inline-flex h-8 min-w-0 shrink-0 items-center gap-1 rounded-full bg-primary/10 pl-3 pr-1 text-sm text-primary", fill ? "max-w-full" : "max-w-[16rem]")}>
          <span className="truncate" title={`${filterLabel(k, st)}: ${name(k, v)}`}>
            <span className="text-primary/70">{filterLabel(k, st)}:</span> {name(k, v)}
          </span>
          <button type="button" aria-label={st("filter.remove", "Remove filter {{name}}", { name: name(k, v) })} className="rounded-full p-1 hover:bg-primary/15" onClick={() => onChange((s) => withoutFilter(s, k, v))}>
            <X className="h-3.5 w-3.5" />
          </button>
        </span>
      ))}
    </>
  );
}

export function Controls({ state, onChange, caps, st }: { state: StatsState; onChange: (s: StatsState) => void; caps: UsageCapabilities | undefined; st: ST }) {
  const views = availableViews(caps);
  const isQuota = state.m === "quota" && caps?.quota === true;
  const dims = allowedDimensions(caps, state.m);
  // Every change builds on the last state sent, not on the render's props: the URL (and so `state`) catches up a
  // render later, and two quick changes in a row must both land instead of the second undoing the first.
  const sent = useRef(state);
  const seen = useRef(state);
  if (seen.current !== state) {
    seen.current = state;
    sent.current = state;
  }
  const update = (fn: (s: StatsState) => StatsState) => {
    sent.current = fn(sent.current);
    onChange(sent.current);
  };
  const set = (patch: Partial<StatsState>) => update((s) => ({ ...s, ...patch }));
  const setG = (g: UsageDimension) => update((s) => ({ ...s, g, s: s.s === g ? null : s.s }));
  const accountHidden = !isQuota && caps?.quota === true && !caps.group_by.includes("account");
  // Narrow: an active filter changes what every number below means, so it never hides past the strip's edge.
  const filtered = !isQuota && activeFilters(state).length > 0;

  const dimSelect = (compact: boolean) => (
    <label className={cn("flex shrink-0 items-center gap-2", compact && "contents")}>
      {!compact && <span className="text-sm text-muted-foreground">{st("ctl.group", "Breakdown")}</span>}
      <Pick compact={compact} aria-label={st("ctl.group", "Breakdown")} data-testid="group-select" value={state.g} onChange={(e) => setG(e.target.value as UsageDimension)}>
        {dims.map((d) => (
          <option key={d} value={d}>
            {compact ? st("ctl.groupChip", "Breakdown: {{dim}}", { dim: dimensionLabel(d, st).toLowerCase() }) : dimensionLabel(d, st)}
          </option>
        ))}
      </Pick>
    </label>
  );
  const stackSelect = (compact: boolean) => (
    <label className={cn("flex shrink-0 items-center gap-2", compact && "contents")}>
      {!compact && <span className="text-sm text-muted-foreground">{st("ctl.stack", "Inside")}</span>}
      <Pick compact={compact} aria-label={st("ctl.stack", "Inside")} data-testid="stack-select" value={state.s ?? ""} onChange={(e) => set({ s: (e.target.value || null) as UsageDimension | null })}>
        <option value="">{st("ctl.noStack", "no second breakdown")}</option>
        {dims.filter((d) => d !== state.g).map((d) => (
          <option key={d} value={d}>
            {compact ? st("ctl.stackChip", "Inside: {{dim}}", { dim: dimensionLabel(d, st).toLowerCase() }) : dimensionLabel(d, st)}
          </option>
        ))}
      </Pick>
    </label>
  );
  const tokenSelect = (compact: boolean) => (
    <Pick compact={compact} aria-label={st("ctl.tokens", "Tokens")} data-testid="tokens-select" value={state.t} onChange={(e) => set({ t: e.target.value as StatsState["t"] })}>
      {TOKEN_KINDS.map((k) => (
        <option key={k} value={k}>
          {tokenKindLabel(k, st)}
        </option>
      ))}
    </Pick>
  );

  return (
    <>
      {/* wide */}
      <section className="hidden min-w-0 space-y-3 rounded-xl border border-border bg-card p-4 @3xl:block" data-testid="stats-controls">
        <div className="flex min-w-0 flex-wrap items-center gap-3">
          <span className="text-sm text-muted-foreground">{st("ctl.show", "Show")}</span>
          <Segmented label={st("ctl.show", "Show")} value={state.m} options={views.map((v) => ({ id: v, label: viewLabel(v, st) }))} onChange={(m) => set({ m })} />
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-x-5 gap-y-3">
          {isQuota ? (
            <>
              <span className="flex items-center gap-2">
                <span className="text-sm text-muted-foreground">{st("ctl.window", "Limit window")}</span>
                <Segmented label={st("ctl.window", "Limit window")} value={state.w} options={WINDOWS.map((w) => ({ id: w, label: windowLabel(w, st) }))} onChange={(w) => set({ w })} />
              </span>
              <span className="flex items-center gap-2">
                <span className="text-sm text-muted-foreground">{st("ctl.group", "Breakdown")}</span>
                <Pick aria-label={st("ctl.group", "Breakdown")} disabled>
                  <option>{dimensionLabel("account", st)}</option>
                </Pick>
              </span>
            </>
          ) : (
            <>
              {dimSelect(false)}
              {stackSelect(false)}
              {state.m === "tokens" && tokenSelect(false)}
            </>
          )}
          <span className="flex min-w-0 items-center gap-2">
            <span className="text-sm text-muted-foreground">{st("period.label", "Period")}</span>
            <PeriodPicker value={state.p} onChange={(p) => set({ p })} st={st} />
          </span>
          <span className="text-xs text-muted-foreground" data-testid="period-describe">{rangeText(state.p, st)}</span>
        </div>
        {isQuota ? (
          <p className="text-xs text-muted-foreground">{st("ctl.quotaHint", "Limits belong to an account, so breakdowns by agent and project are not available here")}</p>
        ) : (
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">{st("ctl.filter", "Filter")}</span>
            <FilterChips state={state} onChange={update} st={st} />
            <AddFilter onChange={update} st={st} caps={caps} />
            {accountHidden && <span className="text-xs text-muted-foreground">{st("ctl.accountHint", "The «Pool / account» breakdown appears once the runner is connected and reports the account")}</span>}
          </div>
        )}
      </section>

      {/* narrow: one strip that scrolls sideways inside itself */}
      <section className="-mx-3 min-w-0 @3xl:hidden" data-testid="stats-chips">
        <div className="relative flex min-w-0 items-center gap-2 overflow-x-auto px-3 pb-1 [scrollbar-width:none]">
          <Pick compact aria-label={st("ctl.show", "Show")} data-testid="view-select" className="font-medium" value={state.m} onChange={(e) => set({ m: e.target.value as StatsView })}>
            {views.map((v) => (
              <option key={v} value={v}>
                {viewLabel(v, st)}
              </option>
            ))}
          </Pick>
          {isQuota ? (
            <Pick compact aria-label={st("ctl.window", "Limit window")} value={state.w} onChange={(e) => set({ w: e.target.value as StatsState["w"] })}>
              {WINDOWS.map((w) => (
                <option key={w} value={w}>
                  {windowLabel(w, st)}
                </option>
              ))}
            </Pick>
          ) : (
            <>
              {dimSelect(true)}
              {stackSelect(true)}
              {state.m === "tokens" && tokenSelect(true)}
            </>
          )}
          <PeriodPicker value={state.p} onChange={(p) => set({ p })} st={st} compact />
          {!isQuota && !filtered && <FilterChips state={state} onChange={update} st={st} />}
          {!isQuota && <AddFilter onChange={update} st={st} caps={caps} />}
        </div>
        {filtered && (
          <div className="flex min-w-0 flex-wrap items-center gap-2 px-3 pt-2" data-testid="active-filters" role="group" aria-label={st("ctl.filter", "Filter")}>
            <FilterChips state={state} onChange={update} st={st} fill />
          </div>
        )}
      </section>
    </>
  );
}
