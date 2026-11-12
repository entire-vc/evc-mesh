import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { OUT_DIR, RUN_ID, SetupFail, type Harness } from "./lib/env";
import { type Actor, type Ctx, chatBase, openActor, resolveCtx, step, timeline, until, UntilTimeout } from "./lib/harness";
import { agentId, history, newChatViaUi, sendVia, waitAssistantDone } from "./lib/chat";
import { type ThreadRuntime, assertIdleHistory, assertIdleResources, assertIdleResumed, assertNotReleasedEarly, resourcesFromRuntime } from "./lib/idle";

/**
 * S08, once per harness: a settled chat with no active
 * turn sits idle for the fixture runners' session_idle_minutes (N = 10, runner.toml of both, set on
 * e2e-ci-sandbox only; prod is not touched). Then: history unchanged; the runner snapshot read from
 * GET /chat/threads/{id}/runtime says process_alive=false and slot_held=false by
 * N + 3 min (N + reap 30 s + claim long-poll 35 s + snapshot freshness); then one short click-sent
 * message ends done, with the old messages neither lost nor duplicated.
 * Before N the same probe must say alive (positive control: the probe can see a live session).
 */
test.describe.configure({ mode: "serial" });

const HARNESSES: Harness[] = ["claude", "codex"];
const IDLE_MIN = Number(process.env.E2E_CHAT_SESSION_IDLE_MINUTES || 10);
const DEADLINE_MS = (IDLE_MIN + 3) * 60_000;

/** What decides whether a click on send can go out (chat-composer canSend), read from the live DOM; no message bodies. */
async function sendState(page: Page): Promise<Record<string, unknown>> {
  return page.evaluate(() => {
    const q = (id: string) => document.querySelector(`[data-testid="${id}"]`);
    const send = q("chat-send") as HTMLButtonElement | null;
    const input = q("chat-input") as HTMLTextAreaElement | null;
    return {
      sendPresent: !!send,
      sendDisabled: send?.disabled ?? null,
      inputDisabled: input?.disabled ?? null,
      inputLength: input?.value.length ?? null,
      stopShown: !!q("chat-stop"), // the composer shows Stop while a turn is active (busy)
      sessionState: q("chat-session-state")?.getAttribute("data-state") ?? null,
      pendingAttachments: document.querySelectorAll('[data-testid="chat-pending-attachment"]').length,
      feedLoading: !!q("chat-feed-loading"),
      notFound: !!q("chat-not-found"),
      navigatorOnLine: navigator.onLine,
      visibility: document.visibilityState,
      path: location.pathname,
    };
  });
}

/** Failure evidence for the post-idle send: the state above and an aria snapshot of the page, both into the job artifacts. */
async function captureSendFailure(page: Page, h: Harness): Promise<string> {
  const state = await sendState(page).catch((e) => ({ stateError: (e as Error).message.slice(0, 120) }));
  try {
    writeFileSync(join(OUT_DIR, `s08-${h}-after-idle-page.aria.txt`), await page.locator("body").ariaSnapshot({ timeout: 10_000 }));
  } catch {
    /* the snapshot is evidence, its loss must not hide the failure */
  }
  writeFileSync(join(OUT_DIR, `s08-${h}-after-idle-send-state.json`), JSON.stringify(state, null, 1));
  return JSON.stringify(state);
}

for (const h of HARNESSES) {
  test.describe(`S08 ${h}`, () => {
    let A: Actor;
    let C: Actor;
    let x: Ctx;

    test.beforeAll(async ({ browser }) => {
      A = await openActor(browser, "A");
      C = await openActor(browser, "C");
      x = await resolveCtx(C);
      if (!agentId(h)) throw new SetupFail(`agent id for ${h} is not configured`);
    });
    test.afterAll(async () => {
      await A?.close();
      await C?.close();
    });

    const runtime = async (threadId: string): Promise<ThreadRuntime | undefined> => {
      const res = await A.api("GET", `${chatBase(x)}/threads/${threadId}/runtime`);
      // The endpoint serves only the thread's own author (another user's thread is 404 thread_not_found), so it is read as A.
      if (res.status !== 200) throw new SetupFail(`runtime probe answered ${res.status}`);
      return res.json() as ThreadRuntime;
    };

    test(`S08 ${h}: idle frees the process and the slot; the next turn resumes without duplicates`, async () => {
      test.setTimeout(DEADLINE_MS + 15 * 60_000);
      await step("S08", h, async () => {
        const threadId = await newChatViaUi(A, x, h);
        await A.page.getByTestId("chat-input").fill(`${RUN_ID} S08 ${h}: reply with one short word.`);
        const first = await sendVia(A.page, threadId, () => A.page.getByTestId("chat-send").click());
        expect(first.status, "the first send is accepted").toBe(202);
        await waitAssistantDone(A, x, threadId, 1);
        const settledAt = Date.now();
        const before = await history(A, x, threadId);

        // Positive control: right after the turn the probe sees a live session, so a later "false" means something.
        const live = await until("runtime snapshot shows the live session", async () => {
          const rt = await runtime(threadId);
          return rt?.process_alive === true && rt.slot_held === true ? rt : false;
        }, 60_000, 3000);
        expect(live.slot_held, "positive control: slot held before idle").toBe(true);

        // Idle: the deadline is absolute, the poll is slow; no active turn may appear meanwhile.
        const idleSince = settledAt;
        let seen: ThreadRuntime | undefined;
        try {
          seen = await until(`no chat process and no slot ${IDLE_MIN} min + 3 min after the turn`, async () => {
            // History during idle: no active turn, no replay, nothing changed, on every poll.
            assertIdleHistory(before, await history(A, x, threadId));
            const rt = await runtime(threadId);
            try {
              const res = resourcesFromRuntime(rt, idleSince, Date.now());
              assertNotReleasedEarly(rt!, idleSince, IDLE_MIN); // lower bound: gone before N - 60 s is a failure, not a pass
              assertIdleResources(res);
            } catch (e) {
              if (/still alive|still occupied|stale|predates|not confirmed fresh/.test((e as Error).message)) return false;
              throw e;
            }
            return rt;
          }, Math.max(1, idleSince + DEADLINE_MS - Date.now()), 15_000);
        } catch (e) {
          if (e instanceof UntilTimeout) assertIdleResources(resourcesFromRuntime(await runtime(threadId), idleSince, Date.now()));
          throw e;
        }
        // until() accepts a result that arrives after its budget ran out; the N + 3 min deadline is absolute.
        if (Date.now() > idleSince + DEADLINE_MS) throw new Error(`S08 idle: release observed after the N + 3 min deadline`);
        const freedAfterMin = Math.round((Date.now() - idleSince) / 600) / 100;
        assertIdleHistory(before, await history(A, x, threadId));

        // The next turn, sent by click, resumes or restores from Mesh.
        const body = `${RUN_ID} S08 ${h}: second short message.`;
        await A.page.getByTestId("chat-input").fill(body);
        // The send control must be usable after idle: a disabled button is a product finding, named with its cause, not a 30 s click timeout.
        try {
          await until("send is enabled after idle", async () => (await sendState(A.page)).sendDisabled === false, 20_000, 500);
        } catch (e) {
          if (!(e instanceof UntilTimeout)) throw e;
          throw new Error(`S08 resume: send is not enabled ${IDLE_MIN} min after the turn: ${await captureSendFailure(A.page, h)}`);
        }
        let second: Awaited<ReturnType<typeof sendVia>>;
        try {
          second = await sendVia(A.page, threadId, () => A.page.getByTestId("chat-send").click());
        } catch (e) {
          throw new Error(`S08 resume: the click on an enabled send produced no POST /messages within 30 s: ${await captureSendFailure(A.page, h)}; ${(e as Error).message.split("\n")[0]}`);
        }
        expect(second.status, "the send after idle is accepted").toBe(202);
        expect(second.turnId, "the send names its turn").toBeTruthy();
        await waitAssistantDone(A, x, threadId, 2);
        const after = await history(A, x, threadId);
        const turn = after.active_turn ? after.active_turn.state : "done";
        assertIdleResumed(before, after, second.turnId!, body, turn);
        console.log(`[S08 ${h}] thread=${threadId} runner=${seen?.runner_id} freed_after_min=${freedAfterMin} messages=${before.items.length}->${after.items.length}`);
        timeline(`s08-${h}`, [{ thread: threadId, freed_after_min: freedAfterMin, before: before.items.length, after: after.items.length }]);
      });
    });
  });
}
