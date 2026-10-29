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
    const res = await admin.api("POST", `/api/v1/agents/${agentId}/keys`, { label, expires_in_seconds: ttlSeconds });
    // Only the status leaves this function: the body holds the raw key.
    if (res.status !== 201) throw new SetupFail(`issuing a temporary key for the offline agent answered ${res.status} (actor ${admin.id} must be workspace admin)`);
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
