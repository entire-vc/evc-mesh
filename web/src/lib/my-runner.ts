import { api } from "@/lib/api";
import type { FieldSchema, Values } from "@/components/extensions/schema-form";
import { resolveSchema } from "@/components/extensions/schema-form/resolve";
import type { ExtensionManifest } from "@/hooks/useExtensionCatalog";

/**
 * "My Runner": the signed-in user's own runtime document and the agents they
 * operate in the current workspace (for its owner or admin, also the agents the
 * workspace's runner drives). Paths follow internal/handler/user_runtime_handler.go;
 * every route is user-auth only.
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
  /** null: no operator; the agent is listed because the workspace's runner drives it. */
  operator_user_id: string | null;
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
  /** `manifest`: the Runner manifest whose schemas build the entry forms; null when it could not be read. */
  /** `excluded`: bindings the runner is not given, with the server's reason (absent on servers that do not report it). */
  | { source: "workspace"; wsId: string; revision: number; enabled: boolean; config: RuntimeConfig; manifest: ExtensionManifest | null; excluded: Record<string, string> }
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

export const getOperatorAgents = (workspaceId: string) =>
  api<{ agents: OperatorAgent[] | null }>(`/api/v1/me/runtime/agents?workspace_id=${encodeURIComponent(workspaceId)}`).then((r) => r.agents ?? []);

export const getOperatorRunners = (workspaceId: string) =>
  api<{ runners: OperatorRunner[] | null }>(`/api/v1/me/runtime/runners?workspace_id=${encodeURIComponent(workspaceId)}`).then((r) => r.runners ?? []);

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
      const [raw, manifest] = await Promise.all([
        api<{ mode?: string; revision?: number; enabled?: boolean; config?: unknown; excluded?: unknown }>(`/api/v1/workspaces/${wsId}/runtime`),
        loadRunnerManifest(wsId),
      ]);
      if (raw?.mode === "managed") {
        if (!raw.config || typeof raw.config !== "object" || Array.isArray(raw.config)) throw new Error("runtime catalog missing");
        return {
          source: "workspace", wsId, revision: raw.revision ?? 0, enabled: raw.enabled === true, config: raw.config as RuntimeConfig, manifest,
          excluded: excludedReasons(raw.excluded),
        };
      }
    } catch (e) {
      if (status(e) !== 403) throw e;
    }
  }
  const doc = await getMyRuntime();
  return { source: "personal", revision: doc.revision, config: doc.config ?? {} };
}

/**
 * The Runner manifest as the workspace sees it (localized by the server). A
 * failure leaves the list readable: the forms are off, not the whole block.
 */
async function loadRunnerManifest(wsId: string): Promise<ExtensionManifest | null> {
  try {
    const r = await api<{ extensions: { id: string; manifest?: ExtensionManifest }[] | null }>(`/api/v1/workspaces/${wsId}/extensions`);
    return r?.extensions?.find((e) => e.id === "runner")?.manifest ?? null;
  } catch {
    return null;
  }
}

/**
 * Saves the whole draft against the loaded revision. On/off is not part of it:
 * only the Runner switch on the overview changes that.
 */
export function saveResources(doc: ResourceDoc, config: RuntimeConfig): Promise<unknown> {
  if (doc.source === "personal") return saveMyRuntime(doc.revision, config);
  return api(`/api/v1/workspaces/${doc.wsId}/runtime`, { method: "PUT", body: { if_revision: doc.revision, config } });
}

/** Saves an earlier catalog revision's config as the next revision; the server keeps its credential references. */
export const restoreRevision = (wsId: string, ifRevision: number, revision: number) =>
  api(`/api/v1/workspaces/${wsId}/runtime/restore`, { method: "POST", body: { if_revision: ifRevision, revision } });

/** `excluded: [{ref, reason}]` of the runtime answer as reason by binding ref; anything else reads as none. */
export function excludedReasons(raw: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (!Array.isArray(raw)) return out;
  for (const e of raw) {
    const { ref, reason } = (e && typeof e === "object" ? e : {}) as { ref?: unknown; reason?: unknown };
    if (typeof ref === "string" && typeof reason === "string" && reason) out[ref] = reason;
  }
  return out;
}

/** The workspace a catalog binding belongs to, or null when the entry names none. */
export function bindingWorkspace(value: unknown): string | null {
  const b = (value && typeof value === "object" ? value : {}) as { binding?: { workspace_id?: unknown } };
  return typeof b.binding?.workspace_id === "string" ? b.binding.workspace_id : null;
}

/**
 * Whether a binding is not this workspace's: it names another workspace or
 * none at all. Such bindings are counted, never listed, and kept in the
 * catalog as they are.
 */
export function isForeignBinding(value: unknown, wsId: string): boolean {
  return bindingWorkspace(value) !== wsId;
}

/** A new binding made on a workspace's screen belongs to that workspace. */
export function newBindingValue(wsId: string): Values {
  return { binding: { workspace_id: wsId } };
}

/** Profiles each agent of the workspace may run with, by agent id, read from its own catalog bindings. */
export function profilesByAgent(config: RuntimeConfig | null, wsId?: string): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const value of Object.values(config?.bindings ?? {})) {
    if (wsId && isForeignBinding(value, wsId)) continue;
    const b = (value && typeof value === "object" ? value : {}) as { binding?: { agent_id?: unknown }; permitted_profiles?: unknown };
    const id = b.binding?.agent_id;
    if (typeof id !== "string" || !Array.isArray(b.permitted_profiles)) continue;
    const list = (out[id] ??= []);
    for (const p of b.permitted_profiles) if (typeof p === "string" && !list.includes(p)) list.push(p);
  }
  return out;
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

/** The form schema of one entry of a catalog section, or null when the manifest does not describe it. */
export function entrySchema(manifest: ExtensionManifest | null, section: ConfigSection): FieldSchema | null {
  const catalog = (manifest?.schemas as Record<string, FieldSchema> | undefined)?.catalog;
  const entry = catalog?.properties?.[section]?.additionalProperties;
  if (!entry || typeof entry !== "object") return null;
  const resolved = resolveSchema(entry, manifest);
  return resolved.properties ? resolved : null;
}

/** What each `x-ref` of the schema may point at, from the draft and the entry being edited. */
export function refOptions(config: RuntimeConfig, section: ConfigSection, entry: Values): Record<string, string[]> {
  const names = (s: ConfigSection | "controllers") => Object.keys((config[s] ?? {}) as Record<string, unknown>).sort((a, b) => a.localeCompare(b));
  const pools = config.pools ?? {};
  const provider = typeof entry.provider === "string" ? entry.provider : "";
  const controller = (config.controllers as Record<string, { capabilities?: unknown }> | undefined)?.[String(entry.controller_ref ?? "")];
  const permitted = Array.isArray(entry.permitted_profiles) ? entry.permitted_profiles.map(String) : [];
  return {
    // an account draws only on pools of its own provider (the server refuses the rest)
    pools: names("pools").filter((n) => section !== "accounts" || !provider || (pools[n] as { provider?: unknown })?.provider === provider),
    accounts: names("accounts"),
    profiles: names("profiles"),
    controllers: names("controllers"),
    capabilities: Array.isArray(controller?.capabilities) ? controller.capabilities.map(String) : [],
    permitted_profiles: permitted,
    // providers the workspace has accounts or pools of
    providers: [...new Set([...Object.values(config.accounts ?? {}), ...Object.values(pools)].map((v) => (v as { provider?: unknown })?.provider).filter((x): x is string => typeof x === "string"))].sort(),
  };
}

/** A copy of the schema where every `x-ref` / `x-key-ref` became an `enum` of what exists now. */
export function bindRefs(schema: FieldSchema, options: Record<string, string[]>): FieldSchema {
  const out: FieldSchema = { ...schema };
  if (schema["x-ref"]) out.enum = options[schema["x-ref"]] ?? [];
  if (schema["x-key-ref"]) out.propertyNames = { ...(schema.propertyNames ?? {}), type: "string", enum: options[schema["x-key-ref"]] ?? [] };
  if (schema.items) out.items = bindRefs(schema.items, options);
  if (schema.additionalProperties && typeof schema.additionalProperties === "object") out.additionalProperties = bindRefs(schema.additionalProperties, options);
  if (schema.properties) out.properties = Object.fromEntries(Object.entries(schema.properties).map(([k, f]) => [k, bindRefs(f, options)]));
  return out;
}

/** Dotted paths of fields the entry holds and the schema does not know (the form would drop them). */
export function unknownFields(schema: FieldSchema, value: unknown, prefix = ""): string[] {
  if (!value || typeof value !== "object" || Array.isArray(value)) return [];
  const out: string[] = [];
  for (const [k, v] of Object.entries(value)) {
    const path = prefix ? `${prefix}.${k}` : k;
    const f = schema.properties?.[k];
    if (!f) out.push(path);
    else if (f.properties) out.push(...unknownFields(f, v, path));
  }
  return out;
}

/** Whether a field path (`a.b.c`) names a field the form can show; map keys follow the map's value schema. */
export function isSchemaPath(schema: FieldSchema, path: string): boolean {
  let at: FieldSchema | undefined = schema;
  for (const k of path.split(".")) {
    if (!at) return false;
    if (at.type === "array") return true;
    const next: FieldSchema | boolean | undefined = at.properties?.[k] ?? at.additionalProperties;
    at = typeof next === "object" ? next : undefined;
  }
  return at !== undefined;
}

// The server keeps credential references to itself: GET returns them empty and
// an empty one on PUT keeps the stored reference.
const STORED = "********";

/** The entry as the form starts it: a stored credential reference reads as "set", never as text. */
export function toFormValue(section: ConfigSection, value: unknown, isNew: boolean): Values {
  const v = { ...((value && typeof value === "object" ? value : {}) as Values) };
  if (section === "accounts" && !isNew) v.credential_ref = STORED;
  return v;
}

/** The entry to store: an untouched credential reference goes back empty, i.e. "keep". */
export function fromFormValue(section: ConfigSection, payload: Values): Values {
  if (section !== "accounts") return payload;
  // A mode with no pools is left out: the catalog lists only the modes an account serves.
  const out: Values = { ...payload, credential_ref: typeof payload.credential_ref === "string" ? payload.credential_ref : "" };
  if (payload.quota_pools_by_mode && typeof payload.quota_pools_by_mode === "object") {
    out.quota_pools_by_mode = Object.fromEntries(
      Object.entries(payload.quota_pools_by_mode as Values).filter(([, pools]) => !Array.isArray(pools) || pools.length > 0),
    );
  }
  return out;
}

export interface EntryFieldErrors { section: ConfigSection; name: string; fields: Record<string, string> }

/**
 * The server's per-field reasons (`config.<section>.<name>.<field>` → code)
 * grouped by entry, so the editor can reopen it with each reason at its field.
 * Entry names may hold dots: the longest existing name wins.
 */
export function fieldErrorsByEntry(details: Record<string, string> | undefined, config: RuntimeConfig): EntryFieldErrors[] {
  const out = new Map<string, EntryFieldErrors>();
  for (const [key, code] of Object.entries(details ?? {})) {
    const path = key.replace(/^config\./, "");
    const section = path.slice(0, path.indexOf(".")) as ConfigSection;
    const rest = path.slice(section.length + 1);
    const name = Object.keys(config[section] ?? {})
      .filter((n) => rest === n || rest.startsWith(`${n}.`))
      .sort((a, b) => b.length - a.length)[0];
    if (!name) continue;
    const id = `${section}\u0000${name}`;
    const e = out.get(id) ?? { section, name, fields: {} };
    e.fields[rest === name ? "" : rest.slice(name.length + 1)] = code;
    out.set(id, e);
  }
  return [...out.values()];
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
