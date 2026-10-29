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

/**
 * S06/P05: Stop and the reload frames make sense only while the reply is still mid-turn.
 * `byTurn` is passed for codex: its rollout delivers the reply in one piece at turn end, so a running
 * codex turn has no reply yet and only the thread's active turn says it is still going; an absent
 * active turn counts as ended.
 */
export function assertTurnRunning(m: { status: string } | undefined, sentAt: number, now = Date.now(), byTurn?: { active?: { state: string } | null }): void {
  if (byTurn ? !m && byTurn.active?.state === "streaming" : m?.status === "streaming") return;
  const turn = byTurn ? `, turn ${byTurn.active?.state ?? "none"}` : "";
  throw new Error(`turn ended before Stop after ${Math.round((now - sentAt) / 1000)}s (reply status ${m?.status ?? "absent"}${turn}): the hold prompt did not keep the turn running`);
}

/**
 * S06: after the terminal state no later snapshot may change the body or the status. `keepsBody`
 * is false for codex: Stop lands before its one-piece reply, so the cancelled reply may be empty.
 */
export function assertFrozen(afterTerminal: BodySnapshot[], expectStatus: string, keepsBody = true): void {
  if (afterTerminal.length < 2) throw new Error("frozen: need at least two snapshots after the terminal state");
  for (const s of afterTerminal) {
    if (s.status !== expectStatus) throw new Error(`frozen: status ${s.status}, expected ${expectStatus}`);
    if (s.body !== afterTerminal[0]!.body) throw new Error("frozen: body changed after the terminal state");
  }
  if (keepsBody && afterTerminal[0]!.body === "") throw new Error("frozen: the partial body was not kept");
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

/** Computed CSS colours of one piece of text and of the first opaque surface behind it. */
export interface PaintedText {
  fg: string;
  bg: string;
}

function srgb(css: string): [number, number, number, number] | null {
  const m = /^rgba?\(\s*([\d.]+)[ ,]+([\d.]+)[ ,]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/.exec(css.trim());
  if (!m) return null;
  const a = m[4] === undefined ? 1 : m[4].endsWith("%") ? Number(m[4].slice(0, -1)) / 100 : Number(m[4]);
  return [Number(m[1]), Number(m[2]), Number(m[3]), a];
}

function relLuminance([r, g, b]: [number, number, number, number]): number {
  const lin = (c: number) => (c / 255 <= 0.03928 ? c / 255 / 12.92 : ((c / 255 + 0.055) / 1.055) ** 2.4);
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

/** WCAG contrast ratio of two computed colours; throws when either cannot be judged. */
export function contrastRatio(p: PaintedText): number {
  const fg = srgb(p.fg);
  const bg = srgb(p.bg);
  if (!fg || !bg) throw new Error(`contrast: cannot parse fg=${p.fg} bg=${p.bg}`);
  if (bg[3] < 1) throw new Error(`contrast: background ${p.bg} is not opaque`);
  const [hi, lo] = [relLuminance(fg), relLuminance(bg)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

/** E3 AC1: a file attachment's name reads as normal text on its card (WCAG >= 4.5:1). */
export function assertReadable(what: string, p: PaintedText, min = 4.5): void {
  const ratio = contrastRatio(p);
  if (ratio < min) throw new Error(`${what}: contrast ${ratio.toFixed(2)}:1 < ${min}:1 (fg ${p.fg} on bg ${p.bg})`);
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

/** The reply side of one thread as history shows it. Only ids, states and lengths leave this file. */
export interface ReplyView {
  items: Array<{ id: string; role: string; status: string; body: string; error_code?: string | null }>;
  active_turn: { id: string; state: string; queue_position?: number; agent_offline?: boolean } | null;
}

/**
 * Where a reply wait stopped: the turn and its phase (queued with its place, claimed, streaming, or
 * none), the reply's id, status and error code. The body goes out as its length only, so a timeout
 * names its cause without copying a prompt or a reply into the CI log (job 436006 timed out twice
 * with nothing but "timed out after 240000 ms").
 */
export function describeReply(h: ReplyView, threadId: string, nth: number): string {
  const replies = h.items.filter((i) => i.role === "assistant");
  const m = replies[nth - 1];
  const t = h.active_turn;
  const turn = t
    ? `turn ${t.id} ${t.state}${t.queue_position != null ? ` at queue position ${t.queue_position}` : ""}${t.agent_offline ? ", agent offline" : ""}`
    : "no active turn";
  // Every line names status, error_code and body length, "none"/0 when the reply does not exist yet.
  const reply = m
    ? `reply #${nth} ${m.id} status ${m.status}, error_code ${m.error_code || "none"}, body ${m.body.length} chars`
    : `reply #${nth} absent (${replies.length} assistant message(s)), status none, error_code none, body 0 chars`;
  return `thread ${threadId}: ${turn}; ${reply}`;
}

/**
 * A reply that ended failed or cancelled with no text will never get one: waiting the full budget for
 * a body only hides why (job 436006, S04 codex: harness_error at 62 s, the wait ran to 240 s).
 */
export function assertReplyCanStillCome(h: ReplyView, threadId: string, nth: number): void {
  const m = h.items.filter((i) => i.role === "assistant")[nth - 1];
  if (m && (m.status === "failed" || m.status === "cancelled") && m.body === "") {
    throw new Error(`reply #${nth} ended ${m.status} without a body: ${describeReply(h, threadId, nth)}`);
  }
}

/** One turn as the workspace chat audit (actor C) reports it: lifecycle fields only, no bodies. */
export interface AuditTurn {
  id: string;
  state: string;
  runner_id: string | null;
  attempt: number;
  first_claimed_at: string | null;
  started_at: string | null;
  finished_at: string | null;
  error_code: string | null;
  stop_reason: string | null;
  last_seq: number;
  terminal_seq: number | null;
}

/** S07: a message to an offline agent waits in the queue untouched: nobody claimed or started it. */
export function assertQueuedUnclaimed(t: AuditTurn | undefined): void {
  if (!t) throw new Error("S07 queue: the turn is not in the audit");
  const held = [
    t.state !== "queued" && `state ${t.state}`,
    t.runner_id && `runner_id ${t.runner_id}`,
    t.attempt !== 0 && `attempt ${t.attempt}`,
    t.first_claimed_at && "first_claimed_at set",
    t.started_at && "started_at set",
    t.finished_at && "finished_at set",
  ].filter(Boolean);
  if (held.length > 0) throw new Error(`S07 queue: turn ${t.id} is not an untouched queued turn (${held.join(", ")})`);
}

/**
 * S07 recovery: the runner that came back took the turn exactly once and ended it exactly once.
 * `claims` is how many claim answers carried the turn; `secondTerminal` is the answer to a second
 * terminal event for the same turn, which the protocol must refuse with 409 chat_turn_terminal.
 */
export function assertOneClaimOneTerminal(
  t: AuditTurn | undefined,
  runnerId: string,
  claims: number,
  terminalSeq: number,
  secondTerminal: { status: number; code: string },
): void {
  if (!t) throw new Error("S07 recovery: the turn is not in the audit");
  if (claims !== 1) throw new Error(`S07 recovery: turn ${t.id} was handed out by ${claims} claim(s), need exactly 1`);
  if (t.attempt !== 1) throw new Error(`S07 recovery: turn ${t.id} attempt ${t.attempt}, need exactly 1 claim`);
  if (t.runner_id !== runnerId) throw new Error(`S07 recovery: turn ${t.id} is held by runner ${t.runner_id}, not by the recovered one`);
  if (t.state !== "done" || t.stop_reason !== "end_turn") throw new Error(`S07 recovery: turn ${t.id} ended ${t.state}/${t.stop_reason}, need done/end_turn`);
  if (!t.started_at || !t.finished_at) throw new Error(`S07 recovery: turn ${t.id} has no started_at/finished_at`);
  if (t.terminal_seq !== terminalSeq || t.last_seq !== terminalSeq) {
    throw new Error(`S07 recovery: turn ${t.id} terminal_seq ${t.terminal_seq}, last_seq ${t.last_seq}, need one terminal at seq ${terminalSeq}`);
  }
  if (secondTerminal.status !== 409 || secondTerminal.code !== "chat_turn_terminal") {
    throw new Error(`S07 recovery: a second terminal for turn ${t.id} answered ${secondTerminal.status} ${secondTerminal.code || "no code"}, need 409 chat_turn_terminal`);
  }
}

/**
 * S07 expiry: the turn nobody took became expired with a finish time, was never claimed, and its
 * user message is failed with not_delivered. A late runner neither gets it from a claim nor may
 * act on it: start answers 403 chat_not_claim_holder (it never held the turn).
 */
export function assertExpiredNotDelivered(
  t: AuditTurn | undefined,
  user: { status: string; error_code?: string | null } | undefined,
  lateClaimTurnIds: string[],
  lateStart: { status: number; code: string },
): void {
  if (!t) throw new Error("S07 expiry: the turn is not in the audit");
  if (t.state !== "expired") throw new Error(`S07 expiry: turn ${t.id} is ${t.state}, need expired`);
  if (!t.finished_at) throw new Error(`S07 expiry: turn ${t.id} is expired without finished_at`);
  if (t.error_code !== "not_delivered") throw new Error(`S07 expiry: turn ${t.id} error_code ${t.error_code}, need not_delivered`);
  if (t.runner_id || t.attempt !== 0 || t.started_at) throw new Error(`S07 expiry: turn ${t.id} was claimed or started (runner ${t.runner_id}, attempt ${t.attempt})`);
  if (!user || user.status !== "failed" || user.error_code !== "not_delivered") {
    throw new Error(`S07 expiry: the user message is ${user?.status ?? "absent"}/${user?.error_code ?? "none"}, need failed/not_delivered`);
  }
  if (lateClaimTurnIds.includes(t.id)) throw new Error(`S07 expiry: a late claim handed out expired turn ${t.id}`);
  if (lateStart.status !== 403 || lateStart.code !== "chat_not_claim_holder") {
    throw new Error(`S07 expiry: a late start of turn ${t.id} answered ${lateStart.status} ${lateStart.code || "no code"}, need 403 chat_not_claim_holder`);
  }
}

/** S01: the project tab strip in its full order, ids as the strip renders them (`data-tab-id`). */
export function assertTabAfter(order: string[], before: string, tab: string): void {
  const i = order.indexOf(tab);
  if (i < 0) throw new Error(`S01: tab "${tab}" is not in the strip: ${order.join(",")}`);
  const b = order.indexOf(before);
  if (b < 0) throw new Error(`S01: tab "${before}" is not in the strip: ${order.join(",")}`);
  if (i !== b + 1) throw new Error(`S01: tab "${tab}" is at ${i}, not right after "${before}" at ${b}: ${order.join(",")}`);
}

/** S01: a project without the chat has no Chat tab; an empty strip is not proof of that. */
export function assertNoTab(order: string[], tab: string): void {
  if (!order.includes("board")) throw new Error(`S01: the strip is not rendered (no board tab): ${order.join(",") || "empty"}`);
  if (order.includes(tab)) throw new Error(`S01: tab "${tab}" is in the strip of a project without it: ${order.join(",")}`);
}

export interface ChatSettingsView {
  enabled: boolean;
  agents: string[];
  allowed_roles?: string[];
}

/** S01: what a GET of the settings answers is what was saved: on/off, the same agents, the same roles. */
export function assertSettingsReadback(what: string, want: ChatSettingsView, got: ChatSettingsView): void {
  const set = (xs: string[] | undefined) => [...(xs ?? [])].sort().join(",");
  if (got.enabled !== want.enabled) throw new Error(`${what}: enabled=${got.enabled}, saved ${want.enabled}`);
  if (set(got.agents) !== set(want.agents)) throw new Error(`${what}: agents [${set(got.agents)}], saved [${set(want.agents)}]`);
  if (want.allowed_roles && set(got.allowed_roles) !== set(want.allowed_roles)) {
    throw new Error(`${what}: allowed_roles [${set(got.allowed_roles)}], saved [${set(want.allowed_roles)}]`);
  }
}

/** S01: the workspace limits (retention, attachment, idle, expiry) read back unchanged, key by key. */
export function assertLimitsReadback(what: string, want: Record<string, unknown>, got: Record<string, unknown>): void {
  const keys = [...new Set([...Object.keys(want), ...Object.keys(got)])].sort();
  if (keys.length === 0) throw new Error(`${what}: no limits in either answer`);
  const diff = keys.filter((k) => want[k] !== got[k]).map((k) => `${k}=${String(got[k])} (was ${String(want[k])})`);
  if (diff.length > 0) throw new Error(`${what}: limits changed: ${diff.join(", ")}`);
}

/** S01: a chat call in a project with the chat off is refused as not enabled, not served and not a 5xx. */
export function assertChatOff(what: string, status: number, body: string): void {
  if (status >= 200 && status < 300) throw new Error(`${what}: the chat answered ${status} in a project with the chat off`);
  let code = "";
  try { code = (JSON.parse(body) as { error_code?: string }).error_code ?? ""; } catch { code = ""; }
  if (status !== 404 || code !== "chat_not_enabled") throw new Error(`${what}: expected 404 chat_not_enabled, got ${status} ${code || "no error_code"}`);
}

/** S01: switching the chat on with no agent is a validation error that stores nothing. */
export function assertEmptyAgentsRejected(status: number, before: ChatSettingsView, after: ChatSettingsView): void {
  if (status !== 422) throw new Error(`S01: enabled with no agents answered ${status}, expected 422`);
  assertSettingsReadback("S01 settings after the rejected save", before, after);
}
