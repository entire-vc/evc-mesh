import { api } from "@/lib/api";

/**
 * "My Runner": the signed-in user's own runtime document and the agents they
 * operate. Paths follow internal/handler/user_runtime_handler.go; every route is
 * user-auth only and returns the caller's data alone.
 */

export const CONFIG_SECTIONS = ["accounts", "pools", "profiles", "agents"] as const;
/** The workspace runner catalog (schema v2) keeps per-agent settings under `bindings`. */
export const CATALOG_SECTIONS = ["accounts", "pools", "profiles", "bindings"] as const;
export type ConfigSection = (typeof CONFIG_SECTIONS)[number] | (typeof CATALOG_SECTIONS)[number];

export type RuntimeConfig = Partial<Record<ConfigSection, Record<string, unknown>>> & Record<string, unknown>;

export interface UserRuntime {
  user_id: string;
  revision: number;
  config: RuntimeConfig | null;
  updated_at: string;
}

export interface RuntimeController {
  runner_id: string;
  name: string;
  host: string;
  version: string;
  lease_expires_at: string;
  live: boolean;
}

export interface OperatorAgent {
  agent_id: string;
  name: string;
  operator_user_id: string;
  max_concurrent_tasks: number;
  runtime_manager: boolean;
  controller: RuntimeController | null;
}

export interface OperatorRunner {
  runner_id: string;
  name: string;
  host: string;
  version: string;
  lease_expires_at: string;
  live: boolean;
  agents: { agent_id: string; name: string }[];
}

export type HistoryKind = "config" | "limits" | "manager_granted" | "manager_revoked";

export interface RuntimeHistoryEntry {
  id: number;
  revision: number;
  kind: HistoryKind;
  detail: Record<string, unknown> | null;
  author_type: "user" | "agent";
  author_id: string;
  created_at: string;
}

/** One saved revision of the workspace runner catalog (GET /workspaces/:ws/runtime/revisions). */
export interface CatalogRevision {
  revision: number;
  digest: string;
  enabled: boolean;
  author_id: string;
  author_name: string;
  created_at: string;
}

/**
 * What the Resources block edits: the workspace runner catalog when the
 * workspace has a runner connection, otherwise the user's own document.
 */
export type ResourceDoc =
  | { source: "workspace"; wsId: string; revision: number; enabled: boolean; config: RuntimeConfig }
  | { source: "personal"; revision: number; config: RuntimeConfig };

export type HistoryView =
  | { source: "workspace"; entries: CatalogRevision[] }
  | { source: "personal"; entries: RuntimeHistoryEntry[] };

export const MAX_CONCURRENT_CAP = 1000;

export const getMyRuntime = () => api<UserRuntime>("/api/v1/me/runtime");

export const saveMyRuntime = (ifRevision: number, config: RuntimeConfig) =>
  api<UserRuntime>("/api/v1/me/runtime", { method: "PUT", body: { if_revision: ifRevision, config } });

export const getMyRuntimeHistory = (limit = 50) =>
  api<{ history: RuntimeHistoryEntry[] | null }>(`/api/v1/me/runtime/history?limit=${limit}`).then((r) => r.history ?? []);

export const getOperatorAgents = () =>
  api<{ agents: OperatorAgent[] | null }>("/api/v1/me/runtime/agents").then((r) => r.agents ?? []);

export const getOperatorRunners = () =>
  api<{ runners: OperatorRunner[] | null }>("/api/v1/me/runtime/runners").then((r) => r.runners ?? []);

export const setAgentLimit = (agentId: string, maxConcurrentTasks: number) =>
  api(`/api/v1/me/runtime/agents/${agentId}/limits`, { method: "PUT", body: { max_concurrent_tasks: maxConcurrentTasks } });

export const setRuntimeManager = (agentId: string, granted: boolean) =>
  api(`/api/v1/me/runtime/managers${granted ? "" : "/revoke"}`, { method: "POST", body: { agent_id: agentId } });

const status = (e: unknown) => (e as { status?: number } | null)?.status;

/**
 * The workspace catalog when the runner connection exists ("managed"); the
 * personal document when there is none ("direct") or the user may not read it
 * (403: GET and PUT are both owner/admin). Any other failure is an error, never
 * a silent switch to a config the runner does not read.
 */
export async function loadResources(wsId: string | undefined): Promise<ResourceDoc> {
  if (wsId) {
    try {
      const raw = await api<{ mode?: string; revision?: number; enabled?: boolean; config?: unknown }>(`/api/v1/workspaces/${wsId}/runtime`);
      if (raw?.mode === "managed") {
        if (!raw.config || typeof raw.config !== "object" || Array.isArray(raw.config)) throw new Error("runtime catalog missing");
        return { source: "workspace", wsId, revision: raw.revision ?? 0, enabled: raw.enabled === true, config: raw.config as RuntimeConfig };
      }
    } catch (e) {
      if (status(e) !== 403) throw e;
    }
  }
  const doc = await getMyRuntime();
  return { source: "personal", revision: doc.revision, config: doc.config ?? {} };
}

/** Saves the whole draft against the loaded revision; the catalog keeps its enabled (drain) state. */
export function saveResources(doc: ResourceDoc, config: RuntimeConfig): Promise<unknown> {
  if (doc.source === "personal") return saveMyRuntime(doc.revision, config);
  return api(`/api/v1/workspaces/${doc.wsId}/runtime`, { method: "PUT", body: { if_revision: doc.revision, enabled: doc.enabled, config } });
}

/** Catalog revisions when the workspace has a runner connection (even with none saved yet); otherwise (no connection, no rights, older server) the personal history. */
export async function loadHistory(wsId: string | undefined, limit = 50): Promise<HistoryView> {
  if (wsId) {
    try {
      const r = await api<{ managed?: boolean; revisions: CatalogRevision[] | null }>(`/api/v1/workspaces/${wsId}/runtime/revisions?limit=${limit}`);
      if (r?.managed) return { source: "workspace", entries: r.revisions ?? [] };
    } catch (e) {
      if (status(e) !== 403 && status(e) !== 404) throw e;
    }
  }
  return { source: "personal", entries: await getMyRuntimeHistory(limit) };
}

const SUMMARY_LIMIT = 3;

/** A short line of the fields people scan for, so a row says more than its name. */
export function entrySummary(value: unknown): { text: string[]; max: number | null; disabled: boolean } {
  const v = (value && typeof value === "object" ? value : {}) as Record<string, unknown>;
  const str = (k: string) => (typeof v[k] === "string" && v[k] ? (v[k] as string) : null);
  const text: string[] = [];
  const provider = str("provider");
  if (provider) text.push(provider);
  const model = [str("harness"), str("model")].filter(Boolean).join(" · ");
  if (model) text.push(model);
  if (Array.isArray(v.permitted_profiles) && v.permitted_profiles.length) {
    const p = v.permitted_profiles.filter((x): x is string => typeof x === "string");
    text.push(p.slice(0, SUMMARY_LIMIT).join(", ") + (p.length > SUMMARY_LIMIT ? ` +${p.length - SUMMARY_LIMIT}` : ""));
  }
  return { text, max: typeof v.max_concurrency === "number" ? v.max_concurrency : null, disabled: v.enabled === false };
}

/** A parsed limit, or null when the text is not a whole number in 0..cap. */
export function parseLimit(text: string): number | null {
  const s = text.trim();
  if (!/^\d{1,4}$/.test(s)) return null;
  const n = Number(s);
  return n <= MAX_CONCURRENT_CAP ? n : null;
}

/** Entries of one config section as [name, value] pairs, sorted by name. */
export function sectionEntries(config: RuntimeConfig | null, section: ConfigSection): [string, unknown][] {
  return Object.entries(config?.[section] ?? {}).sort(([a], [b]) => a.localeCompare(b));
}

export type EntryParse = { ok: true; value: unknown } | { ok: false };

/** An entry value is any JSON object; secrets are refused by the server, not guessed at here. */
export function parseEntryValue(text: string): EntryParse {
  try {
    const v: unknown = JSON.parse(text);
    return v && typeof v === "object" && !Array.isArray(v) ? { ok: true, value: v } : { ok: false };
  } catch {
    return { ok: false };
  }
}

// Same substrings as rejectSecrets in internal/repository/postgres/user_runtime_repo.go.
const SECRET_KEY = /secret|password|token|api_key|apikey|private_key/i;

/**
 * Mirrors the server's no-secrets contract so the editor refuses early: a field
 * named like a secret, or a `credential` that is not a `cred:` reference. Returns
 * the dotted path of the first offender, or null. The server stays the authority.
 */
export function findSecretViolation(value: unknown, path = ""): string | null {
  if (Array.isArray(value)) {
    for (let i = 0; i < value.length; i++) {
      const hit = findSecretViolation(value[i], `${path}[${i}]`);
      if (hit) return hit;
    }
    return null;
  }
  if (!value || typeof value !== "object") return null;
  for (const [k, v] of Object.entries(value)) {
    const here = path ? `${path}.${k}` : k;
    if (SECRET_KEY.test(k)) return here;
    if ((k.toLowerCase() === "credential" || k.toLowerCase() === "cred") && !(typeof v === "string" && v.startsWith("cred:"))) return here;
    // The catalog hides stored references: empty keeps the stored one, anything else must be a reference.
    if (k.toLowerCase() === "credential_ref" && !(v === "" || (typeof v === "string" && v.startsWith("cred:")))) return here;
    const hit = findSecretViolation(v, here);
    if (hit) return hit;
  }
  return null;
}

/** A copy of the config with one entry set (value) or removed (value undefined). */
export function withEntry(config: RuntimeConfig | null, section: ConfigSection, name: string, value: unknown): RuntimeConfig {
  const next: RuntimeConfig = { ...(config ?? {}) };
  const entries = { ...(next[section] ?? {}) };
  if (value === undefined) delete entries[name];
  else entries[name] = value;
  next[section] = entries;
  return next;
}

/** The runner whose lease the agent holds is reachable only while the lease is live. */
export function agentState(a: OperatorAgent): "live" | "lapsed" | "none" {
  if (!a.controller) return "none";
  return a.controller.live ? "live" : "lapsed";
}
