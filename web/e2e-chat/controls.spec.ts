import { expect, test } from "@playwright/test";
import {
  type Forbidden, type Frame, assertDenied, assertDistinctSessions, assertFramesClean, assertFrozen, assertIntermediateThenFinal,
  assertEffectiveMode, assertFramesDiffer, assertMarkdownDom, assertReadable, assertMemoryAndEventsClean, assertNoDeltaAfter, assertNoLeak, assertOk,
  assertOpaque404, assertOwnEvents, assertFixtureDisjoint, canary, assertStreaming, assertToolEvents, assertDeltasInFinal, assertEarlyDelta,
  assertTurnRunning,
} from "./lib/detect";
import { F1, redControl, row } from "./lib/harness";

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
