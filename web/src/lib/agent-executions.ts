import { api } from "@/lib/api";

// Read-only client model for the scoped batch projection of
// GET /api/v1/workspaces/:ws/agent-executions (schema_version 1, Mesh Doc
// runner-execution-observation-v1 §3). `configured` is policy, `attempts` are
// confirmed runs; nothing here falls back from one to the other, and a missing
// observation stays null ("unknown") instead of borrowing the agent profile.

export type AttemptState = "starting" | "running" | "waiting" | "stopped" | "completed" | "failed";
export type Freshness = "unknown" | "current" | "stale" | "offline";

export interface ConfiguredRoute {
  binding_ref: string;
  controller_ref: string;
  profile_ref: string;
  harness: string | null;
  model: string | null;
  effort: string | null;
  order: number;
}

export interface ExecutionAttempt {
  attempt_id: string;
  turn_id: string | null;
  task_id: string | null;
  binding_ref: string | null;
  controller_ref: string;
  instance_id: string | null;
  generation: number | null;
  sequence: number | null;
  state: AttemptState;
  freshness: Freshness;
  policy_matches: boolean | null;
  resolved_route: {
    source: string;
    revision: number | null;
    digest: string | null;
    profile_ref: string | null;
    harness: string | null;
    model: string | null;
    effort: string | null;
  } | null;
  /** Adapter-confirmed values only; null model/effort = not observed for this turn. */
  observed: {
    harness: string | null;
    model: string | null;
    effort: string | null;
    source: string | null;
    observed_at: string | null;
  };
  heartbeat_at: string | null;
  received_at: string | null;
}

export interface AgentExecutionView {
  workspace_id: string;
  project_id: string;
  agent_id: string;
  managed: boolean;
  enabled: boolean;
  management_source: string | null;
  configured: {
    source: string;
    revision: number | null;
    digest: string | null;
    routes: ConfiguredRoute[];
  } | null;
  attempts: ExecutionAttempt[];
  profile: { source: string; harness: string | null; model: string | null } | null;
  freshness: Freshness;
}

export type ExecutionLoad =
  | { kind: "ok"; items: AgentExecutionView[]; partial: boolean; rejected: number }
  | { kind: "forbidden" }
  | { kind: "error" };

const STATES = new Set<string>(["starting", "running", "waiting", "stopped", "completed", "failed"]);
const FRESHNESS = new Set<string>(["unknown", "current", "stale", "offline"]);

const str = (v: unknown): string | null => (typeof v === "string" && v !== "" ? v : null);
const num = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) ? v : null);
const rec = (v: unknown): Record<string, unknown> | null =>
  v !== null && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
const fresh = (v: unknown): Freshness => (typeof v === "string" && FRESHNESS.has(v) ? (v as Freshness) : "unknown");

function normalizeAttempt(raw: unknown): ExecutionAttempt | null {
  const a = rec(raw);
  if (!a) return null;
  const attemptId = str(a.attempt_id);
  const state = str(a.state);
  if (!attemptId || !state || !STATES.has(state)) return null;
  const obs = rec(a.observed);
  const route = rec(a.resolved_route);
  return {
    attempt_id: attemptId,
    turn_id: str(a.turn_id),
    task_id: str(a.task_id),
    binding_ref: str(a.binding_ref),
    controller_ref: str(a.controller_ref) ?? "",
    instance_id: str(a.instance_id),
    generation: num(a.generation),
    sequence: num(a.sequence),
    state: state as AttemptState,
    freshness: fresh(a.freshness),
    policy_matches: typeof a.policy_matches === "boolean" ? a.policy_matches : null,
    resolved_route: route && {
      source: str(route.source) ?? "",
      revision: num(route.revision),
      digest: str(route.digest),
      profile_ref: str(route.profile_ref),
      harness: str(route.harness),
      model: str(route.model),
      effort: str(route.effort),
    },
    observed: {
      harness: str(obs?.harness),
      model: str(obs?.model),
      effort: str(obs?.effort),
      source: str(obs?.source),
      observed_at: str(obs?.observed_at ?? a.model_observed_at),
    },
    heartbeat_at: str(a.heartbeat_at),
    received_at: str(a.received_at),
  };
}

function normalizeRoute(raw: unknown): ConfiguredRoute | null {
  const r = rec(raw);
  if (!r) return null;
  return {
    binding_ref: str(r.binding_ref) ?? "",
    controller_ref: str(r.controller_ref) ?? "",
    profile_ref: str(r.profile_ref) ?? "",
    harness: str(r.harness),
    model: str(r.model),
    effort: str(r.effort),
    order: num(r.order) ?? 0,
  };
}

export function normalizeExecutionView(raw: unknown): AgentExecutionView | null {
  const v = rec(raw);
  if (!v) return null;
  const projectId = str(v.project_id);
  const agentId = str(v.agent_id);
  if (!projectId || !agentId || typeof v.managed !== "boolean") return null;
  const cfg = rec(v.configured);
  const profile = rec(v.profile);
  return {
    workspace_id: str(v.workspace_id) ?? "",
    project_id: projectId,
    agent_id: agentId,
    managed: v.managed,
    enabled: v.enabled !== false,
    management_source: str(v.management_source),
    configured: cfg && {
      source: str(cfg.source) ?? "",
      revision: num(cfg.revision),
      digest: str(cfg.digest),
      routes: (Array.isArray(cfg.routes) ? cfg.routes : [])
        .map(normalizeRoute)
        .filter((r): r is ConfiguredRoute => r !== null)
        .sort((a, b) => a.order - b.order),
    },
    attempts: (Array.isArray(v.attempts) ? v.attempts : [])
      .map(normalizeAttempt)
      .filter((a): a is ExecutionAttempt => a !== null),
    profile: profile && { source: str(profile.source) ?? "agent_profile", harness: str(profile.harness), model: str(profile.model) },
    freshness: fresh(v.freshness),
  };
}

const PAGE_LIMIT = 200;
const MAX_PAGES = 25;

/**
 * Walks next_cursor to the end so pagination never drops attempts. A failure on
 * the first page is an error (never "unmanaged"); a failure on a later page
 * keeps what was read and marks the result partial.
 */
export async function fetchAgentExecutions(
  workspaceId: string,
  filter: { projectId?: string | null; agentId?: string | null } = {},
): Promise<ExecutionLoad> {
  const items: AgentExecutionView[] = [];
  let rejected = 0;
  let cursor: string | null = null;
  for (let page = 0; page < MAX_PAGES; page++) {
    const q = new URLSearchParams({ limit: String(PAGE_LIMIT) });
    if (filter.projectId) q.set("project_id", filter.projectId);
    if (filter.agentId) q.set("agent_id", filter.agentId);
    if (cursor) q.set("cursor", cursor);
    let body: unknown;
    try {
      body = await api<unknown>(`/api/v1/workspaces/${workspaceId}/agent-executions?${q.toString()}`);
    } catch (err) {
      if (page > 0) return { kind: "ok", items, partial: true, rejected };
      return { kind: (err as { status?: number } | null)?.status === 403 ? "forbidden" : "error" };
    }
    const b = rec(body);
    if (!b || b.schema_version !== 1 || !Array.isArray(b.items)) {
      return page > 0 ? { kind: "ok", items, partial: true, rejected } : { kind: "error" };
    }
    for (const raw of b.items) {
      const v = normalizeExecutionView(raw);
      if (v) items.push(v);
      else rejected++;
    }
    cursor = str(b.next_cursor);
    if (!cursor) return { kind: "ok", items, partial: rejected > 0, rejected };
  }
  // Cursor never ended: keep what we have, but say it is incomplete.
  return { kind: "ok", items, partial: true, rejected };
}

// ---------------------------------------------------------------------------
// Derived view state
// ---------------------------------------------------------------------------

const ACTIVE_STATES = new Set<AttemptState>(["starting", "running", "waiting"]);

/** Running for the operator: live state and not lost. Offline/ended attempts are history. */
export function isActiveAttempt(a: ExecutionAttempt): boolean {
  return ACTIVE_STATES.has(a.state) && a.freshness !== "offline";
}

export type RouteMode = "managed" | "disabled" | "unmanaged";

export function routeMode(v: AgentExecutionView): RouteMode {
  if (!v.managed) return "unmanaged";
  return v.enabled ? "managed" : "disabled";
}

/** Observed model only; never the configured route or the agent profile. */
export function observedModel(a: ExecutionAttempt): string | null {
  return a.observed.model;
}

export function modelEffort(model: string | null, effort: string | null): string | null {
  if (!model) return null;
  return effort ? `${model} · ${effort}` : model;
}

export interface AgentExecutionSummary {
  agentId: string;
  /** One entry per project view; the chart never collapses them into one model. */
  views: AgentExecutionView[];
  mode: RouteMode;
  /** True when views disagree (e.g. managed in one project, unmanaged in another). */
  mixed: boolean;
  configuredRevisions: number[];
  /** Distinct policy sources of managed views, shown next to the revision. */
  configuredSources: string[];
  /** Distinct configured primary routes (order 0), in first-seen order. */
  configured: { harness: string | null; model: string | null; effort: string | null }[];
  active: { view: AgentExecutionView; attempt: ExecutionAttempt }[];
  historyCount: number;
  /** Profile shown only when no managed view exists for the agent. */
  profile: AgentExecutionView["profile"];
}

export function summarizeByAgent(items: AgentExecutionView[]): Map<string, AgentExecutionSummary> {
  const by = new Map<string, AgentExecutionView[]>();
  for (const v of items) by.set(v.agent_id, [...(by.get(v.agent_id) ?? []), v]);
  const out = new Map<string, AgentExecutionSummary>();
  for (const [agentId, views] of by) {
    const modes = views.map(routeMode);
    const managedViews = views.filter((v) => routeMode(v) === "managed");
    const mode: RouteMode = modes.includes("managed") ? "managed" : modes.includes("disabled") ? "disabled" : "unmanaged";
    const configured: AgentExecutionSummary["configured"] = [];
    const revisions = new Set<number>();
    const sources = new Set<string>();
    for (const v of managedViews) {
      if (v.configured?.revision != null) revisions.add(v.configured.revision);
      if (v.configured?.source) sources.add(v.configured.source);
      const primary = v.configured?.routes[0];
      if (!primary) continue;
      const key = `${primary.harness}|${primary.model}|${primary.effort}`;
      if (!configured.some((c) => `${c.harness}|${c.model}|${c.effort}` === key)) {
        configured.push({ harness: primary.harness, model: primary.model, effort: primary.effort });
      }
    }
    const active: AgentExecutionSummary["active"] = [];
    let history = 0;
    for (const v of views) {
      for (const attempt of v.attempts) {
        if (isActiveAttempt(attempt)) active.push({ view: v, attempt });
        else history++;
      }
    }
    out.set(agentId, {
      agentId,
      views,
      mode,
      mixed: new Set(modes).size > 1,
      configuredRevisions: [...revisions].sort((a, b) => a - b),
      configuredSources: [...sources],
      configured,
      active,
      historyCount: history,
      profile: managedViews.length === 0 ? (views.find((v) => v.profile)?.profile ?? null) : null,
    });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Detail list: filters, search, sort
// ---------------------------------------------------------------------------

export type AttemptScope = "all" | "active" | "history";

export interface AttemptRow {
  view: AgentExecutionView;
  attempt: ExecutionAttempt;
  active: boolean;
}

export function attemptRows(items: AgentExecutionView[]): AttemptRow[] {
  return items.flatMap((view) => view.attempts.map((attempt) => ({ view, attempt, active: isActiveAttempt(attempt) })));
}

export function filterAttemptRows(
  rows: AttemptRow[],
  opts: { projectId: string | null; scope: AttemptScope; query: string },
): AttemptRow[] {
  const q = opts.query.trim().toLowerCase();
  return rows.filter((r) => {
    if (opts.projectId && r.view.project_id !== opts.projectId) return false;
    if (opts.scope === "active" && !r.active) return false;
    if (opts.scope === "history" && r.active) return false;
    if (!q) return true;
    const a = r.attempt;
    return [a.attempt_id, a.turn_id, a.task_id, a.controller_ref, a.observed.model, a.observed.harness, a.state]
      .some((x) => x?.toLowerCase().includes(q));
  });
}

/** Active first, then newest observation; stable on attempt_id. */
export function sortAttemptRows(rows: AttemptRow[]): AttemptRow[] {
  const t = (r: AttemptRow) => Date.parse(r.attempt.heartbeat_at ?? r.attempt.received_at ?? "") || 0;
  return [...rows].sort(
    (a, b) => Number(b.active) - Number(a.active) || t(b) - t(a) || a.attempt.attempt_id.localeCompare(b.attempt.attempt_id),
  );
}
