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
    /** The controller's own build version, when it sends one. */
    version?: string;
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

export interface QuotaWindow {
  pool: string;
  window: string;
  /** null = the provider gave no number; it is never shown as 0 %. */
  usedFraction: number | null;
  observedAt: string;
  stale: boolean;
}

export type QuotaLoad =
  | { kind: "ok"; windows: QuotaWindow[] }
  | { kind: "forbidden" }
  | { kind: "unavailable" };

// Date.parse alone accepts a bare year; the API contract is an RFC 3339 timestamp.
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;
// Date.parse also normalizes Feb 30 into March 2: require the calendar fields to round-trip.
function isValidRfc3339(v: string): boolean {
  if (!RFC3339.test(v) || Number.isNaN(Date.parse(v))) return false;
  const [y = NaN, mo = NaN, d = NaN, h = NaN, mi = NaN, sec = NaN] = v.slice(0, 19).split(/[-T:]/).map(Number);
  const c = new Date(Date.UTC(y, mo - 1, d, h, mi, sec));
  return c.getUTCFullYear() === y && c.getUTCMonth() === mo - 1 && c.getUTCDate() === d
    && c.getUTCHours() === h && c.getUTCMinutes() === mi && c.getUTCSeconds() === sec;
}

/** HH:MMZ in UTC, whatever offset the server sent. */
export function formatObservedAtUtc(iso: string): string {
  return `${new Date(iso).toISOString().slice(11, 16)}Z`;
}

const QUOTA_STATUSES = ["available", "exhausted", "unknown", "auth_error", "network_error"];

// GET .../controllers/:ref/quota-observations (RuntimeQuotaList). One malformed
// record makes the whole list null: a partial list would read as "no data".
export function normalizeQuotaObservations(raw: unknown): QuotaWindow[] | null {
  if (!isObject(raw) || !Array.isArray(raw.observations)) return null;
  const out: QuotaWindow[] = [];
  for (const o of raw.observations) {
    if (!isObject(o) || typeof o.pool !== "string" || typeof o.window !== "string") return null;
    if (typeof o.provider !== "string" || typeof o.status !== "string" || !QUOTA_STATUSES.includes(o.status)) return null;
    if (typeof o.stale !== "boolean" || typeof o.observed_at !== "string" || !isValidRfc3339(o.observed_at)) return null;
    const f = o.used_fraction;
    if (f !== null && (typeof f !== "number" || !Number.isFinite(f) || f < 0 || f > 1)) return null;
    // Same rule as the server: a status without data carries no measurement.
    if (f !== null && o.status !== "available" && o.status !== "exhausted") return null;
    out.push({
      pool: o.pool,
      window: o.window,
      usedFraction: f,
      observedAt: o.observed_at,
      stale: o.stale,
    });
  }
  return out;
}

export async function fetchQuotaObservations(wsId: string, controllerRef: string): Promise<QuotaLoad> {
  try {
    const windows = normalizeQuotaObservations(
      await api<unknown>(`/api/v1/workspaces/${wsId}/runtime/controllers/${controllerRef}/quota-observations`),
    );
    return windows ? { kind: "ok", windows } : { kind: "unavailable" };
  } catch (err) {
    return { kind: (err as { status?: number } | null)?.status === 403 ? "forbidden" : "unavailable" };
  }
}

/** "7 %" for a number (0 stays "0 %"), "unknown" for null; a stale reading is not a current one. */
export function formatUsedFraction(w: Pick<QuotaWindow, "usedFraction" | "stale">): string {
  const pct = w.usedFraction === null ? "unknown" : `${Math.round(w.usedFraction * 1000) / 10} %`;
  return w.stale ? `${pct} (stale)` : pct;
}

export interface RuntimePreview {
  selectedProfileId: string;
  selectedAccountRef: string;
  preferredAccountRef: string;
  reason: string;
  trace: { profileId: string; reason: string }[];
  revision: number;
  qaMode: string;
  qaCompromise: boolean;
}

export type PreviewResult =
  | { kind: "ok"; preview: RuntimePreview }
  | { kind: "forbidden" }
  | { kind: "stale" }
  | { kind: "unavailable" };

const str = (v: unknown): string => (typeof v === "string" ? v : "");

/**
 * POST .../preview body → typed preview. The always-present contract fields
 * (reason, trace[], revision, launch_authorized=false, qa_compromise) must all
 * have their type (a preview that claims a launch is authorized is not the contract); a partial body or a malformed trace entry is null, never a result.
 */
export function normalizePreview(raw: unknown): RuntimePreview | null {
  if (
    !isObject(raw) ||
    typeof raw.reason !== "string" ||
    !Array.isArray(raw.trace) ||
    typeof raw.revision !== "number" ||
    raw.launch_authorized !== false ||
    typeof raw.qa_compromise !== "boolean"
  ) {
    return null;
  }
  for (const k of ["selected_profile_id", "selected_account_ref", "preferred_account_ref", "qa_mode"]) {
    if (raw[k] !== undefined && typeof raw[k] !== "string") return null;
  }
  const trace: RuntimePreview["trace"] = [];
  for (const t of raw.trace) {
    if (!isObject(t) || typeof t.profile_id !== "string" || typeof t.reason !== "string") return null;
    trace.push({ profileId: t.profile_id, reason: t.reason });
  }
  return {
    selectedProfileId: str(raw.selected_profile_id),
    selectedAccountRef: str(raw.selected_account_ref),
    preferredAccountRef: str(raw.preferred_account_ref),
    reason: raw.reason,
    trace,
    revision: raw.revision,
    qaMode: str(raw.qa_mode),
    qaCompromise: raw.qa_compromise,
  };
}

/**
 * Advisory "which route would the router pick now and why" (R1 preview/explain).
 * It launches nothing and reserves nothing. 403 = not allowed to see it, 409 =
 * the catalog changed under us (reload and ask again); no failure is shown as a result.
 */
export async function previewBinding(
  workspaceId: string,
  bindingRef: string,
  purpose: "new_launch" | "quota_reserve" = "new_launch",
): Promise<PreviewResult> {
  try {
    const raw = await api<unknown>(
      `/api/v1/workspaces/${workspaceId}/runtime/bindings/${encodeURIComponent(bindingRef)}/preview?resource_owner_workspace_id=${workspaceId}`,
      { method: "POST", body: { purpose, required_capabilities: [] } },
    );
    const preview = normalizePreview(raw);
    return preview ? { kind: "ok", preview } : { kind: "unavailable" };
  } catch (err) {
    const status = (err as { status?: number } | null)?.status;
    if (status === 403) return { kind: "forbidden" };
    if (status === 409) return { kind: "stale" };
    return { kind: "unavailable" };
  }
}

export type SavePolicyResult =
  | { kind: "ok"; revision: number }
  | { kind: "forbidden" }
  | { kind: "stale" }
  | { kind: "invalid"; message: string }
  | { kind: "unavailable" };

/**
 * PUT .../bindings/:id/policy (R1b). The server edits only this binding inside the
 * stored catalog, so credential refs never travel. `ifRevision` is the catalog
 * revision the edit started from: a conflict (409) is reported, never retried
 * or merged. Success needs a numeric `revision` in the body, a bare 200 is not one.
 */
export async function saveBindingPolicy(
  workspaceId: string,
  bindingRef: string,
  ifRevision: number,
  permittedProfiles: string[],
  policy: RuntimeRoutingPolicy,
): Promise<SavePolicyResult> {
  try {
    const raw = await api<unknown>(
      `/api/v1/workspaces/${workspaceId}/runtime/bindings/${encodeURIComponent(bindingRef)}/policy?resource_owner_workspace_id=${workspaceId}`,
      { method: "PUT", body: { if_revision: ifRevision, permitted_profiles: permittedProfiles, policy } },
    );
    return isObject(raw) && typeof raw.revision === "number"
      ? { kind: "ok", revision: raw.revision }
      : { kind: "unavailable" };
  } catch (err) {
    const e = err as { status?: number; message?: string } | null;
    if (e?.status === 403) return { kind: "forbidden" };
    if (e?.status === 409) return { kind: "stale" };
    if (e?.status === 400 || e?.status === 422) return { kind: "invalid", message: e.message ?? "" };
    return { kind: "unavailable" };
  }
}
