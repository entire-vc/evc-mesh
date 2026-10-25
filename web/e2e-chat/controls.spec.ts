import { expect, test } from "@playwright/test";
import {
  type Forbidden, type Frame, assertDenied, assertDistinctSessions, assertFramesClean, assertFrozen, assertIntermediateThenFinal,
  assertMemoryAndEventsClean, assertNoDeltaAfter, assertNoLeak, assertOk, assertOpaque404, assertOwnEvents, assertStreaming,
} from "./lib/detect";
import { redControl, row } from "./lib/harness";

/**
 * Red controls of the detectors themselves, with no network and no browser: each
 * detector is fed a known-bad input and must throw, and a known-good input and
 * must not. A detector that cannot fail would turn P01–P07 and S04/S06 into
 * decoration. Results go into results.tsv as RED-OK next to the live rows.
 */
const F: Forbidden = { ids: ["11111111-1111-1111-1111-111111111111"], texts: ["Secret title"], canary: "CANARY-A-xyz" };
const frame = (o: Partial<Frame>): Frame => ({ event: "chat.message.delta", raw: "{}", ...o });

test("S04 streaming: a single final text, one delta, mixed turns and a repeated seq all fail; two good deltas pass", async () => {
  await redControl("S04-red-final-only", "detector", () => assertStreaming([frame({ textLen: 40, turn_id: "t", message_id: "m", seq: 1 })]));
  await redControl("S04-red-empty-deltas", "detector", () => assertStreaming([frame({ textLen: 0, seq: 1 }), frame({ textLen: 0, seq: 2 })]));
  await redControl("S04-red-mixed-turns", "detector", () => assertStreaming([frame({ textLen: 3, turn_id: "a", message_id: "m", seq: 1 }), frame({ textLen: 3, turn_id: "b", message_id: "m", seq: 2 })]));
  await redControl("S04-red-seq-repeat", "detector", () => assertStreaming([frame({ textLen: 3, turn_id: "a", message_id: "m", seq: 2 }), frame({ textLen: 3, turn_id: "a", message_id: "m", seq: 2 })]));
  await redControl("S04-red-no-intermediate", "detector", () => assertIntermediateThenFinal("", "final"));
  await redControl("S04-red-not-prefix", "detector", () => assertIntermediateThenFinal("abc", "xyz-longer"));
  assertStreaming([frame({ textLen: 3, turn_id: "a", message_id: "m", seq: 1 }), frame({ textLen: 5, turn_id: "a", message_id: "m", seq: 2 })]);
  assertIntermediateThenFinal("abc", "abcdef");
});

test("S06 stop: body growth, a status flip and a late delta after the terminal state all fail; a frozen body passes", async () => {
  await redControl("S06-red-body-grows", "detector", () => assertFrozen([{ body: "ab", status: "cancelled" }, { body: "abc", status: "cancelled" }], "cancelled"));
  await redControl("S06-red-done-instead", "detector", () => assertFrozen([{ body: "ab", status: "done" }, { body: "ab", status: "done" }], "cancelled"));
  await redControl("S06-red-empty-partial", "detector", () => assertFrozen([{ body: "", status: "cancelled" }, { body: "", status: "cancelled" }], "cancelled"));
  await redControl("S06-red-late-delta", "detector", () => assertNoDeltaAfter([frame({ message_id: "m", seq: 9 })], "m", 5));
  assertFrozen([{ body: "ab", status: "cancelled" }, { body: "ab", status: "cancelled" }], "cancelled");
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
