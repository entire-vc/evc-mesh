import { api } from "@/lib/api";

/**
 * "My Runner": the signed-in user's own runtime document and the agents they
 * operate. Paths follow internal/handler/user_runtime_handler.go; every route is
 * user-auth only and returns the caller's data alone.
 */

export const CONFIG_SECTIONS = ["accounts", "pools", "profiles", "agents"] as const;
export type ConfigSection = (typeof CONFIG_SECTIONS)[number];

export type RuntimeConfig = Partial<Record<ConfigSection, Record<string, unknown>>>;

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
