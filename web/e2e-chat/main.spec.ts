import { expect, test } from "@playwright/test";
import { RUN_ID, type Harness } from "./lib/env";
import {
  assertDeltasInFinal, assertEarlyDelta, assertFrozen, assertIntermediateThenFinal, assertNoDeltaAfter, assertStreaming, type BodySnapshot,
} from "./lib/detect";
import {
  type Actor, type Ctx, chatBase, chatUrl, openActor, resolveCtx, sha256, snap, step, timeline, until,
} from "./lib/harness";
import {
  holdTurnPrompt, expectTurnRunning, codeOf, history, makeCodePng, newChatViaUi, pastePng, waitAssistantDone, listThreadIds, agentId, sendVia, assertUserSettled,
} from "./lib/chat";

/**
 * e2e/agent-chat S02–S06 and S09 as user A, once per harness (interactive Claude
 * and interactive Codex). The browser does the work; the API and the WebSocket
 * frames only observe. Nothing of ours is mocked: the reply comes from the real
 * runner and agent, and a missing agent is FAIL SETUP, not a skip.
 */
test.describe.configure({ mode: "serial" });

const HARNESSES: Harness[] = ["claude", "codex"];

for (const h of HARNESSES) {
  test.describe(`agent-chat ${h}`, () => {
    let A: Actor;
    let x: Ctx;
    let threadId: string;
    // Unambiguous glyphs only, so the model reads the picture, not guesses it.
    const code = `VC${Array.from({ length: 4 }, () => "ACDEFGHJKMNPQRTUVWXY2346789"[Math.floor(Math.random() * 27)]).join("")}`;
    const text1 = `${RUN_ID} Name the visual code in the picture.`;

    test.beforeAll(async ({ browser }) => {
      A = await openActor(browser, "A");
      x = await resolveCtx(A);
      expect(agentId(h), `agent id for ${h} must be configured`).toBeTruthy();
    });

    test.afterAll(async () => {
      try {
        A?.f1.verify(`A/${h}`);
      } finally {
        await A?.close();
      }
    });

    test(`S02 ${h}: new chat creates exactly one thread`, async () => {
      await step("S02", h, async () => {
        threadId = await newChatViaUi(A, x, h);
        const hist = await history(A, x, threadId);
        expect(hist.items, "a new thread has an empty history").toHaveLength(0);
        await expect(A.page.getByTestId("chat-empty-thread")).toBeVisible();
        await snap(A, `empty-${h}`, undefined, undefined, [A.page.getByTestId("chat-empty-thread")]);
        await A.page.goto(chatUrl(x, threadId));
      });
    });

    test(`S03 ${h}: real paste of a PNG, one message with one attachment`, async () => {
      await step("S03", h, async () => {
        const png = await makeCodePng(A.page, code);
        const pending = A.page.getByTestId("chat-pending-attachment");
        // The browser's own paste event puts the file into the composer, and the image is readable.
        let pasted = { bytes: png.bytes, sha: png.sha };
        const paste = async () => {
          pasted = await pastePng(A.page, png.bytes);
          await expect(pending, "a pasted image appears in the composer").toHaveCount(1, { timeout: 30_000 });
          await expect(pending).toHaveAttribute("data-status", "ready", { timeout: 60_000 });
          const readable = await pending.locator("img").evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth > 0);
          expect(readable, "the pasted image preview decodes").toBe(true);
        };
        // snap reloads the page for every viewport and theme, and a pending attachment lives only in
        // the composer: paste for each shot, then once more on a fresh page for the message itself.
        await snap(A, `paste-preview-${h}`, paste, undefined, [A.page.locator('[data-testid="chat-pending-attachment"][data-status="ready"]')]);
        await A.page.reload();
        await A.page.getByTestId("chat-composer").waitFor({ timeout: 30_000 });
        await paste();

        await A.page.getByTestId("chat-input").fill(text1);
        const sent = await sendVia(A.page, threadId, () => A.page.getByTestId("chat-send").click());
        expect(sent.status, "the send answers 202").toBe(202);

        const msgs = await until("the user message is stored with its attachment", async () => {
          const hist = await history(A, x, threadId);
          const u = hist.items.filter((m) => m.role === "user");
          return u.length > 0 ? u : false;
        }, 60_000);
        expect(msgs, "exactly one user message").toHaveLength(1);
        const m = msgs[0]!;
        expect(m.body).toBe(text1);
        expect(m.attachments, "exactly one attachment").toHaveLength(1);
        const att = m.attachments[0]!;
        expect(att.mime).toBe("image/png");
        expect(att.size, "the stored size is the pasted file's").toBe(pasted.bytes.length);
        expect([att.width, att.height]).toEqual([png.width, png.height]);
        expect(att.file_name, "the code is in the pixels, not in the file name").not.toContain(code);

        // Download under A: the 302's first hop needs the bearer token, the storage hop must not get it.
        const hop = await A.api("GET", `${chatBase(x)}/attachments/${att.id}/download`, undefined, { maxRedirects: 0 });
        expect([200, 302]).toContain(hop.status);
        let bytes: Buffer;
        if (hop.status === 302) {
          const got = await A.context.request.get(hop.headers["location"]!);
          expect(got.status()).toBe(200);
          bytes = await got.body();
        } else {
          bytes = await hop.bytes();
        }
        expect(sha256(bytes), "the downloaded bytes are the pasted PNG").toBe(pasted.sha);

        await A.page.reload();
        await expect(A.page.getByTestId("chat-message").filter({ hasText: text1 })).toHaveCount(1);
        await expect(A.page.getByTestId("chat-attachment-image")).toHaveCount(1);
      });
    });

    // A reply of one block arrives as one delta on either harness: the interactive transcript holds
    // whole blocks only and Codex writes its final message in one piece. S04 asks for text before
    // completed; the multi-block stream is S04b.
    test(`S04 ${h}: streaming: text arrives as delta(s) before completed, final carries the code`, async () => {
      await step("S04", h, async () => {
        // Deltas are collected from the WS frames of this page, from the first message on.
        const first = await until("first assistant body appears (streaming or done)", async () => {
          const hist = await history(A, x, threadId);
          const a = hist.items.find((i) => i.role === "assistant");
          return a && a.body !== "" ? a : false;
        }, 240_000, 300);
        const intermediate = first.status === "streaming" ? first.body : "";
        const done = await waitAssistantDone(A, x, threadId, 1);
        expect(done.status, "turn ends done").toBe("done");
        await until("the completed frame of the reply", async () => A.frames.some((f) => f.event === "chat.message.completed" && f.message_id === done.id), 30_000, 200);
        const frames = A.frames.filter((f) => f.thread_id === threadId);
        timeline(`S04-${h}`, frames.map(({ raw: _raw, ...rest }) => rest));
        assertDeltasInFinal(assertStreaming(frames, done.id), done.body);
        // One block arrives as one delta, so the in-flight body may already be the whole reply.
        if (intermediate) assertIntermediateThenFinal(intermediate, done.body, false);
        expect(codeOf(done.body), "the reply names the visual code of the picture").toContain(codeOf(code));
        await expect(A.page.getByTestId("chat-message").filter({ hasText: code }).last()).toBeVisible();
        await assertUserSettled(A, x, threadId, text1);
      });
    });

    test(`S05 ${h}: rename from the menu and a second turn on the same chat`, async () => {
      await step("S05", h, async () => {
        const before = (await listThreadIds(A, x, agentId(h))).length;
        const title = `${RUN_ID}-renamed-${h}`;
        await A.page.getByRole("button", { name: /^Actions for chat/ }).first().click();
        await A.page.getByRole("menuitem", { name: "Rename" }).click();
        await A.page.getByTestId("chat-rename-input").fill(title);
        await A.page.getByTestId("chat-rename-submit").click();
        await expect(A.page.getByTestId("chat-thread-item").filter({ hasText: title })).toBeVisible();
        await A.page.reload();
        await expect(A.page.getByTestId("chat-thread-item").filter({ hasText: title })).toBeVisible();
        expect((await listThreadIds(A, x, agentId(h))).length).toBe(before);

        await A.page.getByTestId("chat-input").fill(`${RUN_ID} Repeat the code from the previous message.`);
        await A.page.getByTestId("chat-send").click();
        const second = await waitAssistantDone(A, x, threadId, 2);
        expect(second.status).toBe("done");
        expect(codeOf(second.body), "the second turn continues the same conversation").toContain(codeOf(code));
        const hist = await history(A, x, threadId);
        expect(hist.items.map((m) => m.role)).toEqual(["user", "assistant", "user", "assistant"]);
      });
    });

    test(`S06 ${h}: Stop on a long running turn ends cancelled and frozen`, async () => {
      // codex delivers its reply in one piece at turn end (S04b), so no delta marks the turn as running
      // (jobs 414033, 414785). Its shell call does: the rollout writes the call before the command runs,
      // and the bridge relays it at once as chat.turn.tool. Stop then lands before any reply text.
      const byTurn = h === "codex";
      await step("S06", h, async () => {
        await A.page.getByTestId("chat-input").fill(`${RUN_ID} ${holdTurnPrompt(h)}`);
        // Stop is pressed only after the server has shown the turn at work: a real delta (claude) or
        // tool call (codex), not a guess. The offset is taken before the send: the frame can land
        // while the click is still resolving.
        const before = A.frames.length;
        const sentAt = Date.now();
        await A.page.getByTestId("chat-send").click();
        if (byTurn) {
          await until("a tool call of the long turn", async () => A.frames.slice(before).find((f) => f.event === "chat.turn.tool" && f.thread_id === threadId), 240_000, 200);
        } else {
          await until("a delta of the long reply", async () => A.frames.slice(before).find((f) => f.event === "chat.message.delta" && (f.textLen ?? 0) > 0), 240_000, 200);
        }
        // Stop exists only while the turn runs: each reloaded frame must still be mid-turn. A turn that is
        // already over fails here by name, not as a missing button after the reload.
        await expectTurnRunning(A, x, threadId, 3, sentAt, byTurn);
        await snap(A, `streaming-${h}`, undefined, undefined, [A.page.getByTestId("chat-stop")]);
        await expectTurnRunning(A, x, threadId, 3, sentAt, byTurn);
        await expect(A.page.getByTestId("chat-stop"), "the turn is still running when Stop is pressed").toBeVisible({ timeout: 15_000 });
        await A.page.getByTestId("chat-stop").click();

        const msg = await waitAssistantDone(A, x, threadId, 3, 120_000);
        expect(msg.status, "cancelled, not done").toBe("cancelled");
        // codex: the reply row is created by the cancel itself and may be empty; claude keeps what streamed.
        if (!byTurn) expect(msg.body.length, "the partial body is kept").toBeGreaterThan(0);
        await expect(A.page.getByText("stopped", { exact: true }).last()).toBeVisible();
        await snap(A, `cancelled-${h}`, undefined, undefined, [A.page.getByText("stopped", { exact: true })]);

        const terminalSeq = Math.max(...A.frames.filter((f) => f.thread_id === threadId && f.event === "chat.turn.state").map((f) => f.seq ?? 0));
        // Drain window: bounded by the protocol's cancel/drain deadline, observed on the stored body, not slept blind.
        const snaps: BodySnapshot[] = [];
        for (let i = 0; i < 4; i++) {
          const m = (await history(A, x, threadId)).items.filter((v) => v.role === "assistant")[2]!;
          snaps.push({ body: m.body, status: m.status });
          await new Promise((r) => setTimeout(r, 2500));
        }
        assertFrozen(snaps, "cancelled", !byTurn);
        assertNoDeltaAfter(A.frames, msg.id, terminalSeq);

        // The session stays usable: the next short message finishes.
        await A.page.getByTestId("chat-input").fill(`${RUN_ID} Reply with the single word ok.`);
        await A.page.getByTestId("chat-send").click();
        const next = await waitAssistantDone(A, x, threadId, 4);
        expect(next.status).toBe("done");
      });
    });

    test(`S04b ${h}: a reply of several blocks streams them one by one`, async () => {
      test.skip(h === "codex", "codex rollout delivers the final message in one piece at turn end");
      await step("S04b", h, async () => {
        const multi = await newChatViaUi(A, x, h);
        await A.page.goto(chatUrl(x, multi));
        const code2 = `VC${Array.from({ length: 4 }, () => "ACDEFGHJKMNPQRTUVWXY2346789"[Math.floor(Math.random() * 27)]).join("")}`;
        const png = await makeCodePng(A.page, code2);
        await pastePng(A.page, png.bytes);
        await expect(A.page.getByTestId("chat-pending-attachment")).toHaveAttribute("data-status", "ready", { timeout: 60_000 });
        // A foreground pause between the blocks: without it the gap is one tool round-trip
        // (0.3–1.9 s live) and the model may name the code, which it already sees, before the
        // tool and send one block. The printed word can only be written after the pause.
        await A.page.getByTestId("chat-input").fill(
          `${RUN_ID} First write one short sentence saying you will now look at the picture, and nothing else yet. ` +
          "Then run exactly this shell command in the foreground and wait for it to finish: " +
          "python3 -c 'import secrets, time; time.sleep(4); print(secrets.token_hex(3))' . " +
          "Only after it finishes, write a second message that names the visual code in the attached picture and repeats the word the command printed.");
        await A.page.getByTestId("chat-send").click();

        // The stored body while the reply streams, read as often as the API allows.
        let intermediate = "";
        const done = await until("the multi-block reply is terminal", async () => {
          const a = (await history(A, x, multi)).items.find((i) => i.role === "assistant");
          if (a?.status === "streaming" && a.body !== "" && !intermediate) intermediate = a.body;
          return a && ["done", "failed", "cancelled"].includes(a.status) ? a : false;
        }, 240_000, 150);
        expect(done.status, "turn ends done").toBe("done");
        await until("the completed frame of the reply", async () => A.frames.some((f) => f.event === "chat.message.completed" && f.message_id === done.id), 30_000, 200);
        const frames = A.frames.filter((f) => f.thread_id === multi);
        timeline(`S04b-${h}`, frames.map(({ raw: _raw, ...rest }) => rest));
        assertDeltasInFinal(assertStreaming(frames, done.id, 2), done.body);
        assertEarlyDelta(frames, done.id, 1000);
        assertIntermediateThenFinal(intermediate, done.body);
        expect(codeOf(done.body), "the reply names the visual code of the picture").toContain(codeOf(code2));
        await A.page.goto(chatUrl(x, threadId));
      });
    });

    test(`S09 ${h}: delete asks first; cancel changes nothing, confirm removes the chat`, async () => {
      await step("S09", h, async () => {
        const control = await newChatViaUi(A, x, h);
        await A.page.goto(chatUrl(x, threadId));
        const ids = await listThreadIds(A, x, agentId(h));
        const histBefore = JSON.stringify((await history(A, x, threadId)).items.map((m) => [m.id, m.body, m.status]));

        const del = async () => {
          // Below md an open thread hides the chat list: a phone user goes back to the list first.
          // Bounded clicks: a missing control fails here by name, not as the test timeout (job 411311).
          // isVisible() does not wait: right after goto the list is not drawn yet, so wait for either control first.
          // Only visible matches count: on a phone the item is in the DOM but hidden, at some widths both show.
          const item = A.page.locator(`[data-testid="chat-thread-item"][data-thread-id="${threadId}"]`);
          const back = A.page.getByRole("button", { name: "Back to chats" });
          await expect(item.or(back).filter({ visible: true }).first()).toBeVisible({ timeout: 15_000 });
          if (!(await item.isVisible())) await back.click({ timeout: 15_000 });
          await item.getByTestId("chat-thread-menu").click({ timeout: 15_000 });
          await A.page.getByRole("menuitem", { name: "Delete" }).click({ timeout: 15_000 });
        };
        await del();
        await expect(A.page.getByRole("dialog")).toContainText("Delete this chat?");
        // Frames of the open confirmation: the dialog is re-opened after each reload of the frame set.
        await snap(A, `delete-confirm-${h}`, async () => {
          await del();
          await expect(A.page.getByRole("dialog")).toBeVisible();
        }, async () => { await A.page.keyboard.press("Escape"); }, [A.page.getByRole("dialog")]);
        await del();
        await A.page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
        expect(await listThreadIds(A, x, agentId(h))).toEqual(ids);
        expect(JSON.stringify((await history(A, x, threadId)).items.map((m) => [m.id, m.body, m.status]))).toBe(histBefore);

        await A.page.goto(chatUrl(x, threadId));
        await del();
        await A.page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
        await until("the thread is gone from the list", async () => !(await listThreadIds(A, x, agentId(h))).includes(threadId), 30_000);
        const gone = await A.api("GET", `${chatBase(x)}/threads/${threadId}`);
        expect(gone.status, "the deleted thread is not served any more").toBe(404);
        expect(await listThreadIds(A, x, agentId(h))).toContain(control);
      });
    });
  });
}
