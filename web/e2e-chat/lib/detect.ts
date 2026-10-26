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

/**
 * The canary of one actor. The random tail follows "_", not "-": with "-" a tail that starts
 * with "a" spelled `${runId}-a` inside B's own canary, and the case-insensitive match read B's
 * own message as A's title (P05 B, job 409302).
 */
export function canary(who: string, runId: string, tail = Math.random().toString(36).slice(2, 10)): string {
  return `CANARY-${who}-${runId}_${tail}`;
}

/** An actor's own fixture must not already match the foreign set, or every check would read it as a leak. */
export function assertFixtureDisjoint(what: string, own: string[], f: Forbidden): void {
  const found = leaks(own.join("\n"), f);
  if (found.length > 0) throw new Error(`${what}: own fixture matches the foreign set (${[...new Set(found)].join(",")}) — a fixture collision, not a leak`);
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
  /** Arrival time in the test process, ms since epoch. */
  at?: number;
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

/** The text a delta frame carries, read from its raw payload in memory. */
export function deltaText(fr: Frame): string {
  try {
    const d = (JSON.parse(fr.raw) as { data?: { text?: unknown } }).data;
    return typeof d?.text === "string" ? d.text : "";
  } catch {
    return "";
  }
}

const isTextDelta = (fr: Frame): boolean => fr.event === "chat.message.delta" && (fr.textLen ?? 0) > 0;

/**
 * S04: the reply text reaches the client as delta(s) before its message completes. `frames` are
 * the thread's frames in arrival order. At least `min` non-empty deltas of one turn/message with
 * seq strictly increasing, a chat.message.completed for the message, and no delta after it.
 * Returns the deltas.
 */
export function assertStreaming(frames: Frame[], messageId: string, min = 1): Frame[] {
  const mine = frames.filter((fr) => fr.message_id === messageId);
  const d = mine.filter(isTextDelta);
  if (d.length < min) throw new Error(`streaming: ${d.length} non-empty delta(s), need >= ${min}`);
  const key = (fr: Frame) => `${fr.turn_id}/${fr.message_id}`;
  if (new Set(d.map(key)).size !== 1) throw new Error("streaming: deltas belong to different turn/message");
  for (let i = 1; i < d.length; i++) {
    if (!((d[i]!.seq ?? -1) > (d[i - 1]!.seq ?? -1))) throw new Error(`streaming: seq not increasing at ${i}`);
  }
  const done = mine.findIndex((fr) => fr.event === "chat.message.completed");
  if (done < 0) throw new Error("streaming: no chat.message.completed for the message");
  const late = mine.slice(done + 1).filter(isTextDelta).length;
  if (late > 0) throw new Error(`streaming: ${late} delta(s) after chat.message.completed`);
  return d;
}

/** Every delta's text, as sent, is part of the final body: Mesh appends delta text verbatim. */
export function assertDeltasInFinal(deltas: Frame[], final: string): void {
  deltas.forEach((fr, i) => {
    const t = deltaText(fr);
    if (t === "") throw new Error(`streaming: delta ${i} carries no readable text`);
    if (!final.includes(t)) throw new Error(`streaming: the text of delta ${i} is not in the final body`);
  });
}

/** S04b: the first delta arrived at least `leadMs` before chat.message.completed of its message. */
export function assertEarlyDelta(frames: Frame[], messageId: string, leadMs = 1000): void {
  const mine = frames.filter((fr) => fr.message_id === messageId);
  const first = mine.find(isTextDelta);
  const done = mine.find((fr) => fr.event === "chat.message.completed");
  if (first?.at === undefined || done?.at === undefined) throw new Error("streaming: no timed delta or completed frame");
  const lead = done.at - first.at;
  if (lead < leadMs) throw new Error(`streaming: the first delta came ${lead} ms before completed, need >= ${leadMs} ms`);
}

/**
 * The stored body of an in-flight message must already be a prefix of the final one. With `grow`
 * (S04b, several blocks) it must also be shorter; a one-block reply (S04) is stored whole while the
 * message is still streaming, so there the in-flight body may equal the final one.
 */
export function assertIntermediateThenFinal(intermediate: string, final: string, grow = true): void {
  if (intermediate === "") throw new Error("streaming: no intermediate body was ever stored");
  if (!final.startsWith(intermediate)) throw new Error("streaming: intermediate body is not a prefix of the final body");
  if (grow && final.length <= intermediate.length) throw new Error("streaming: final body is not longer than the intermediate one");
}

export interface BodySnapshot {
  body: string;
  status: string;
}

/** S06/P05: Stop and the reload frames make sense only while the reply is still mid-turn. */
export function assertTurnRunning(m: { status: string } | undefined, sentAt: number, now = Date.now()): void {
  if (m?.status === "streaming") return;
  throw new Error(`turn ended before Stop after ${Math.round((now - sentAt) / 1000)}s (reply status ${m?.status ?? "absent"}): the hold prompt did not keep the turn running`);
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

/** What the page actually paints, read from <html> and the computed body background. */
export interface PaintedMode {
  htmlDark: boolean;
  /** Relative luminance 0..1 of the first opaque background from <body> up. */
  bgLuminance: number;
}

/**
 * AC7: a frame labelled light or dark must be painted in that mode. The class alone
 * is not enough (a theme can ignore it), the luminance alone is not either (a dark
 * picture on a light page), so both must agree with the label.
 */
export function assertEffectiveMode(expected: "light" | "dark", p: PaintedMode): void {
  if (p.htmlDark !== (expected === "dark")) throw new Error(`mode: expected ${expected}, <html> dark class is ${p.htmlDark}`);
  const dark = p.bgLuminance < 0.4;
  if (dark !== (expected === "dark")) throw new Error(`mode: expected ${expected}, page background luminance is ${p.bgLuminance.toFixed(3)}`);
}

/** AC7: the light and the dark frame of one state are different pictures. */
export function assertFramesDiffer(name: string, lightSha: string, darkSha: string): void {
  if (lightSha === darkSha) throw new Error(`${name}: light and dark frames are byte-identical (${lightSha.slice(0, 12)})`);
}

/** AC3: the rendered reply carries a heading, a list of at least `items` entries and a code block with `code`. */
export function assertMarkdownDom(d: { headings: string[]; listItems: number; codeBlocks: string[] }, code: string, items = 3): void {
  if (d.headings.length === 0) throw new Error("markdown: no heading element in the rendered reply");
  if (d.listItems < items) throw new Error(`markdown: ${d.listItems} list item(s), need >= ${items}`);
  if (!d.codeBlocks.some((c) => c.includes(code))) {
    // The thread is deleted at teardown: the blocks themselves are the only trace of what the agent wrote.
    const seen = d.codeBlocks.slice(0, 3).map((c) => c.trim().slice(0, 80));
    throw new Error(`markdown: no <pre><code> block carries ${code}; blocks: ${JSON.stringify(seen)}`);
  }
}

/** AC3: the turn reported at least one tool event with a label, over the socket and in the activity strip. */
export function assertToolEvents(frames: Frame[], turnId: string, domLabels: string[]): void {
  const own = frames.filter((fr) => fr.event === "chat.turn.tool" && fr.turn_id === turnId);
  if (own.length === 0) throw new Error(`tools: no chat.turn.tool frame for turn ${turnId}`);
  if (domLabels.filter((l) => l.trim() !== "").length === 0) throw new Error("tools: the activity strip never showed a tool label");
}
