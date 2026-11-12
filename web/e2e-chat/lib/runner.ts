import { randomUUID } from "node:crypto";
import { type APIRequestContext, request } from "@playwright/test";
import type { Actor } from "./harness";
import { SetupFail, cfg } from "./env";

/**
 * The S07 test runner: the offline fixture agent coming back, speaking the runner chat protocol
 * (docs/extensions/runner-chat-protocol.md) with nothing but HTTP. It is the only runner of that
 * agent, so "exactly one claim" is about the queue, not about which of two runners won.
 *
 * Its credential is a temporary key of the offline agent issued by the workspace admin (actor C)
 * through POST /agents/{id}/keys with an expiry, and revoked by close(). The key lives only in
 * this process: it is never printed, never written to an artifact, and never part of an error.
 */
export interface RunnerAnswer {
  status: number;
  code: string;
  json: Record<string, unknown>;
}

export interface ClaimedTurn {
  turn_id: string;
  thread_id: string;
  claim_token: string;
  attempt: number;
}

export interface KeyInfo { id: string; primary?: boolean; label?: string; created_at?: string | null; expires_at?: string | null; revoked_at?: string | null }

/**
 * Keys this harness issued and never revoked (a run that died before afterAll). The agent holds at most two
 * active keys, so two leaked ones answer 409 to every later run. One run at a time (resource_group), so any
 * active non-primary key with the harness label is stale. The original key and foreign keys are never touched.
 */
export function staleHarnessKeys(keys: KeyInfo[], now: number = Date.now(), minAgeMs = 10 * 60_000): KeyInfo[] {
  // Ownership: the exact label shape the harness builds (e2e-chat-<RUN_ID>-s07, RUN_ID = chat-<pipeline>-<job>) and an age
  // over minAgeMs, so a key of a run in progress, or one without a creation time, is never taken.
  return keys.filter((k) => {
    const born = k.created_at ? Date.parse(k.created_at) : NaN;
    // An expired key is not active and holds no slot; an unparseable expiry is not trusted either way, so it is never swept.
    const exp = k.expires_at == null ? Infinity : Date.parse(k.expires_at);
    const gone = !(exp > now);
    return !k.primary && !k.revoked_at && !gone && /^e2e-chat-chat-[a-z0-9]+-[a-z0-9]+-s07$/.test(k.label ?? "") && Number.isFinite(born) && now - born > minAgeMs;
  });
}

type KeyApi = (method: string, path: string, body?: unknown) => Promise<{ status: number; json: () => unknown }>;

/** POST a key; on 409 revoke this harness's stale keys and POST once more. Any other status is returned as is. */
export async function issueSweeping(api: KeyApi, agentId: string, label: string, ttlSeconds: number) {
  const issue = () => api("POST", `/api/v1/agents/${agentId}/keys`, { label, expires_in_seconds: ttlSeconds });
  let res = await issue();
  if (res.status === 409) {
    const list = await api("GET", `/api/v1/agents/${agentId}/keys`);
    const stale = list.status === 200 ? staleHarnessKeys(((list.json() as { keys?: KeyInfo[] }).keys) ?? []) : [];
    for (const k of stale) await api("DELETE", `/api/v1/agents/${agentId}/keys/${k.id}`);
    if (stale.length) res = await issue();
  }
  return res;
}

export class TestRunner {
  readonly runnerId = randomUUID();
  private constructor(
    private readonly admin: Actor,
    private readonly agentId: string,
    private readonly keyId: string,
    private readonly http: APIRequestContext,
  ) {}

  /** Issues the key (C must be owner/admin of the workspace) and opens a request context bound to it. */
  static async start(admin: Actor, agentId: string, label: string, ttlSeconds: number): Promise<TestRunner> {
    const res = await issueSweeping(admin.api.bind(admin), agentId, label, ttlSeconds);
    // Only the status leaves this function: the body holds the raw key.
    if (res.status !== 201) throw new SetupFail(`issuing a temporary key for the offline agent answered ${res.status} (actor ${admin.id} must be workspace admin; 409 = two active keys that are not this harness's)`);
    const issued = res.json() as { key_id?: string; api_key?: string };
    if (!issued.key_id || !issued.api_key) throw new SetupFail("the issued agent key carries no key_id/api_key");
    const http = await request.newContext({
      baseURL: cfg().baseURL,
      extraHTTPHeaders: { "X-Agent-Key": issued.api_key, "X-Chat-Protocol": "1" },
    });
    return new TestRunner(admin, agentId, issued.key_id, http);
  }

  private async call(method: string, path: string, body?: unknown): Promise<RunnerAnswer> {
    const res = await this.http.fetch(`/api/v1/runner/chat${path}`, {
      method,
      headers: { "X-Chat-Runner-Id": this.runnerId },
      data: body as never,
    });
    const text = await res.text().catch(() => "");
    let json: Record<string, unknown> = {};
    try {
      json = text ? (JSON.parse(text) as Record<string, unknown>) : {};
    } catch {
      json = {};
    }
    return { status: res.status(), code: typeof json.error_code === "string" ? json.error_code : "", json };
  }

  /** Long-poll claim for the offline agent; 204 is an empty queue. */
  async claim(timeoutSeconds: number, max = 5): Promise<{ status: number; turns: ClaimedTurn[] }> {
    const a = await this.call("GET", `/claim?agents=${this.agentId}&timeout=${timeoutSeconds}&max=${max}`);
    if (a.status !== 200 && a.status !== 204) throw new Error(`runner claim answered ${a.status} ${a.code}`);
    return { status: a.status, turns: (a.json.turns as ClaimedTurn[] | undefined) ?? [] };
  }

  start(turnId: string, claimToken: string): Promise<RunnerAnswer> {
    return this.call("POST", `/turns/${turnId}/start`, { claim_token: claimToken });
  }

  events(turnId: string, claimToken: string, events: Array<Record<string, unknown>>): Promise<RunnerAnswer> {
    return this.call("POST", `/turns/${turnId}/events`, { claim_token: claimToken, events });
  }

  /** Revokes the key and drops the context; a failed revoke is reported, the key still expires on its own. */
  async close(): Promise<void> {
    try {
      const del = await this.admin.api("DELETE", `/api/v1/agents/${this.agentId}/keys/${this.keyId}`);
      if (del.status >= 300 && del.status !== 404) throw new Error(`revoking the temporary offline-agent key ${this.keyId} answered ${del.status}`);
    } finally {
      await this.http.dispose();
    }
  }
}
