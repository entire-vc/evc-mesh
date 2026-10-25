import { expect, test } from "@playwright/test";
import { RUN_ID, type Harness } from "./lib/env";
import {
  assertFrozen, assertIntermediateThenFinal, assertNoDeltaAfter, assertStreaming, type BodySnapshot,
} from "./lib/detect";
import {
  type Actor, type Ctx, chatBase, chatUrl, openActor, resolveCtx, sha256, snap, step, timeline, until,
} from "./lib/harness";
import {
  codeOf, history, makeCodePng, newChatViaUi, pastePng, waitAssistantDone, listThreadIds, agentId,
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
        await snap(A.page, `empty-${h}`);
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
        await snap(A.page, `paste-preview-${h}`, paste);
        await A.page.reload();
        await A.page.getByTestId("chat-composer").waitFor({ timeout: 30_000 });
        await paste();

        await A.page.getByTestId("chat-input").fill(text1);
        await A.page.getByTestId("chat-send").click();

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

    test(`S04 ${h}: streaming with at least two deltas, final carries the code`, async () => {
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
        const deltas = A.frames.filter((f) => f.event === "chat.message.delta" && f.message_id === done.id);
        timeline(`S04-${h}`, A.frames.filter((f) => f.thread_id === threadId).map(({ raw: _raw, ...rest }) => rest));
        assertStreaming(deltas);
        if (intermediate) assertIntermediateThenFinal(intermediate, done.body);
        expect(codeOf(done.body), "the reply names the visual code of the picture").toContain(codeOf(code));
        await expect(A.page.getByTestId("chat-message").filter({ hasText: code }).last()).toBeVisible();
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

    test(`S06 ${h}: Stop on a long streaming reply ends cancelled and frozen`, async () => {
      await step("S06", h, async () => {
        await A.page.getByTestId("chat-input").fill(`${RUN_ID} Count from 1 to 1000, one number per line, with a short remark on each.`);
        await A.page.getByTestId("chat-send").click();
        // Stop is pressed only after the server has streamed something: a real delta, not a guess.
        const before = A.frames.length;
        await until("a delta of the long reply", async () => A.frames.slice(before).find((f) => f.event === "chat.message.delta" && (f.textLen ?? 0) > 0), 240_000, 200);
        await snap(A.page, `streaming-${h}`);
        await expect(A.page.getByTestId("chat-stop")).toBeVisible();
        await A.page.getByTestId("chat-stop").click();

        const msg = await waitAssistantDone(A, x, threadId, 3, 120_000);
        expect(msg.status, "cancelled, not done").toBe("cancelled");
        expect(msg.body.length, "the partial body is kept").toBeGreaterThan(0);
        await expect(A.page.getByText("stopped", { exact: true }).last()).toBeVisible();
        await snap(A.page, `cancelled-${h}`);

        const terminalSeq = Math.max(...A.frames.filter((f) => f.thread_id === threadId && f.event === "chat.turn.state").map((f) => f.seq ?? 0));
        // Drain window: bounded by the protocol's cancel/drain deadline, observed on the stored body, not slept blind.
        const snaps: BodySnapshot[] = [];
        for (let i = 0; i < 4; i++) {
          const m = (await history(A, x, threadId)).items.filter((v) => v.role === "assistant")[2]!;
          snaps.push({ body: m.body, status: m.status });
          await new Promise((r) => setTimeout(r, 2500));
        }
        assertFrozen(snaps, "cancelled");
        assertNoDeltaAfter(A.frames, msg.id, terminalSeq);

        // The session stays usable: the next short message finishes.
        await A.page.getByTestId("chat-input").fill(`${RUN_ID} Reply with the single word ok.`);
        await A.page.getByTestId("chat-send").click();
        const next = await waitAssistantDone(A, x, threadId, 4);
        expect(next.status).toBe("done");
      });
    });

    test(`S09 ${h}: delete asks first; cancel changes nothing, confirm removes the chat`, async () => {
      await step("S09", h, async () => {
        const control = await newChatViaUi(A, x, h);
        await A.page.goto(chatUrl(x, threadId));
        const ids = await listThreadIds(A, x, agentId(h));
        const histBefore = JSON.stringify((await history(A, x, threadId)).items.map((m) => [m.id, m.body, m.status]));

        const del = async () => {
          await A.page.locator(`[data-testid="chat-thread-item"][data-thread-id="${threadId}"]`).getByTestId("chat-thread-menu").click();
          await A.page.getByRole("menuitem", { name: "Delete" }).click();
        };
        await del();
        await expect(A.page.getByRole("dialog")).toContainText("Delete this chat?");
        // Frames of the open confirmation: the dialog is re-opened after each reload of the frame set.
        await snap(A.page, `delete-confirm-${h}`, async () => {
          await del();
          await expect(A.page.getByRole("dialog")).toBeVisible();
        }, async () => { await A.page.keyboard.press("Escape"); });
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
