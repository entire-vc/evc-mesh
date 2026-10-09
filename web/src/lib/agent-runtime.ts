import { api } from "@/lib/api";

// Read-only client model for docs/api/agent-runtime.md (schema v2). Nothing here
// launches, reserves or switches accounts: the UI shows desired catalog, what a
// controller reported as applied, and the route order derived from the policy.

export type ExecutionMode = "subscription" | "api";
export type QaMode = "off" | "prefer" | "strict";

export interface RuntimeController {
  host: string;
  version: string;
  reporter_grant_id: string;
  capabilities: string[];
  enabled: boolean;
  heartbeat_max_age_seconds: number;
}

export interface RuntimeAccount {
  provider: string;
  credential_ref: string;
  quota_pools_by_mode: Partial<Record<ExecutionMode, string[]>>;
}

export interface RuntimePool {
  provider: string;
  resource_ref: string;
  aliases: string[];
  max_concurrency: number;
  threshold_ref?: string;
}

export interface RuntimeProfile {
  controller_ref: string;
  account_ref: string;
  harness: string;
  model: string;
  model_developer: string;
  model_family: string;
  execution_mode: ExecutionMode;
  capabilities: string[];
  enabled: boolean;
}

export interface RuntimeRoutingPolicy {
  primary_profiles: string[];
  quota_edges: Record<string, string[]>;
  preferred_accounts: Record<string, string>;
  api_reserve_providers: string[];
  max_attempts: number;
  evidence_max_age_seconds: number;
  qa: { mode: QaMode; quota_fallback?: boolean };
}

export interface RuntimeBinding {
  binding: { agent_id: string; workspace_id: string; grant_id: string };
  permitted_profiles: string[];
  policy: RuntimeRoutingPolicy;
  enabled: boolean;
}

export interface RuntimeCatalog {
  schema_version: 2;
  controllers: Record<string, RuntimeController>;
  accounts: Record<string, RuntimeAccount>;
  pools: Record<string, RuntimePool>;
  profiles: Record<string, RuntimeProfile>;
  bindings: Record<string, RuntimeBinding>;
}

export interface PoolObservation {
  state: string;
  observed_at: string;
  verified: boolean;
}

export interface ControllerState {
  controller_ref: string;
  report: {
    revision: number;
    digest: string;
    status: string;
    emergency_paused?: boolean;
    pools?: Record<string, PoolObservation>;
  };
  received_at: string;
  /** Server verdict: matching revision+digest, applied, fresh, grant intact. */
  current: boolean;
}

export interface RuntimeState {
  revision: number;
  digest: string;
  enabled: boolean;
  drainRequested: boolean;
  catalog: RuntimeCatalog;
  controllers: ControllerState[];
}

export type RuntimeLoad =
  | { kind: "ok"; state: RuntimeState }
  | { kind: "not_configured" }
  | { kind: "unavailable" }
  | { kind: "forbidden" };

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

// GET /workspaces/:ws/runtime (internal/domain RuntimeSnapshot): `mode` is
// "direct" (no connection, 200) or "managed" with revision/digest/config and
// controller reports. Credential refs are already stripped by the server.
export function normalizeRuntime(raw: unknown): RuntimeState | "direct" | null {
  if (!isObject(raw)) return null;
  if (raw.mode === "direct") return "direct";
  const config = raw.config;
  if (raw.mode !== "managed" || !isObject(config) || config.schema_version !== 2) return null;
  return {
    revision: typeof raw.revision === "number" ? raw.revision : 0,
    digest: typeof raw.digest === "string" ? raw.digest : "",
    enabled: raw.enabled === true,
    drainRequested: raw.drain_requested === true,
    catalog: { controllers: {}, accounts: {}, pools: {}, profiles: {}, bindings: {}, ...config } as RuntimeCatalog,
    controllers: Array.isArray(raw.controllers) ? (raw.controllers as ControllerState[]) : [],
  };
}

/**
 * 200 direct = no runtime connection (runtime facts unknown); 403 hides owner
 * data from non-admins; every other failure is "unavailable", never success.
 */
export async function fetchWorkspaceRuntime(wsId: string): Promise<RuntimeLoad> {
  try {
    const state = normalizeRuntime(await api<unknown>(`/api/v1/workspaces/${wsId}/runtime`));
    if (state === "direct") return { kind: "not_configured" };
    return state ? { kind: "ok", state } : { kind: "unavailable" };
  } catch (err) {
    return { kind: (err as { status?: number } | null)?.status === 403 ? "forbidden" : "unavailable" };
  }
}

export function findAgentBinding(
  catalog: RuntimeCatalog,
  agentId: string,
  workspaceId: string,
): { ref: string; binding: RuntimeBinding } | null {
  for (const [ref, binding] of Object.entries(catalog.bindings)) {
    if (binding.binding.agent_id === agentId && binding.binding.workspace_id === workspaceId) {
      return { ref, binding };
    }
  }
  return null;
}

const QUOTA_STATES = new Set(["exhausted", "threshold"]);
const ERROR_STATES = new Set(["auth_error", "network_error"]);

export type RouteRole = "primary" | "quota_reserve" | "api_reserve";

export interface RankedRoute {
  profileRef: string;
  profile: RuntimeProfile;
  role: RouteRole;
  rank: number;
  /** Why this entry is not currently usable; null = nothing known against it. */
  unavailable: string | null;
  /** "quota" = confirmed exhaustion/threshold (the only reason quota edges may open); "other" = anything else. */
  unavailableKind: "quota" | "other" | null;
}

/**
 * Ordered allowed routes: primary profiles first, then the quota-edge chain
 * (breadth-first, each profile once). API-mode profiles are shown as an API
 * reserve only when the provider is opted in; otherwise they are listed as
 * unavailable with the reason. Profiles outside permitted_profiles never appear.
 */
export function rankRoutes(
  catalog: RuntimeCatalog,
  b: RuntimeBinding,
  workspace: { enabled: boolean; drainRequested: boolean } = { enabled: true, drainRequested: false },
  observed: Record<string, PoolObservation> = {},
  now: number = Date.now(),
): RankedRoute[] {
  const permitted = new Set(b.permitted_profiles);
  const seen = new Set<string>();
  const out: RankedRoute[] = [];

  // Only verified, recent evidence may move the projection (policy.evidence_max_age_seconds).
  const fresh = (o: PoolObservation): boolean => {
    const age = now - Date.parse(o.observed_at);
    return o.verified && age >= -5_000 && age <= b.policy.evidence_max_age_seconds * 1000;
  };

  const reason = (ref: string, p: RuntimeProfile, role: RouteRole): { why: string; kind: "quota" | "other" } | null => {
    const other = (why: string) => ({ why, kind: "other" as const });
    if (!workspace.enabled) return other("runtime disabled");
    if (workspace.drainRequested) return other("runtime draining");
    if (!b.enabled) return other("binding disabled");
    if (!p.enabled) return other("profile disabled");
    const ctl = catalog.controllers[p.controller_ref];
    if (!ctl?.enabled) return other("controller disabled");
    if (p.execution_mode === "api" && role !== "primary") {
      const provider = catalog.accounts[p.account_ref]?.provider;
      if (!provider || !b.policy.api_reserve_providers.includes(provider)) {
        return other("API not opted in for this provider");
      }
    }
    if (!permitted.has(ref)) return other("not permitted for this agent");
    for (const pool of catalog.accounts[p.account_ref]?.quota_pools_by_mode[p.execution_mode] ?? []) {
      const o = observed[pool];
      if (!o || !fresh(o)) continue;
      if (QUOTA_STATES.has(o.state)) return { why: `pool ${pool} ${o.state}`, kind: "quota" };
      if (ERROR_STATES.has(o.state)) return other(`pool ${pool} ${o.state.replace("_", " ")}`);
    }
    return null;
  };

  const push = (ref: string, role: RouteRole) => {
    if (seen.has(ref)) return;
    const profile = catalog.profiles[ref];
    if (!profile || !permitted.has(ref)) return;
    seen.add(ref);
    const effective: RouteRole =
      role === "quota_reserve" && profile.execution_mode === "api" ? "api_reserve" : role;
    const why = reason(ref, profile, effective);
    out.push({
      profileRef: ref,
      profile,
      role: effective,
      rank: out.length + 1,
      unavailable: why?.why ?? null,
      unavailableKind: why?.kind ?? null,
    });
  };

  const queue: string[] = [];
  for (const ref of b.policy.primary_profiles) {
    push(ref, "primary");
    queue.push(ref);
  }
  while (queue.length > 0) {
    const from = queue.shift() as string;
    for (const to of b.policy.quota_edges[from] ?? []) {
      if (!seen.has(to)) {
        push(to, "quota_reserve");
        queue.push(to);
      }
    }
  }
  return out;
}

export interface PreferredActual {
  provider: string;
  preferredAccount: string;
  /** Account of the first usable route for this provider, or null if none. */
  actualAccount: string | null;
  reason: string;
}

/**
 * Preferred vs actual per provider. "Actual" here is the first usable ranked
 * route for the provider — an advisory projection of the policy, not a launch
 * decision; live quota evidence arrives through the controller report.
 */
export function preferredVsActual(
  catalog: RuntimeCatalog,
  b: RuntimeBinding,
  routes: RankedRoute[],
): PreferredActual[] {
  return Object.entries(b.policy.preferred_accounts).map(([provider, preferredAccount]) => {
    const own = routes.filter((r) => catalog.accounts[r.profile.account_ref]?.provider === provider);
    const usable = own.filter((r) => r.unavailable === null);
    const preferredUsable = usable.find((r) => r.profile.account_ref === preferredAccount);
    if (preferredUsable) {
      return { provider, preferredAccount, actualAccount: preferredAccount, reason: "preferred is usable" };
    }
    // Quota edges open only on confirmed exhaustion/threshold, never on config or auth/network faults.
    const preferredRoute = own.find((r) => r.profile.account_ref === preferredAccount);
    if (!preferredRoute) {
      return { provider, preferredAccount, actualAccount: null, reason: "no route for the preferred account" };
    }
    if (preferredRoute.unavailableKind !== "quota") {
      return {
        provider,
        preferredAccount,
        actualAccount: null,
        reason: `preferred unusable (${preferredRoute.unavailable}); reserve opens only on confirmed quota exhaustion`,
      };
    }
    const first = usable[0];
    if (first) {
      return {
        provider,
        preferredAccount,
        actualAccount: first.profile.account_ref,
        reason: "preferred quota exhausted; policy reserve",
      };
    }
    return { provider, preferredAccount, actualAccount: null, reason: "no usable route" };
  });
}

/** Desired vs applied for one controller; the server decides `current`, we explain why not. */
export function controllerSync(
  c: ControllerState | undefined,
  desiredRevision: number,
  desiredDigest: string,
): { label: "applied" | "stale" | "error" | "paused" | "unknown"; detail: string } {
  if (!c) return { label: "unknown", detail: "no report from controller" };
  const r = c.report;
  if (r.status !== "applied") return { label: "error", detail: `controller ${r.status}` };
  if (r.emergency_paused) return { label: "paused", detail: "emergency pause is on" };
  if (r.revision !== desiredRevision) {
    return { label: "stale", detail: `applied r${r.revision}, desired r${desiredRevision}` };
  }
  if (!desiredDigest || r.digest !== desiredDigest) {
    return { label: "stale", detail: "applied digest differs from desired" };
  }
  if (!c.current) return { label: "stale", detail: "report is not current (heartbeat or grant)" };
  return { label: "applied", detail: `r${desiredRevision}` };
}

/** Freshest observation per pool across controller reports the server marks current (stale/rejected/paused ones are ignored). */
export function poolObservations(controllers: ControllerState[]): Record<string, PoolObservation> {
  const out: Record<string, PoolObservation> = {};
  for (const c of controllers) {
    if (!c.current || c.report.status !== "applied" || c.report.emergency_paused) continue;
    for (const [ref, o] of Object.entries(c.report.pools ?? {})) {
      const prev = out[ref];
      if (!prev || Date.parse(o.observed_at) > Date.parse(prev.observed_at)) out[ref] = o;
    }
  }
  return out;
}
