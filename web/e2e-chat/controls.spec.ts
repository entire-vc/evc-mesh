import { expect, test } from "@playwright/test";
import {
  type Forbidden, type Frame, assertDenied, assertDistinctSessions, assertFramesClean, assertFrozen, assertIntermediateThenFinal,
  assertEffectiveMode, assertFramesDiffer, assertMarkdownDom, assertReadable, assertMemoryAndEventsClean, assertNoDeltaAfter, assertNoLeak, assertOk,
  assertOpaque404, assertOwnEvents, assertFixtureDisjoint, canary, assertStreaming, assertToolEvents, assertDeltasInFinal, assertEarlyDelta,
  assertTurnRunning, assertReplyCanStillCome, describeReply, type ReplyView,
  type AuditTurn, assertQueuedUnclaimed, assertOneClaimOneTerminal, assertExpiredNotDelivered,

  assertTabAfter, assertNoTab, assertSettingsReadback, assertLimitsReadback, assertChatOff, assertEmptyAgentsRejected,
} from "./lib/detect";
import { waitTurnFrame, withReplyState } from "./lib/chat";
import { F1, redControl, row, until } from "./lib/harness";

/**
 * Red controls of the detectors themselves, with no network and no browser: each
 * detector is fed a known-bad input and must throw, and a known-good input and
 * must not. A detector that cannot fail would turn P01–P07 and S04/S06 into
 * decoration. Results go into results.tsv as RED-OK next to the live rows.
 */
const F: Forbidden = { ids: ["11111111-1111-1111-1111-111111111111"], texts: ["Secret title"], canary: "CANARY-A-xyz" };
const frame = (o: Partial<Frame>): Frame => ({ event: "chat.message.delta", raw: "{}", ...o });

// A delta of message m in turn a carrying `text`, arrived at `at` ms; the completed frame of m.
const delta = (seq: number, text: string, at = 0, o: Partial<Frame> = {}): Frame =>
  frame({ turn_id: "a", message_id: "m", seq, textLen: text.length, at, raw: JSON.stringify({ data: { event: "chat.message.delta", text } }), ...o });
const completed = (seq: number, at = 0): Frame => frame({ event: "chat.message.completed", message_id: "m", seq, at });

test("S04 streaming: completed alone, a delta after completed, empty deltas, mixed turns, a repeated seq and foreign text all fail; a delta before completed passes", async () => {
  await redControl("S04-red-completed-only", "detector", () => assertStreaming([completed(2)], "m"), /0 non-empty delta/);
  await redControl("S04-red-delta-after-completed", "detector", () => assertStreaming([completed(2), delta(3, "The code is VC7K2M.")], "m"), /after chat.message.completed/);
  await redControl("S04-red-no-completed", "detector", () => assertStreaming([delta(1, "abc")], "m"), /no chat.message.completed/);
  await redControl("S04-red-empty-deltas", "detector", () => assertStreaming([delta(1, ""), delta(2, ""), completed(3)], "m"), /0 non-empty delta/);
  await redControl("S04-red-mixed-turns", "detector", () => assertStreaming([delta(1, "ab"), delta(2, "cd", 0, { turn_id: "b" }), completed(3)], "m"), /different turn/);
  await redControl("S04-red-seq-repeat", "detector", () => assertStreaming([delta(2, "ab"), delta(2, "cd"), completed(3)], "m"), /seq not increasing/);
  await redControl("S04-red-delta-not-in-final", "detector", () => assertDeltasInFinal([delta(1, "The code is VC7K2M.")], "The code is VC9999."), /not in the final body/);
  await redControl("S04-red-delta-edge-dropped", "detector", () => assertDeltasInFinal([delta(1, "The code is VC7K2M.\n\n")], "The code is VC7K2M."), /not in the final body/);
  await redControl("S04-red-no-intermediate", "detector", () => assertIntermediateThenFinal("", "final"));
  await redControl("S04-red-not-prefix", "detector", () => assertIntermediateThenFinal("abc", "xyz-longer"));
  await redControl("S04-red-not-prefix-one-block", "detector", () => assertIntermediateThenFinal("The code is VC7K2M.", "The code is VC9999.", false), /not a prefix/);
  await redControl("S04-red-no-intermediate-one-block", "detector", () => assertIntermediateThenFinal("", "The code is VC7K2M.", false), /no intermediate body/);
  const one = assertStreaming([delta(1, "The code is VC7K2M."), completed(2)], "m");
  assertDeltasInFinal(one, "The code is VC7K2M.");
  assertIntermediateThenFinal("abc", "abcdef");
  // A one-block reply is stored whole before completed: equal in-flight and final bodies pass S04.
  assertIntermediateThenFinal("The code is VC7K2M.", "The code is VC7K2M.", false);
});

test("S04b multi-block: one block, a first delta less than 1 s before completed and a missing time all fail; two early deltas pass", async () => {
  await redControl("S04b-red-one-block", "detector", () => assertStreaming([delta(1, "The code is VC7K2M.", 1000), completed(2, 1050)], "m", 2), /1 non-empty delta\(s\), need >= 2/);
  await redControl("S04b-red-late-first-delta", "detector", () => assertEarlyDelta([delta(1, "Let me look.", 9000), delta(2, "VC7K2M", 9400), completed(3, 9600)], "m"), /600 ms before completed/);
  await redControl("S04b-red-no-growth", "detector", () => assertIntermediateThenFinal("Let me look.\n\nThe code is VC7K2M.", "Let me look.\n\nThe code is VC7K2M."), /not longer/);
  await redControl("S04b-red-untimed", "detector", () => assertEarlyDelta([frame({ textLen: 3, message_id: "m" }), completed(2)], "m"), /no timed delta/);
  const good = [delta(1, "Let me look at the picture.", 6950), delta(2, "The code is VC7K2M.", 7970), completed(3, 9690)];
  assertStreaming(good, "m", 2);
  assertEarlyDelta(good, "m");
  assertDeltasInFinal(assertStreaming(good, "m", 2), "Let me look at the picture.\n\nThe code is VC7K2M.");
});

test("S06 stop: body growth, a status flip and a late delta after the terminal state all fail; a frozen body passes", async () => {
  await redControl("S06-red-body-grows", "detector", () => assertFrozen([{ body: "ab", status: "cancelled" }, { body: "abc", status: "cancelled" }], "cancelled"));
  await redControl("S06-red-done-instead", "detector", () => assertFrozen([{ body: "ab", status: "done" }, { body: "ab", status: "done" }], "cancelled"));
  await redControl("S06-red-empty-partial", "detector", () => assertFrozen([{ body: "", status: "cancelled" }, { body: "", status: "cancelled" }], "cancelled"));
  await redControl("S06-red-late-delta", "detector", () => assertNoDeltaAfter([frame({ message_id: "m", seq: 9 })], "m", 5));
  await redControl("S06-red-turn-over", "detector", () => assertTurnRunning({ status: "done" }, 0, 7000), /turn ended before Stop after 7s/);
  await redControl("S06-red-no-reply", "detector", () => assertTurnRunning(undefined, 0, 1000), /turn ended before Stop/);
  await redControl("S06-red-codex-turn-over", "detector", () => assertTurnRunning(undefined, 0, 3000, { active: null }), /turn ended before Stop after 3s \(reply status absent, turn none\)/);
  await redControl("S06-red-codex-reply-done", "detector", () => assertTurnRunning({ status: "done" }, 0, 1000, { active: { state: "streaming" } }), /turn ended before Stop/);
  await redControl("S06-red-codex-turn-ended-reply-streaming", "detector", () => assertTurnRunning({ status: "streaming" }, 0, 2000, { active: null }), /turn ended before Stop after 2s \(reply status streaming, turn none\)/);
  await redControl("S06-red-codex-no-active-turn", "detector", () => assertTurnRunning({ status: "streaming" }, 0, 4000, {}), /turn ended before Stop after 4s \(reply status streaming, turn none\)/);
  await redControl("S06-red-codex-status-flip", "detector", () => assertFrozen([{ body: "", status: "cancelled" }, { body: "", status: "done" }], "cancelled", false));
  assertTurnRunning({ status: "streaming" }, 0);
  assertTurnRunning(undefined, 0, Date.now(), { active: { state: "streaming" } });
  assertFrozen([{ body: "ab", status: "cancelled" }, { body: "ab", status: "cancelled" }], "cancelled");
  assertFrozen([{ body: "", status: "cancelled" }, { body: "", status: "cancelled" }], "cancelled", false);
  assertNoDeltaAfter([frame({ message_id: "m", seq: 4 })], "m", 5);
});

test("P01–P04: a foreign id, title or canary in a body, a 200 for a foreign object and a body naming data all fail", async () => {
  await redControl("P01-red-id", "detector", () => assertNoLeak("list", `{"items":[{"id":"${F.ids[0]}"}]}`, F));
  await redControl("P01-red-title", "detector", () => assertNoLeak("list", "x secret TITLE y", F));
  await redControl("P02-red-canary", "detector", () => assertNoLeak("history", "hello canary-a-XYZ", F));
  await redControl("P02-red-200", "detector", () => assertOpaque404("detail", 200, "{}", F));
  await redControl("P02-red-403", "detector", () => assertOpaque404("detail", 403, "{}", F));
  await redControl("P02-red-error-body-names-title", "detector", () => assertOpaque404("detail", 404, '{"error":"no Secret title"}', F));
  await redControl("P04-red-2xx", "detector", () => assertDenied("rename", 204));
  await redControl("P04-red-500", "detector", () => assertDenied("rename", 500));
  await redControl("P04-red-own-control-broken", "detector", () => assertOk("own rename", 500));
  assertNoLeak("list", `{"items":[{"id":"22222222-2222-2222-2222-222222222222"}]}`, F);
  assertOpaque404("detail", 404, '{"error":"not found"}', F);
  assertDenied("rename", 404);
  assertOk("own", 200);
});

test("AC7 mode: a light page labelled dark, a dark class on a light page, a light page under a dark label and identical frames all fail", async () => {
  await redControl("AC7-red-light-as-dark", "detector", () => assertEffectiveMode("dark", { htmlDark: false, bgLuminance: 0.98 }));
  await redControl("AC7-red-class-only", "detector", () => assertEffectiveMode("dark", { htmlDark: true, bgLuminance: 0.98 }));
  await redControl("AC7-red-dark-as-light", "detector", () => assertEffectiveMode("light", { htmlDark: true, bgLuminance: 0.02 }));
  await redControl("AC7-red-same-frames", "detector", () => assertFramesDiffer("empty 393", "78cff901b534", "78cff901b534"));
  assertEffectiveMode("dark", { htmlDark: true, bgLuminance: 0.01 });
  assertEffectiveMode("light", { htmlDark: false, bgLuminance: 1 });
  assertFramesDiffer("empty 393", "aa", "bb");
});

test("AC3 markdown and tools: no heading, a short list, a wrong code and a turn without tool events all fail", async () => {
  const good = { headings: ["Result"], listItems: 3, codeBlocks: ["0123456789abcdef"] };
  await redControl("AC3-red-no-heading", "detector", () => assertMarkdownDom({ ...good, headings: [] }, "0123456789abcdef"));
  await redControl("AC3-red-short-list", "detector", () => assertMarkdownDom({ ...good, listItems: 2 }, "0123456789abcdef"));
  await redControl("AC3-red-wrong-code", "detector", () => assertMarkdownDom(good, "fedcba9876543210"));
  await redControl("AC3-red-no-tool-frame", "detector", () => assertToolEvents([frame({ event: "chat.turn.tool", turn_id: "other" })], "t", ["Bash"]));
  await redControl("AC3-red-no-tool-dom", "detector", () => assertToolEvents([frame({ event: "chat.turn.tool", turn_id: "t" })], "t", [" "]));
  assertMarkdownDom(good, "0123456789abcdef");
  // The miss quotes what the agent wrote: the first three blocks, trimmed, 80 chars each.
  const blocks = [` ${"a".repeat(100)} `, "b", "c", "dddd"];
  expect(() => assertMarkdownDom({ ...good, codeBlocks: blocks }, "fedcba9876543210")).toThrow(
    `markdown: no <pre><code> block carries fedcba9876543210; blocks: ${JSON.stringify(["a".repeat(80), "b", "c"])}`,
  );
  assertToolEvents([frame({ event: "chat.turn.tool", turn_id: "t" })], "t", ["Working: Bash"]);
});

test("E3 attachment name: white on the light card, near-black on the dark card, a transparent card and an unparsed colour all fail", async () => {
  // The defect as painted in production ea83433: the name inherited the user bubble's primary-foreground over bg-muted.
  await redControl("E3-red-name-light", "detector", () => assertReadable("name light", { fg: "rgb(255, 255, 255)", bg: "rgb(243, 248, 248)" }), /contrast 1\.\d+:1 < 4\.5:1/);
  await redControl("E3-red-name-dark", "detector", () => assertReadable("name dark", { fg: "rgb(6, 15, 15)", bg: "rgb(30, 41, 43)" }), /< 4\.5:1/);
  await redControl("E3-red-transparent", "detector", () => assertReadable("name", { fg: "rgb(0, 0, 0)", bg: "rgba(0, 0, 0, 0)" }), /not opaque/);
  await redControl("E3-red-unparsed", "detector", () => assertReadable("name", { fg: "oklch(0.2 0 0)", bg: "rgb(243, 248, 248)" }), /cannot parse/);
  assertReadable("name light", { fg: "rgb(20, 33, 33)", bg: "rgb(243, 248, 248)" });
  assertReadable("name dark", { fg: "rgb(232, 250, 250)", bg: "rgb(30, 41, 43)" });
});

test("P05–P07: a foreign frame, a silent socket and a shared session all fail", async () => {
  await redControl("P05-red-foreign-frame", "detector", () => assertFramesClean("ws", [frame({ raw: `{"data":{"thread_id":"${F.ids[0]}"}}`, thread_id: F.ids[0] })], F));
  await redControl("P05-red-canary-in-delta", "detector", () => assertFramesClean("ws", [frame({ raw: '{"text":"canary-a-xyz"}' })], F));
  await redControl("P05-red-silent-socket", "detector", () => assertOwnEvents("ws", [], "own-thread"));
  await redControl("P06-red-shared-session", "detector", () => assertDistinctSessions({ A: "s1", B: "s1" }));
  await redControl("P06-red-no-session", "detector", () => assertDistinctSessions({ A: "s1", B: undefined }));
  await redControl("P07-red-canary-in-memory", "detector", () => assertMemoryAndEventsClean([{ thread_id: "t", text: "x CANARY-A-xyz y" }], [], F.canary));
  await redControl("P07-red-body-in-event", "detector", () => assertMemoryAndEventsClean([], [{ has_body: true }], F.canary));
  assertMemoryAndEventsClean([{ thread_id: "t", text: "other" }], [{ has_body: false }], F.canary);
  assertFramesClean("ws", [frame({ raw: '{"data":{"thread_id":"own"}}', thread_id: "own", event: "chat.turn.state" })], F);
  assertOwnEvents("ws", [frame({ thread_id: "own", event: "chat.turn.state" })], "own");
  assertDistinctSessions({ A: "s1", B: "s2" });
  row("CONTROLS", "detector", "PASS", "all detector red controls fired");
  expect(true).toBe(true);
});

test("P05 fixture: an own canary never spells A's title or file name; the old dash-separated one did", async () => {
  const run = "chat-19525-mh7k2a1b";
  const fA: Forbidden = { ids: [], texts: [`A-${run}.png`, `${run}-A`], canary: canary("A", run, "a1b2c3d4") };
  await redControl("P05-red-fixture-collision", "detector", () => assertFixtureDisjoint("fixture B", [`CANARY-B-${run}-a1b2c3d4`], fA), /fixture collision/);
  for (const c of "0123456789abcdefghijklmnopqrstuvwxyz") {
    assertFixtureDisjoint("fixture B", [canary("B", run, `${c}1b2c3d4`), canary("C", run, `${c}png`), `B-${run}.png`, `${run}-B-disposable`], fA);
  }
  expect(canary("B", run, "a1b2c3d4")).toBe(`CANARY-B-${run}_a1b2c3d4`);
});

/** A page stand-in that replays console and response events into F1 the way Playwright delivers them. */
function f1With(events: Array<{ console?: string; response?: { method: string; path: string; status: number } }>): F1 {
  const handlers: Record<string, (x: unknown) => void> = {};
  const f1 = new F1();
  f1.attach({ on: (name: string, h: (x: unknown) => void) => void (handlers[name] = h) } as never);
  const url = (path: string) => `https://mesh.example${path}`;
  for (const e of events) {
    if (e.console) {
      const [text, path] = e.console.split(" @ ");
      handlers.console!({ type: () => "error", text: () => text, location: () => ({ url: path ? url(path) : "" }) });
    }
    if (e.response) {
      const r = e.response;
      handlers.response!({ url: () => url(r.path), status: () => r.status, request: () => ({ method: () => r.method }) });
    }
  }
  return f1;
}

test("F1: an unset ui-state 404 is excused on the response and its console line; other 404s and a PUT 404 there still fail", async () => {
  const unset = "/api/v1/me/ui-state/appearance";
  const consoleLine = "Failed to load resource: the server responded with a status of 404 ()";
  await redControl("F1-red-other-console-404", "detector", () =>
    f1With([{ console: `${consoleLine} @ /api/v1/chat/threads`, response: { method: "GET", path: "/api/v1/chat/threads", status: 404 } }]).verify("detector", false, false));
  await redControl("F1-red-ui-state-put-404", "detector", () =>
    f1With([{ console: `${consoleLine} @ ${unset}`, response: { method: "PUT", path: unset, status: 404 } }]).verify("detector", false, false));
  await redControl("F1-red-ui-state-500", "detector", () =>
    f1With([{ console: `Failed to load resource: the server responded with a status of 500 () @ ${unset}`, response: { method: "GET", path: unset, status: 500 } }]).verify("detector", false, false));
  f1With([{ console: `${consoleLine} @ ${unset}`, response: { method: "GET", path: unset, status: 404 } }]).verify("detector", false, false);
});

test("REPLY-WAIT: a reply that ended without text stops the wait and names its error; the timeout line names the turn's phase and never the body", async () => {
  const user = { id: "u1", role: "user", status: "done", body: "Name the visual code CANARY-A-xyz" };
  // job 436006, S04 codex: harness_error at 62 s with an empty body, then 3 min of silent waiting.
  const failed: ReplyView = { items: [user, { id: "r1", role: "assistant", status: "failed", body: "", error_code: "harness_error" }], active_turn: null };
  await redControl("REPLY-red-failed-empty", "detector", () => assertReplyCanStillCome(failed, "t1", 1), /ended failed without a body.*status failed, error_code harness_error, body 0 chars/);
  await redControl("REPLY-red-cancelled-empty", "detector", () =>
    assertReplyCanStillCome({ items: [{ id: "r1", role: "assistant", status: "cancelled", body: "" }], active_turn: null }, "t1", 1), /ended cancelled/);
  // Still coming: no reply yet, an empty streaming reply, a failed reply that kept its text.
  assertReplyCanStillCome({ items: [user], active_turn: { id: "k", state: "queued", queue_position: 2 } }, "t1", 1);
  assertReplyCanStillCome({ items: [user, { id: "r1", role: "assistant", status: "streaming", body: "" }], active_turn: { id: "k", state: "streaming" } }, "t1", 1);
  assertReplyCanStillCome({ items: [user, { id: "r1", role: "assistant", status: "failed", body: "partial" }], active_turn: null }, "t1", 1);
  // job 436006, P04 B: the own turn sat queued behind other actors' turns for the whole budget.
  const queued = describeReply({ items: [user], active_turn: { id: "k", state: "queued", queue_position: 2 } }, "t1", 1);
  expect(queued).toBe("thread t1: turn k queued at queue position 2; reply #1 absent (0 assistant message(s)), status none, error_code none, body 0 chars");
  const ended = describeReply({ items: [user, { id: "r1", role: "assistant", status: "streaming", body: "The code is CANARY-A-xyz" }], active_turn: { id: "k", state: "streaming" } }, "t1", 1);
  expect(ended).toBe("thread t1: turn k streaming; reply #1 r1 status streaming, error_code none, body 24 chars");
  // The line carries lengths, not text: the leak check that catches a line quoting the body passes it.
  await redControl("REPLY-red-body-in-line", "detector", () => assertNoLeak("reply-wait line", `${ended} The code is CANARY-A-xyz`, F), /foreign data present/);
  assertNoLeak("reply-wait line", `${queued} ${ended} ${describeReply(failed, "t1", 1)}`, F);
});

test("REPLY-WAIT: a timed-out wait carries the turn's state; any other error passes through unchanged", async () => {
  const queued: ReplyView = { items: [{ id: "u1", role: "user", status: "done", body: "Name the visual code CANARY-A-xyz" }], active_turn: { id: "k", state: "queued", queue_position: 2 } };
  const read = async () => queued;
  const never = () => until("assistant reply #1 terminal", async () => false, 20, 5);
  await redControl("REPLY-red-timeout-state", "detector", () => withReplyState(read, "t1", 1, never),
    /^timed out after 20 ms waiting for: assistant reply #1 terminal; thread t1: turn k queued at queue position 2; reply #1 absent \(0 assistant message\(s\)\), status none, error_code none, body 0 chars$/);
  await redControl("REPLY-red-timeout-unreadable", "detector", () => withReplyState(async () => { throw new Error("history 500\nstack"); }, "t1", 1, never),
    /timed out after 20 ms.*; thread t1: state unreadable \(history 500\); turn unknown; reply #1 status unknown, error_code unknown, body unknown$/);
  const other = new Error("reply #1 ended failed without a body");
  await redControl("REPLY-red-other-error", "detector", () => withReplyState(read, "t1", 1, async () => { throw other; }), /^reply #1 ended failed without a body$/);
  await withReplyState(read, "t1", 1, async () => { throw other; }).catch((e) => expect(e).toBe(other));
  // An error that only reads like a timeout is not one: it passes through without a state line.
  const lookalike = new Error("timed out after 5 ms waiting for: something else");
  await redControl("REPLY-red-timeout-lookalike", "detector", () => withReplyState(read, "t1", 1, async () => { throw lookalike; }), /^timed out after 5 ms waiting for: something else$/);
  await withReplyState(read, "t1", 1, async () => { throw lookalike; }).catch((e) => expect(e).toBe(lookalike));
  let line = "";
  await withReplyState(read, "t1", 1, never).catch((e: Error) => { line = e.message; });
  assertNoLeak("reply-wait timeout line", line, F);
  expect(await withReplyState(read, "t1", 1, async () => "ok")).toBe("ok");
});

test("REPLY-WAIT: a wait for a running turn's frame stops on a reply that ended without text and names the turn on timeout", async () => {
  // job 442021, P05 B: T-A failed with harness_error in 4 s, the wait for its first delta ran the full 240 s.
  const failed: ReplyView = { items: [{ id: "r1", role: "assistant", status: "failed", body: "", error_code: "harness_error" }], active_turn: null };
  const t0 = Date.now();
  await redControl("REPLY-red-frame-failed", "detector", () => waitTurnFrame(async () => failed, "t1", 1, "A's stream starts", () => undefined, 60_000, 10),
    /^reply #1 ended failed without a body: .*status failed, error_code harness_error, body 0 chars$/);
  expect(Date.now() - t0, "the failed reply ends the wait long before its budget").toBeLessThan(5_000);
  const queued: ReplyView = { items: [], active_turn: { id: "k", state: "claimed" } };
  await redControl("REPLY-red-frame-timeout", "detector", () => waitTurnFrame(async () => queued, "t1", 1, "A's stream starts", () => undefined, 20, 5),
    /^timed out after 20 ms waiting for: A's stream starts; thread t1: turn k claimed; reply #1 absent .*status none, error_code none, body 0 chars$/);
  // A frame that arrives wins; a streaming reply without text yet keeps the wait going.
  const streaming: ReplyView = { items: [{ id: "r1", role: "assistant", status: "streaming", body: "" }], active_turn: { id: "k", state: "streaming" } };
  let n = 0;
  expect(await waitTurnFrame(async () => streaming, "t1", 1, "A's stream starts", () => (++n > 3 ? "delta" : undefined), 5_000, 1)).toBe("delta");
});

// S07 turns as the audit reports them: an untouched queued one, the one the recovered runner answered, an expired one.
const turn = (o: Partial<AuditTurn>): AuditTurn => ({
  id: "t", state: "queued", runner_id: null, attempt: 0, first_claimed_at: null, started_at: null, finished_at: null,
  error_code: null, stop_reason: null, last_seq: 0, terminal_seq: null, ...o,
});
const answered = turn({ state: "done", runner_id: "r", attempt: 1, first_claimed_at: "x", started_at: "x", finished_at: "x", stop_reason: "end_turn", last_seq: 2, terminal_seq: 2 });
const expiredTurn = turn({ state: "expired", finished_at: "x", error_code: "not_delivered" });
const notDelivered = { status: "failed", error_code: "not_delivered" };
const refusedTerminal = { status: 409, code: "chat_turn_terminal" };
const refusedStart = { status: 403, code: "chat_not_claim_holder" };

test("S07-recovery: a claimed, started or missing queued turn, two claims, a second attempt, another runner and a second terminal all fail; one claim and one terminal pass", async () => {
  await redControl("S07-red-queued-claimed", "detector", () => assertQueuedUnclaimed(turn({ state: "claimed", runner_id: "r", attempt: 1 })), /not an untouched queued turn/);
  await redControl("S07-red-queued-started", "detector", () => assertQueuedUnclaimed(turn({ started_at: "x" })), /started_at set/);
  await redControl("S07-red-queued-absent", "detector", () => assertQueuedUnclaimed(undefined), /not in the audit/);
  await redControl("S07-red-two-claims", "detector", () => assertOneClaimOneTerminal(answered, "r", 2, 2, refusedTerminal), /handed out by 2 claim/);
  await redControl("S07-red-second-attempt", "detector", () => assertOneClaimOneTerminal({ ...answered, attempt: 2 }, "r", 1, 2, refusedTerminal), /attempt 2/);
  await redControl("S07-red-other-runner", "detector", () => assertOneClaimOneTerminal(answered, "other", 1, 2, refusedTerminal), /not by the recovered one/);
  await redControl("S07-red-not-done", "detector", () => assertOneClaimOneTerminal({ ...answered, state: "failed", stop_reason: null }, "r", 1, 2, refusedTerminal), /need done\/end_turn/);
  await redControl("S07-red-second-terminal-applied", "detector", () => assertOneClaimOneTerminal({ ...answered, last_seq: 3 }, "r", 1, 2, refusedTerminal), /need one terminal at seq 2/);
  await redControl("S07-red-second-terminal-accepted", "detector", () => assertOneClaimOneTerminal(answered, "r", 1, 2, { status: 200, code: "" }), /second terminal .* answered 200/);
  assertQueuedUnclaimed(turn({}));
  assertOneClaimOneTerminal(answered, "r", 1, 2, refusedTerminal);
});

test("S07-expiry: a queued or claimed turn, no finish time, a delivered message, a late claim that gets the turn and a late start that is not refused all fail; an expired, not delivered turn passes", async () => {
  await redControl("S07-red-still-queued", "detector", () => assertExpiredNotDelivered(turn({}), notDelivered, [], refusedStart), /is queued, need expired/);
  await redControl("S07-red-no-finished-at", "detector", () => assertExpiredNotDelivered({ ...expiredTurn, finished_at: null }, notDelivered, [], refusedStart), /without finished_at/);
  await redControl("S07-red-wrong-code", "detector", () => assertExpiredNotDelivered({ ...expiredTurn, error_code: "runner_error" }, notDelivered, [], refusedStart), /need not_delivered/);
  await redControl("S07-red-expired-was-claimed", "detector", () => assertExpiredNotDelivered({ ...expiredTurn, runner_id: "r", attempt: 1 }, notDelivered, [], refusedStart), /was claimed or started/);
  await redControl("S07-red-message-delivered", "detector", () => assertExpiredNotDelivered(expiredTurn, { status: "done", error_code: null }, [], refusedStart), /user message is done/);
  await redControl("S07-red-late-claim-gets-it", "detector", () => assertExpiredNotDelivered(expiredTurn, notDelivered, ["t"], refusedStart), /late claim handed out/);
  await redControl("S07-red-late-start-accepted", "detector", () => assertExpiredNotDelivered(expiredTurn, notDelivered, [], { status: 200, code: "" }), /late start .* answered 200/);
  assertExpiredNotDelivered(expiredTurn, notDelivered, ["other"], refusedStart);
});

test("S01 enable: Chat missing, after Updates or before Docs, a tab in a chat-off project, a changed readback and a served chat-off call all fail", async () => {
  const builtin = ["board", "list", "timeline", "calendar", "docs", "updates"];
  const good = ["board", "list", "timeline", "calendar", "docs", "chat", "updates", "ext:gitlab"];
  await redControl("S01-red-tab-missing", "detector", () => assertTabAfter(builtin, "docs", "chat"), /"chat" is not in the strip/);
  await redControl("S01-red-tab-after-updates", "detector", () => assertTabAfter([...builtin, "chat"], "docs", "chat"), /not right after "docs"/);
  await redControl("S01-red-tab-before-docs", "detector", () => assertTabAfter(["board", "chat", "docs"], "docs", "chat"), /not right after "docs"/);
  await redControl("S01-red-tab-in-off-project", "detector", () => assertNoTab(good, "chat"), /in the strip of a project without it/);
  await redControl("S01-red-strip-not-rendered", "detector", () => assertNoTab([], "chat"), /strip is not rendered/);
  const saved = { enabled: true, agents: ["a1", "a2"], allowed_roles: ["admin", "member"] };
  await redControl("S01-red-readback-off", "detector", () => assertSettingsReadback("S01", saved, { ...saved, enabled: false }), /enabled=false/);
  await redControl("S01-red-readback-agents", "detector", () => assertSettingsReadback("S01", saved, { ...saved, agents: ["a1"] }), /agents \[a1\]/);
  await redControl("S01-red-readback-roles", "detector", () => assertSettingsReadback("S01", saved, { ...saved, allowed_roles: ["admin"] }), /allowed_roles/);
  const limits = { history_retention_days: 0, max_attachment_mb: 25, session_idle_minutes: 10, offline_expiry_hours: 24 };
  await redControl("S01-red-limits-changed", "detector", () => assertLimitsReadback("S01", limits, { ...limits, session_idle_minutes: 5 }), /session_idle_minutes=5/);
  await redControl("S01-red-limits-empty", "detector", () => assertLimitsReadback("S01", {}, {}), /no limits/);
  await redControl("S01-red-off-served", "detector", () => assertChatOff("S01", 201, "{}"), /answered 201/);
  await redControl("S01-red-off-other-code", "detector", () => assertChatOff("S01", 404, JSON.stringify({ error_code: "thread_not_found" })), /expected 404 chat_not_enabled/);
  await redControl("S01-red-off-5xx", "detector", () => assertChatOff("S01", 500, "Internal Server Error"), /expected 404 chat_not_enabled/);
  await redControl("S01-red-empty-agents-saved", "detector", () => assertEmptyAgentsRejected(200, saved, saved), /answered 200/);
  await redControl("S01-red-empty-agents-stored", "detector", () => assertEmptyAgentsRejected(422, saved, { ...saved, agents: [] }), /agents \[\]/);
  assertTabAfter(good, "docs", "chat");
  assertNoTab(builtin, "chat");
  assertSettingsReadback("S01", saved, { enabled: true, agents: ["a2", "a1"], allowed_roles: ["member", "admin"] });
  assertLimitsReadback("S01", limits, { ...limits });
  assertChatOff("S01", 404, JSON.stringify({ error_code: "chat_not_enabled" }));
  assertEmptyAgentsRejected(422, saved, saved);
});
