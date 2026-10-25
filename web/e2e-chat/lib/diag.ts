import { SetupFail } from "./env";
import type { Actor, Ctx } from "./harness";

/**
 * Read-only QA audit of the chat, delivered by an internal ticket (setup-API):
 *
 *   GET /api/v1/workspaces/{ws}/chat/audit/threads/{thread}         runtime.session_id, turns[].session_id, …
 *   GET /api/v1/workspaces/{ws}/chat/audit/threads/{thread}/events  { events: [{ turn_id, seq, kind, label_length }], attachments: [...] }
 *   GET /api/v1/workspaces/{ws}/chat/audit/memory-writes?thread_id= { items: [{ memory_id, chat_thread_id, initiated_by_user_id, turn_id, … }] }
 *
 * Access is owner/admin of the workspace (actor C), not a personal admin and not
 * the DB. The calls go through the QA actor's page (Actor.fetch), so a red control
 * can answer them with a known-bad body through context.route. Anything but 200 is
 * FAIL SETUP: no pass by absence.
 */
const auditBase = (x: Ctx) => `/api/v1/workspaces/${x.workspaceId}/chat/audit`;

async function read(qa: Actor, what: string, path: string): Promise<{ text: string; json: unknown }> {
  const res = await qa.fetch("GET", path);
  if (res.status !== 200) throw new SetupFail(`${what} audit answered ${res.status} for the QA actor ${qa.id}`);
  return { text: res.text, json: JSON.parse(res.text) };
}

/** The agent session of a thread: the runtime's current one, else the latest turn that recorded one. */
export async function sessionOf(qa: Actor, x: Ctx, threadId: string): Promise<string | undefined> {
  const { json } = await read(qa, "thread", `${auditBase(x)}/threads/${threadId}`);
  const a = json as { runtime?: { session_id?: string } | null; turns?: Array<{ session_id?: string | null }> };
  return a.runtime?.session_id ?? [...(a.turns ?? [])].reverse().find((t) => t.session_id)?.session_id ?? undefined;
}

export interface MemoryWrite { memory_id?: string; chat_thread_id?: string; initiated_by_user_id?: string; turn_id?: string | null; [k: string]: unknown }

export async function memoryWrites(qa: Actor, x: Ctx, threadId?: string): Promise<MemoryWrite[]> {
  const q = threadId ? `?thread_id=${threadId}&limit=200` : "?limit=200";
  const { json } = await read(qa, "memory-writes", `${auditBase(x)}/memory-writes${q}`);
  return (json as { items: MemoryWrite[] }).items;
}

/** Keys that would mean an event or attachment row carries content, not metadata. */
const CONTENT_KEYS = ["body", "text", "content", "payload", "label", "file_name", "data"];

export interface ExtEvent { kind: string; has_body: boolean }

/** Tool events and attachments of one thread, reduced to "does this row carry content". */
export async function threadEvents(qa: Actor, x: Ctx, threadId: string): Promise<{ raw: string; items: ExtEvent[] }> {
  const { text, json } = await read(qa, "events", `${auditBase(x)}/threads/${threadId}/events`);
  const a = json as { events?: Array<Record<string, unknown>>; attachments?: Array<Record<string, unknown>> };
  const rows = [...(a.events ?? []).map((e) => ({ kind: String(e.kind ?? "event"), e })), ...(a.attachments ?? []).map((e) => ({ kind: "attachment", e }))];
  return { raw: text, items: rows.map(({ kind, e }) => ({ kind, has_body: CONTENT_KEYS.some((k) => k in e) })) };
}
