/**
 * S08 detector inputs, not a runner wire schema. The runner observer must supply actual
 * chat-process and occupied-slot counts for this run/thread; daemon, token and active-turn
 * counts are not substitutes. A missing observation must fail, never default to zero.
 */
export interface IdleResources {
  chatProcesses: number;
  occupiedSlots: number;
}

export function assertIdleResources(resources: IdleResources | undefined): void {
  if (!resources) throw new Error("S08 idle: runner resource observation is missing");
  for (const name of ["chatProcesses", "occupiedSlots"] as const) {
    const count = resources[name];
    if (!Number.isSafeInteger(count) || count < 0) throw new Error(`S08 idle: invalid ${name} count`);
  }
  if (resources.chatProcesses !== 0) throw new Error(`S08 idle: ${resources.chatProcesses} chat process(es) still alive after deadline`);
  if (resources.occupiedSlots !== 0) throw new Error(`S08 idle: ${resources.occupiedSlots} slot(s) still occupied after deadline`);
}

export interface IdleMessage {
  id: string;
  role: string;
  body: string;
  status: string;
  turn_id: string | null;
}

export interface IdleHistory {
  items: IdleMessage[];
  active_turn: { id: string; state: string } | null;
}

/** Preserve the old messages in order, with no loss, mutation, replay or extra rows. */
export function assertIdleHistory(before: IdleHistory, after: IdleHistory): void {
  if (before.items.length === 0 || before.active_turn) throw new Error("S08 idle: baseline must be a settled non-empty chat");
  if (after.active_turn) throw new Error("S08 idle: an active turn appeared during idle");
  assertSameMessages(before.items, after.items);
}

function assertSameMessages(before: IdleMessage[], after: IdleMessage[]): void {
  if (before.length !== after.length) throw new Error(`S08 history: ${after.length} messages, expected ${before.length} (duplicate or loss)`);
  if (new Set(after.map((m) => m.id)).size !== after.length) throw new Error("S08 history: duplicate message id");
  for (const [i, old] of before.entries()) {
    const got = after[i]!;
    if (["id", "role", "body", "status", "turn_id"].some((k) => old[k as keyof IdleMessage] !== got[k as keyof IdleMessage])) {
      // Never put a transcript body into a failure log.
      throw new Error(`S08 history: old message #${i + 1} changed or moved`);
    }
  }
}

/** Exactly one new user message and one done reply on the send's turn, following the unchanged prefix. */
export function assertIdleResumed(before: IdleHistory, after: IdleHistory, turnId: string, sentBody: string, turnState: string | undefined): void {
  if (turnState !== "done" || after.active_turn) throw new Error(`S08 resume: turn ${turnId} is ${turnState ?? "absent"}, need done with no active turn`);
  if (before.items.length === 0 || before.active_turn) throw new Error("S08 resume: baseline must be a settled non-empty chat");
  if (before.items.some((m) => m.turn_id === turnId)) throw new Error("S08 resume: the next send reused an old turn");
  assertSameMessages(before.items, after.items.slice(0, before.items.length));
  if (new Set(after.items.map((m) => m.id)).size !== after.items.length) throw new Error("S08 history: duplicate message id");
  const added = after.items.slice(before.items.length);
  if (added.length !== 2) throw new Error(`S08 resume: ${added.length} new messages, need exactly one user and one reply (duplicate or loss)`);
  const [user, reply] = added;
  if (user!.role !== "user" || user!.body !== sentBody || user!.status !== "done" || user!.turn_id !== turnId) {
    throw new Error("S08 resume: the new user message is not the settled send on the new turn");
  }
  if (reply!.role !== "assistant" || reply!.status !== "done" || !reply!.body.trim() || reply!.turn_id !== turnId) {
    throw new Error("S08 resume: the new reply is not a non-empty done reply on the new turn");
  }
}

/** Runner snapshot as served by GET /chat/threads/{id}/runtime. */
export interface ThreadRuntime {
  runner_id?: string;
  process_alive?: boolean;
  slot_held?: boolean;
  reported_at?: string;
  fresh?: boolean;
}

/** Snapshots older than this are "unknown", which is red, never green (2 x announce, with slack). */
export const RUNTIME_MAX_AGE_MS = 2 * 60_000;

/**
 * Lower-bound slack for "released early". The runner's reaper steps every 30 s, so it can fire up to
 * 30 s before our clock says N has passed; the runner counts idle from the end of its turn, which is
 * earlier than `settledAt` (we observe done after it). 30 s + 30 s = 60 s.
 */
export const EARLY_TOLERANCE_MS = 60_000;

/**
 * The idle contract is "released after N", so a fresh snapshot that says the process or the slot is
 * already gone, and was reported before N minus the tolerance, means the runner reaped too early.
 * Call it on a snapshot that already passed resourcesFromRuntime (fresh, not stale, after idleSince).
 */
export function assertNotReleasedEarly(rt: ThreadRuntime, idleSince: number, idleMinutes: number): void {
  const at = Date.parse(rt.reported_at ?? "");
  if (!Number.isFinite(at)) throw new Error("S08 idle: runtime snapshot has no reported_at");
  const lowerBound = idleSince + idleMinutes * 60_000 - EARLY_TOLERANCE_MS;
  if ((rt.process_alive === false || rt.slot_held === false) && at < lowerBound) {
    const afterMin = Math.round((at - idleSince) / 600) / 100;
    throw new Error(`S08 idle: released early, process/slot gone ${afterMin} min after the turn, before the ${idleMinutes} min idle threshold`);
  }
}

/**
 * Maps a runtime snapshot to detector input. A snapshot that is absent, stale, or reported
 * before `idleSince` says nothing about the idle state and throws instead of reading as zero.
 */
export function resourcesFromRuntime(rt: ThreadRuntime | undefined, idleSince: number, now: number): IdleResources {
  if (rt && rt.fresh !== true) throw new Error("S08 idle: runtime snapshot is not confirmed fresh (unknown): fresh must be true");
  if (!rt || typeof rt.process_alive !== "boolean" || typeof rt.slot_held !== "boolean") throw new Error("S08 idle: runtime snapshot is missing or malformed");
  const at = Date.parse(rt.reported_at ?? "");
  if (!Number.isFinite(at)) throw new Error("S08 idle: runtime snapshot has no reported_at");
  if (now - at > RUNTIME_MAX_AGE_MS) throw new Error("S08 idle: runtime snapshot is stale (unknown)");
  if (at < idleSince) throw new Error("S08 idle: runtime snapshot predates the idle start");
  return { chatProcesses: rt.process_alive ? 1 : 0, occupiedSlots: rt.slot_held ? 1 : 0 };
}
