/**
 * Pure leak and behaviour detectors. They take plain data, so every one of them
 * has a red control in controls.spec.ts (fed a known-bad input, it must throw)
 * before it is trusted against the live target.
 */

export interface Forbidden {
  /** IDs that must not appear anywhere (thread, message, attachment of the other user). */
  ids: string[];
  /** Titles, file names and previews that identify the other user's data. */
  texts: string[];
  /** The canary payload. Only a boolean ever leaves the closed fixture. */
  canary: string;
}

export function leaks(haystack: string, f: Forbidden): string[] {
  const found: string[] = [];
  const h = haystack.toLowerCase();
  for (const id of f.ids) if (id && h.includes(id.toLowerCase())) found.push("id");
  for (const t of f.texts) if (t && h.includes(t.toLowerCase())) found.push("text");
  if (f.canary && h.includes(f.canary.toLowerCase())) found.push("canary");
  return found;
}

export function assertNoLeak(what: string, haystack: string, f: Forbidden): void {
  const found = leaks(haystack, f);
  if (found.length > 0) throw new Error(`${what}: foreign data present (${[...new Set(found)].join(",")}) — matched=true`);
}

/** The contract for somebody else's thread: 404 with a body that names nothing. */
export function assertOpaque404(what: string, status: number, body: string, f: Forbidden): void {
  if (status !== 404) throw new Error(`${what}: expected 404 for a foreign object, got ${status}`);
  assertNoLeak(`${what} (error body)`, body, f);
}

export interface Frame {
  event: string;
  thread_id?: string;
  turn_id?: string;
  message_id?: string;
  seq?: number;
  textLen?: number;
  /** Raw frame, memory only: matched against the forbidden set, never written out. */
  raw: string;
}

export function assertFramesClean(what: string, frames: Frame[], f: Forbidden): void {
  for (const fr of frames) {
    const found = leaks(fr.raw, f);
    if (found.length > 0) throw new Error(`${what}: foreign WS frame ${fr.event} seq=${fr.seq} (${[...new Set(found)].join(",")}) — matched=true`);
  }
}

/** A real delivery barrier: the control user got their own events, so silence is not a dead socket. */
export function assertOwnEvents(what: string, frames: Frame[], ownThreadId: string, min = 1): void {
  const own = frames.filter((fr) => fr.thread_id === ownThreadId && fr.event.startsWith("chat."));
  if (own.length < min) throw new Error(`${what}: only ${own.length} own chat events, a silent socket proves nothing`);
}

/** S04: at least two non-empty deltas of one turn/message, seq strictly increasing. */
export function assertStreaming(deltas: Frame[]): void {
  const d = deltas.filter((fr) => fr.event === "chat.message.delta" && (fr.textLen ?? 0) > 0);
  if (d.length < 2) throw new Error(`streaming: ${d.length} non-empty delta(s), need >= 2`);
  const key = (fr: Frame) => `${fr.turn_id}/${fr.message_id}`;
  if (new Set(d.map(key)).size !== 1) throw new Error("streaming: deltas belong to different turn/message");
  for (let i = 1; i < d.length; i++) {
    if (!((d[i]!.seq ?? -1) > (d[i - 1]!.seq ?? -1))) throw new Error(`streaming: seq not increasing at ${i}`);
  }
}

/** The stored body of an in-flight message must already be a prefix of the final one, and shorter. */
export function assertIntermediateThenFinal(intermediate: string, final: string): void {
  if (intermediate === "") throw new Error("streaming: no intermediate body was ever stored");
  if (!final.startsWith(intermediate)) throw new Error("streaming: intermediate body is not a prefix of the final body");
  if (final.length <= intermediate.length) throw new Error("streaming: final body is not longer than the intermediate one");
}

export interface BodySnapshot {
  body: string;
  status: string;
}

/** S06: after the terminal state no later snapshot may change the body or the status. */
export function assertFrozen(afterTerminal: BodySnapshot[], expectStatus: string): void {
  if (afterTerminal.length < 2) throw new Error("frozen: need at least two snapshots after the terminal state");
  for (const s of afterTerminal) {
    if (s.status !== expectStatus) throw new Error(`frozen: status ${s.status}, expected ${expectStatus}`);
    if (s.body !== afterTerminal[0]!.body) throw new Error("frozen: body changed after the terminal state");
  }
  if (afterTerminal[0]!.body === "") throw new Error("frozen: the partial body was not kept");
}

export function assertNoDeltaAfter(frames: Frame[], messageId: string, terminalSeq: number): void {
  const late = frames.filter((fr) => fr.event === "chat.message.delta" && fr.message_id === messageId && (fr.seq ?? 0) > terminalSeq);
  if (late.length > 0) throw new Error(`frozen: ${late.length} delta(s) after the terminal event`);
}

/** P06: every chat of one agent has its own session. */
export function assertDistinctSessions(sessions: Record<string, string | undefined>): void {
  const ids = Object.values(sessions);
  if (ids.some((s) => !s)) throw new Error("sessions: a thread has no session id");
  if (new Set(ids).size !== ids.length) throw new Error("sessions: two threads share one session id");
}

/** A response that was supposed to be denied but carried a 2xx. */
export function assertDenied(what: string, status: number): void {
  if (status >= 200 && status < 300) throw new Error(`${what}: foreign request succeeded (${status})`);
  if (status !== 404) throw new Error(`${what}: expected 404, got ${status}`);
}

/** The matching own-control: a deny only means something if the same call works for the owner. */
export function assertOk(what: string, status: number): void {
  if (status < 200 || status >= 300) throw new Error(`${what}: own control failed with ${status}`);
}

/** P07: the canary is in neither shared memory nor an extension event, and events carry metadata only. */
export function assertMemoryAndEventsClean(
  audit: unknown[],
  events: Array<{ has_body?: boolean }>,
  canary: string,
): void {
  if (JSON.stringify(audit).toLowerCase().includes(canary.toLowerCase())) throw new Error("P07: the canary reached shared memory — matched=true");
  const withBody = events.filter((e) => e.has_body).length;
  if (withBody > 0) throw new Error(`P07: ${withBody} extension event(s) carry a body or attachment payload`);
}
