import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "@playwright/test";
import { OUT_DIR, RUN_ID, SetupFail } from "./lib/env";
import { type AuditTurn, assertExpiredNotDelivered, assertOneClaimOneTerminal, assertQueuedUnclaimed } from "./lib/detect";
import { type Actor, type Ctx, chatUrl, openActor, resolveCtx, snap, step, timeline, until } from "./lib/harness";
import { agentId, history, newChatViaUi, sendVia, waitAssistantDone } from "./lib/chat";
import { TestRunner } from "./lib/runner";

/**
 * S07 to the end (Mesh Doc e2e/agent-chat, an internal ticket): a message to the offline agent waits in
 * the queue; when the agent's runner comes back it is claimed exactly once and answered exactly once
 * (S07-recovery); a message nobody takes within the offline TTL becomes expired, the UI says "not
 * delivered", and a runner that comes later neither gets it nor may act on it (S07-expiry).
 *
 * The returning runner is TestRunner: the offline fixture agent with a temporary key, nothing else
 * of the fleet. The offline TTL is the workspace setting offline_expiry_hours, set to its minimum
 * (1 h) in the e2e-ci-sandbox workspace for the run and restored afterwards (here and, as a net, in
 * global-teardown); prod clocks and other workspaces are not touched. The 1 h wait is why this
 * file runs as its own project before agent-chat and why the job's timeout covers it.
 *
 * Serial: the expiry turn is sent only after the recovery turn is done, so the recovered runner's
 * claim cannot take it, and the late claim runs before any other spec queues for the offline agent.
 */
test.describe.configure({ mode: "serial" });

const WS_BASELINE = join(OUT_DIR, ".ws-chat-settings-baseline.json");
const TTL_HOURS = 1;
// Sweeper period (about a minute) plus slack on top of the TTL.
const EXPIRY_SLACK_MS = 5 * 60_000;

let A: Actor;
let C: Actor;
let x: Ctx;
let runner: TestRunner | undefined;
let wsBaseline: Record<string, unknown> | undefined;

const auditTurn = async (threadId: string, turnId: string): Promise<AuditTurn | undefined> => {
  const res = await C.fetch("GET", `/api/v1/workspaces/${x.workspaceId}/chat/audit/threads/${threadId}`);
  if (res.status !== 200) throw new SetupFail(`thread audit answered ${res.status} for the QA actor C`);
  return ((JSON.parse(res.text) as { turns?: AuditTurn[] }).turns ?? []).find((t) => t.id === turnId);
};

async function restoreWorkspaceSettings(): Promise<void> {
  if (!wsBaseline) return;
  const url = `/api/v1/workspaces/${x.workspaceId}/chat/settings`;
  const put = await C.api("PUT", url, wsBaseline);
  if (put.status !== 200) throw new Error(`restoring workspace chat settings answered ${put.status}`);
  const back = (await C.api("GET", url)).json() as Record<string, unknown>;
  if (back.offline_expiry_hours !== wsBaseline.offline_expiry_hours) {
    throw new Error(`workspace offline_expiry_hours reads back ${String(back.offline_expiry_hours)}, baseline ${String(wsBaseline.offline_expiry_hours)}`);
  }
  wsBaseline = undefined;
}

/** Sends `text` to a new chat with the offline agent through the UI; returns the thread and its turn. */
async function sendOffline(text: string): Promise<{ threadId: string; turnId: string }> {
  const threadId = await newChatViaUi(A, x, "offline");
  await A.page.getByTestId("chat-input").fill(text);
  const sent = await sendVia(A.page, threadId, () => A.page.getByTestId("chat-send").click());
  expect(sent.status, "the send is accepted (202) while the agent is offline").toBe(202);
  expect(sent.turnId, "the send names its turn").toBeTruthy();
  return { threadId, turnId: sent.turnId! };
}

test.beforeAll(async ({ browser }) => {
  A = await openActor(browser, "A");
  C = await openActor(browser, "C");
  x = await resolveCtx(C);
  if (!agentId("offline")) throw new SetupFail("agent id for offline is not configured");
  const offered = ((await A.api("GET", `/api/v1/projects/${x.projectId}/chat/agents`)).json() as { items: Array<{ agent_id: string; presence: string }> }).items;
  if (offered.find((i) => i.agent_id === agentId("offline"))?.presence !== "offline") throw new SetupFail("the offline agent fixture is not offered as offline: S07 would not exercise the queue");
  runner = await TestRunner.start(C, agentId("offline"), `e2e-chat-${RUN_ID}-s07`, 2 * 3600);
});

test.afterAll(async () => {
  const errors: string[] = [];
  await restoreWorkspaceSettings().catch((e: Error) => errors.push(e.message));
  await runner?.close().catch((e: Error) => errors.push(e.message));
  await A?.close();
  await C?.close();
  if (errors.length > 0) throw new Error(errors.join(" | "));
});

test("S07-recovery: the queued message stays unclaimed; the returning runner claims it exactly once and answers exactly once", async () => {
  await step("S07-recovery", "offline", async () => {
    const text = `${RUN_ID}-s07-recovery`;
    const { threadId, turnId } = await sendOffline(text);

    const queued = await history(A, x, threadId);
    expect(queued.items.filter((m) => m.role === "user").map((m) => m.status), "the user message waits in the queue").toEqual(["queued"]);
    expect(queued.active_turn?.state, "one turn, queued").toBe("queued");
    assertQueuedUnclaimed(await auditTurn(threadId, turnId));

    // The runner comes back: one claim must carry exactly this turn and nothing of anyone else.
    const first = await runner!.claim(10);
    const mine = first.turns.filter((t) => t.turn_id === turnId);
    const foreign = first.turns.filter((t) => t.turn_id !== turnId).map((t) => t.turn_id);
    if (foreign.length > 0) throw new Error(`the recovered runner's claim carried foreign queued turn(s) of the offline agent: ${foreign.join(", ")}`);
    expect(mine, "the claim hands out the queued turn").toHaveLength(1);
    const { claim_token: token } = mine[0]!;

    const started = await runner!.start(turnId, token);
    expect(started.status, `start answered ${started.status} ${started.code}`).toBe(200);
    const reply = `${RUN_ID}-s07-reply`;
    const sent = await runner!.events(turnId, token, [
      { seq: 1, type: "text_delta", text: reply },
      { seq: 2, type: "final", text: reply, stop_reason: "end_turn" },
    ]);
    expect(sent.status, `events answered ${sent.status} ${sent.code}`).toBe(200);
    expect(sent.json.state, "the final event ends the turn").toBe("done");

    // No second delivery: a later claim does not hand the turn out again, a second terminal is refused.
    const again = await runner!.claim(3);
    const claims = 1 + again.turns.filter((t) => t.turn_id === turnId).length;
    const second = await runner!.events(turnId, token, [{ seq: 3, type: "final", text: reply, stop_reason: "end_turn" }]);
    const t = await auditTurn(threadId, turnId);
    assertOneClaimOneTerminal(t, runner!.runnerId, claims, 2, { status: second.status, code: second.code });
    // AC2 of the card reads this line in the job log.
    console.log(`[S07-recovery] turn=${turnId} claims=${claims} attempt=${t!.attempt} state=${t!.state} terminal_seq=${t!.terminal_seq} second_terminal=${second.status} ${second.code}`);
    timeline("s07-recovery", [{ turn: turnId, claims, attempt: t!.attempt, state: t!.state, terminal_seq: t!.terminal_seq, second_terminal: `${second.status} ${second.code}` }]);

    const answer = await waitAssistantDone(A, x, threadId, 1, 60_000);
    expect(answer.status).toBe("done");
    expect(answer.body).toBe(reply);
    const settled = await history(A, x, threadId);
    expect(settled.items.filter((m) => m.role === "user").map((m) => m.status), "the user message is delivered").toEqual(["done"]);
    expect(settled.items.filter((m) => m.role === "assistant"), "exactly one reply").toHaveLength(1);
    await A.page.goto(chatUrl(x, threadId));
    await expect(A.page.locator('[data-testid="chat-message"][data-role="assistant"]').filter({ hasText: reply }), "the reply is in the UI").toBeVisible({ timeout: 30_000 });
  });
});

test("S07-expiry: a message nobody takes within the offline TTL is expired, shown as not delivered, and a late runner cannot take it", async () => {
  test.setTimeout(TTL_HOURS * 3600_000 + EXPIRY_SLACK_MS + 15 * 60_000);
  await step("S07-expiry", "offline", async () => {
    const url = `/api/v1/workspaces/${x.workspaceId}/chat/settings`;
    const base = await C.api("GET", url);
    if (base.status !== 200) throw new SetupFail(`GET workspace chat settings answered ${base.status}`);
    wsBaseline = base.json() as Record<string, unknown>;
    // Written before the change: global-teardown restores from this file if the spec dies first.
    writeFileSync(WS_BASELINE, base.text);
    const put = await C.api("PUT", url, { ...wsBaseline, offline_expiry_hours: TTL_HOURS });
    expect(put.status, `setting offline_expiry_hours=${TTL_HOURS} in ${x.wsSlug} answered ${put.status}`).toBe(200);

    const { threadId, turnId } = await sendOffline(`${RUN_ID}-s07-expiry`);
    const sentAt = Date.now();
    assertQueuedUnclaimed(await auditTurn(threadId, turnId));

    const expired = await until(`turn ${turnId} expired by the sweeper`, async () => {
      const t = await auditTurn(threadId, turnId);
      if (t && t.state !== "queued" && t.state !== "expired") throw new Error(`S07 expiry: turn ${turnId} left the queue as ${t.state} before its TTL`);
      return t?.state === "expired" ? t : false;
    }, TTL_HOURS * 3600_000 + EXPIRY_SLACK_MS, 30_000);
    const waitedMin = Math.round((Date.now() - sentAt) / 60_000);
    await restoreWorkspaceSettings();

    // The runner comes back too late: the claim must not carry the turn and it may not start it.
    const late = await runner!.claim(3);
    const lateStart = await runner!.start(turnId, "late-runner-never-held-this-turn");
    const user = (await history(A, x, threadId)).items.find((m) => m.role === "user");
    assertExpiredNotDelivered(expired, user, late.turns.map((t) => t.turn_id), { status: lateStart.status, code: lateStart.code });
    const after = await auditTurn(threadId, turnId);
    expect(after?.state, "the late runner changed nothing").toBe("expired");
    console.log(`[S07-expiry] turn=${turnId} state=${expired.state} finished_at=${expired.finished_at} error_code=${expired.error_code} waited_min=${waitedMin} late_claim_turns=${late.turns.length} late_start=${lateStart.status} ${lateStart.code}`);
    timeline("s07-expiry", [{ turn: turnId, state: expired.state, finished_at: expired.finished_at, waited_min: waitedMin, late_claim_turns: late.turns.length, late_start: `${lateStart.status} ${lateStart.code}` }]);

    // The UI after a reload (no live turn event): the bubble itself says not delivered.
    await A.page.goto(chatUrl(x, threadId));
    const bubble = A.page.locator('[data-testid="chat-message"][data-role="user"][data-status="failed"]');
    await expect(bubble.getByTestId("chat-not-delivered"), "the bubble says not delivered").toBeVisible({ timeout: 30_000 });
    await snap(A, "s07-not-delivered", undefined, undefined, [bubble.getByTestId("chat-not-delivered")]);
  });
});
