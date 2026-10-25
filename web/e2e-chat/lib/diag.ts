import { SetupFail } from "./env";
import type { Actor } from "./harness";

/**
 * Read-only QA diagnostics of the chat (session/process per thread, shared-memory
 * audit with source attribution, extension-event metadata). The endpoints are
 * delivered by an internal ticket (setup-API). Until their paths are bound through the
 * CI variables below, every call is FAIL SETUP: no personal or admin access, no
 * DB shortcut, and no pass by absence.
 *
 *   E2E_CHAT_DIAG_SESSION_PATH  e.g. /api/v1/.../{thread}   -> { session_id, process_alive }
 *   E2E_CHAT_DIAG_MEMORY_PATH   audit of shared-memory writes -> { items: [{ thread_id, turn_id, user_id, text? }] }
 *   E2E_CHAT_DIAG_EVENTS_PATH   extension-event metadata      -> { items: [{ kind, thread_id?, has_body: boolean }] }
 *
 * The response shapes above are what this suite reads; when an internal ticket merges its
 * contract replaces them here, in one file.
 */
const need = (name: string): string => {
  const v = process.env[name];
  if (!v) throw new SetupFail(`${name} is not bound: the read-only diagnostic of an internal ticket is not available (P05–P07, S06 cancel ack, S08 stay FAIL SETUP)`);
  return v;
};

export async function sessionOf(qa: Actor, threadId: string): Promise<string | undefined> {
  const res = await qa.api("GET", need("E2E_CHAT_DIAG_SESSION_PATH").replace("{thread}", threadId));
  if (res.status !== 200) throw new SetupFail(`session diagnostic answered ${res.status} for the QA actor`);
  return (res.json() as { session_id?: string }).session_id;
}

export interface MemoryAuditItem { thread_id?: string; turn_id?: string; user_id?: string; text?: string }

export async function memoryAudit(qa: Actor): Promise<MemoryAuditItem[]> {
  const res = await qa.api("GET", need("E2E_CHAT_DIAG_MEMORY_PATH"));
  if (res.status !== 200) throw new SetupFail(`memory audit answered ${res.status} for the QA actor`);
  return (res.json() as { items: MemoryAuditItem[] }).items;
}

export interface ExtEvent { kind: string; thread_id?: string; has_body?: boolean }

export async function extensionEvents(qa: Actor): Promise<ExtEvent[]> {
  const res = await qa.api("GET", need("E2E_CHAT_DIAG_EVENTS_PATH"));
  if (res.status !== 200) throw new SetupFail(`extension events answered ${res.status} for the QA actor`);
  return (res.json() as { items: ExtEvent[] }).items;
}
