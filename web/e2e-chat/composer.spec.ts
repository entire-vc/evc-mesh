import { createHash } from "node:crypto";
import { expect, test } from "@playwright/test";
import { RUN_ID, SetupFail, type Harness } from "./lib/env";
import { assertMarkdownDom, assertReadable, assertToolEvents } from "./lib/detect";
import { type Actor, type Ctx, chatBase, chatUrl, openActor, resolveCtx, sha256, snap, step, timeline } from "./lib/harness";
import {
  agentId, assertUserSettled, countSends, dropFile, history, makeCodePng, newChatViaUi, pastePng, sendVia, shootSettledBubble, waitAssistantDone,
} from "./lib/chat";

/**
 * The composer and the states around a send, as user A (Mesh Doc e2e/agent-chat,
 * an internal ticket): paste and drag-drop (AC1), Enter / Shift+Enter (AC2), Markdown and tool
 * events in the reply (AC3, both harnesses), the offline agent's queue and a
 * failed send with its recovery (AC5). Nothing of ours is mocked; the one routed
 * request in AC5 is a network failure, the external boundary.
 *
 * Every test opens its own chat and is not serial: one live defect fails its AC
 * and leaves the other ACs to report their own result instead of being skipped.
 */

const HARNESSES: Harness[] = ["claude", "codex"];

let A: Actor;
let x: Ctx;

test.beforeAll(async ({ browser }) => {
  A = await openActor(browser, "A");
  x = await resolveCtx(A);
  const offered = ((await A.api("GET", `${chatBase(x)}/agents`)).json() as { items: Array<{ agent_id: string; presence: string }> }).items;
  for (const role of ["claude", "codex", "offline"] as const) {
    if (!agentId(role)) throw new SetupFail(`agent id for ${role} is not configured`);
    if (!offered.some((i) => i.agent_id === agentId(role))) throw new SetupFail(`agent fixture "${role}" is not offered to A`);
  }
  if (offered.find((i) => i.agent_id === agentId("offline"))!.presence !== "offline") throw new SetupFail("the offline agent fixture is online: AC5 would not exercise the queue");
});

test.afterAll(async () => {
  try {
    A?.f1.verify("A/composer");
  } finally {
    await A?.close();
  }
});

test.describe("composer claude", () => {
  test("AC2: Shift+Enter adds a newline and sends nothing; Enter sends exactly one message", async () => {
    await step("AC2", "claude", async () => {
      const threadId = await newChatViaUi(A, x, "claude");
      const input = A.page.getByTestId("chat-input");
      const sends = countSends(A.page, threadId);
      try {
        await input.click();
        await A.page.keyboard.type(`${RUN_ID} line one`);
        await A.page.keyboard.press("Shift+Enter");
        await A.page.keyboard.type("Reply with the single word ok.");
        const body = `${RUN_ID} line one\nReply with the single word ok.`;
        await expect(input, "Shift+Enter puts a newline into the draft").toHaveValue(body);
        expect(sends.count(), "Shift+Enter sends nothing").toBe(0);
        expect((await history(A, x, threadId)).items, "nothing is stored after Shift+Enter").toHaveLength(0);

        const sent = await sendVia(A.page, threadId, () => A.page.keyboard.press("Enter"));
        expect(sent.status, "Enter sends with 202").toBe(202);
        await expect(input, "the composer is cleared after the send").toHaveValue("");
        const u = (await history(A, x, threadId)).items.filter((m) => m.role === "user");
        expect(u, "Enter creates exactly one user message").toHaveLength(1);
        expect(u[0]!.body, "the stored body keeps the newline").toBe(body);
        await waitAssistantDone(A, x, threadId, 1);
        expect(sends.count(), "exactly one POST for one Enter").toBe(1);
        await assertUserSettled(A, x, threadId, body);
        await shootSettledBubble(A, body);
      } finally {
        sends.stop();
      }
    });
  });

  test("AC1: a dropped PNG is sent with 202, shown in history and DOM, and downloads with the same hash", async () => {
    await step("AC1-drop", "claude", async () => {
      const threadId = await newChatViaUi(A, x, "claude");
      const png = await makeCodePng(A.page, `DROP${Math.floor(Math.random() * 1e4)}`);
      const name = `${RUN_ID}-drop.png`;
      await dropFile(A.page, png.bytes, name, "image/png");
      const pending = A.page.getByTestId("chat-pending-attachment");
      await expect(pending, "a dropped file appears in the composer").toHaveCount(1, { timeout: 30_000 });
      await expect(pending).toHaveAttribute("data-status", "ready", { timeout: 60_000 });

      const text = `${RUN_ID} dropped file, reply with the single word ok.`;
      await A.page.getByTestId("chat-input").fill(text);
      const sent = await sendVia(A.page, threadId, () => A.page.getByTestId("chat-send").click());
      expect(sent.status, "the send answers 202").toBe(202);
      await expect(pending, "the sent file leaves the composer").toHaveCount(0);

      const m = (await history(A, x, threadId)).items.filter((v) => v.role === "user" && v.body === text);
      expect(m, "exactly one user message").toHaveLength(1);
      expect(m[0]!.attachments, "exactly one attachment").toHaveLength(1);
      const att = m[0]!.attachments[0]!;
      expect(att.mime).toBe("image/png");
      expect(att.size, "the stored size is the dropped file's").toBe(png.bytes.length);
      expect(att.file_name).toBe(name);

      const hop = await A.api("GET", `${chatBase(x)}/attachments/${att.id}/download`, undefined, { maxRedirects: 0 });
      expect([200, 302]).toContain(hop.status);
      const bytes = hop.status === 302 ? await (await A.context.request.get(hop.headers["location"]!)).body() : await hop.bytes();
      expect(sha256(bytes), "the downloaded bytes are the dropped PNG").toBe(png.sha);

      await waitAssistantDone(A, x, threadId, 1);
      await A.page.reload();
      const bubble = A.page.getByTestId("chat-message").filter({ hasText: text });
      await expect(bubble).toHaveCount(1);
      await expect(bubble.getByTestId("chat-attachment-image"), "the attachment is in the DOM after reload").toHaveCount(1);
    });
  });

  // S03 in main.spec.ts pastes too, but inside the serial S02–S09 chain that S04 reads its code from;
  // this copy keeps AC1 paste reporting its own result when an earlier scenario step fails.
  test("AC1: a pasted PNG is sent with 202, shown in history and DOM, and downloads with the same hash", async () => {
    await step("AC1-paste", "claude", async () => {
      const threadId = await newChatViaUi(A, x, "claude");
      const png = await makeCodePng(A.page, `PASTE${Math.floor(Math.random() * 1e4)}`);
      const pasted = await pastePng(A.page, png.bytes);
      const pending = A.page.getByTestId("chat-pending-attachment");
      await expect(pending, "a pasted image appears in the composer").toHaveCount(1, { timeout: 30_000 });
      await expect(pending).toHaveAttribute("data-status", "ready", { timeout: 60_000 });

      const text = `${RUN_ID} pasted file, reply with the single word ok.`;
      await A.page.getByTestId("chat-input").fill(text);
      const sent = await sendVia(A.page, threadId, () => A.page.getByTestId("chat-send").click());
      expect(sent.status, "the send answers 202").toBe(202);
      await expect(pending, "the sent file leaves the composer").toHaveCount(0);

      const m = (await history(A, x, threadId)).items.filter((v) => v.role === "user" && v.body === text);
      expect(m, "exactly one user message").toHaveLength(1);
      expect(m[0]!.attachments, "exactly one attachment").toHaveLength(1);
      const att = m[0]!.attachments[0]!;
      expect(att.mime).toBe("image/png");
      expect(att.size, "the stored size is the pasted file's").toBe(pasted.bytes.length);

      const hop = await A.api("GET", `${chatBase(x)}/attachments/${att.id}/download`, undefined, { maxRedirects: 0 });
      expect([200, 302]).toContain(hop.status);
      const bytes = hop.status === 302 ? await (await A.context.request.get(hop.headers["location"]!)).body() : await hop.bytes();
      expect(sha256(bytes), "the downloaded bytes are the pasted PNG").toBe(pasted.sha);

      await waitAssistantDone(A, x, threadId, 1);
      await A.page.reload();
      const bubble = A.page.getByTestId("chat-message").filter({ hasText: text });
      await expect(bubble).toHaveCount(1);
      await expect(bubble.getByTestId("chat-attachment-image"), "the attachment is in the DOM after reload").toHaveCount(1);
    });
  });

  test("AC5: offline disables sending and keeps the draft; a failed send shows an error, keeps the draft and recovers once", async () => {
    await step("AC5-error-recovery", "claude", async () => {
      const threadId = await newChatViaUi(A, x, "claude");
      const draft = `${RUN_ID} after recovery, reply with the single word ok.`;
      const input = A.page.getByTestId("chat-input");
      await input.fill(draft);

      // The browser goes offline: the banner says so, sending is unavailable, the draft stays.
      A.f1.setNetworkDown(true);
      const banner = A.page.getByRole("status").filter({ hasText: "No network connection" });
      try {
        await A.context.setOffline(true);
        await expect(banner, "the offline banner is shown").toBeVisible({ timeout: 15_000 });
        await expect(A.page.getByTestId("chat-send"), "send is unavailable offline").toBeDisabled();
        await expect(input, "the draft survives going offline").toHaveValue(draft);
        await A.context.setOffline(false);
        await expect(banner, "the banner goes away online").toHaveCount(0, { timeout: 15_000 });
        await expect(input, "the draft survives coming back").toHaveValue(draft);
        // snap reloads per frame, which needs the network: offline only between the reload and the shot.
        await snap(A, "offline-network", async () => {
          await A.context.setOffline(true);
          await expect(banner).toBeVisible({ timeout: 15_000 });
        }, async () => { await A.context.setOffline(false); }, [banner]);
      } finally {
        await A.context.setOffline(false);
      }
      await A.page.getByTestId("chat-composer").waitFor({ timeout: 30_000 });
      await input.fill(draft); // snap reloaded the page

      // The network drops the one send: the error is visible, the draft and nothing else stays.
      const route = `**/threads/${threadId}/messages`;
      await A.page.route(route, (r) => (r.request().method() === "POST" ? r.abort("internetdisconnected") : r.fallback()));
      A.f1.setNetworkDown(true);
      try {
        await A.page.getByTestId("chat-send").click();
        await expect(A.page.locator('[data-sonner-toast][data-type="error"]'), "the failed send is reported").toBeVisible({ timeout: 15_000 });
        await expect(input, "the draft is kept after a failed send").toHaveValue(draft);
        await expect(A.page.getByTestId("chat-message").filter({ hasText: draft }), "no phantom message stays in the feed").toHaveCount(0);
      } finally {
        await A.page.unroute(route);
        A.f1.setNetworkDown(false);
      }
      expect((await history(A, x, threadId)).items.filter((m) => m.body === draft), "the failed send stored nothing").toHaveLength(0);

      // Recovery: the same draft goes once, the reply completes, the user message settles.
      const sends = countSends(A.page, threadId);
      try {
        const sent = await sendVia(A.page, threadId, () => A.page.getByTestId("chat-send").click());
        expect(sent.status, "the retried send answers 202").toBe(202);
        await expect(input).toHaveValue("");
        await waitAssistantDone(A, x, threadId, 1);
        expect(sends.count(), "recovery makes exactly one POST").toBe(1);
      } finally {
        sends.stop();
      }
      await assertUserSettled(A, x, threadId, draft);
    });
  });
});

test("AC5: a message to the offline agent is stored and queued, shown as offline, and stays queued after reload", async () => {
  await step("AC5-queued-offline", "offline", async () => {
    const threadId = await newChatViaUi(A, x, "offline");
    const text = `${RUN_ID}-offline`;
    await A.page.getByTestId("chat-input").fill(text);
    const sent = await sendVia(A.page, threadId, () => A.page.getByTestId("chat-send").click());
    expect(sent.status, "the send is accepted (202) while the agent is offline").toBe(202);

    const check = async () => {
      const h = await history(A, x, threadId);
      const u = h.items.filter((m) => m.role === "user");
      expect(u, "exactly one user message").toHaveLength(1);
      expect(u[0]!.body).toBe(text);
      expect(u[0]!.status, "the user message waits in the queue").toBe("queued");
      expect(h.items.filter((m) => m.role === "assistant"), "no reply before a claim").toHaveLength(0);
      expect(h.active_turn?.state, "one turn, queued").toBe("queued");
      if (sent.turnId) expect(h.active_turn?.id).toBe(sent.turnId);
      await expect(A.page.getByTestId("chat-agent-offline"), "the UI says the agent is offline").toBeVisible({ timeout: 30_000 });
      const bubble = A.page.locator('[data-testid="chat-message"][data-role="user"]').filter({ hasText: text });
      await expect(bubble).toHaveAttribute("data-status", "queued");
      await expect(bubble.getByText("queued", { exact: true }), "the bubble is marked queued").toBeVisible();
    };
    await check();
    await A.page.reload();
    await A.page.getByTestId("chat-composer").waitFor({ timeout: 30_000 });
    await check();
    await snap(A, "queued-offline", undefined, undefined, [
      A.page.getByTestId("chat-agent-offline"),
      A.page.locator('[data-testid="chat-message"][data-role="user"][data-status="queued"]').filter({ hasText: text }),
    ]);
    await A.page.goto(chatUrl(x, threadId));
  });
});

for (const h of HARNESSES) {
  test(`AC3 ${h}: the reply renders Markdown heading, list and code, and the turn reports its tool use`, async () => {
    await step("AC3", h, async () => {
      const threadId = await newChatViaUi(A, x, h);
      // The answer cannot be guessed: the right 16 hex characters in the code block prove the tool really ran.
      // Letters, digits and dashes only: the token goes into a shell command the agent runs.
      const token = `${RUN_ID.replace(/[^A-Za-z0-9-]/g, "")}-${Math.random().toString(36).slice(2, 10)}`;
      const want = createHash("sha256").update(token).digest("hex").slice(0, 16);
      // A chat on the claude harness runs with Bash denied (the runner's read-only boundary, no sandbox
      // in headless mode); codex keeps a shell in its read-only sandbox. So the claude case reads the value
      // from an attached file: the message carries only the file's path, so the value proves the read.
      if (h === "claude") {
        const name = `${RUN_ID}-ac3.txt`;
        await dropFile(A.page, Buffer.from(`${want}\n`), name, "text/plain");
        const pending = A.page.getByTestId("chat-pending-attachment");
        await expect(pending, "the dropped file appears in the composer").toHaveCount(1, { timeout: 30_000 });
        await expect(pending).toHaveAttribute("data-status", "ready", { timeout: 60_000 });
      }
      // The activity strip lives only while the turn runs: record every label it ever shows.
      await A.page.evaluate(() => {
        const w = window as unknown as { __toolLabels: string[] };
        w.__toolLabels = [];
        new MutationObserver(() => {
          document.querySelectorAll('[data-testid="chat-tools"]').forEach((el) => w.__toolLabels.push(el.textContent ?? ""));
        }).observe(document.body, { subtree: true, childList: true, characterData: true });
      });
      await A.page.getByTestId("chat-input").fill(
        h === "claude"
          ? "Use your file-reading tool to read the attached text file. " +
            "Then reply in Markdown with: a level-2 heading 'Result', a bulleted list of exactly three short items about the file, " +
            "and a fenced code block that contains only the 16 hex characters written in the file."
          : `Use your shell tool to run exactly: printf '%s' '${token}' | sha256sum (or shasum -a 256). ` +
            "Then reply in Markdown with: a level-2 heading 'Result', a bulleted list of exactly three short items about the command, " +
            "and a fenced code block that contains only the first 16 hex characters of the hash.",
      );
      const sent = await sendVia(A.page, threadId, () => A.page.getByTestId("chat-send").click());
      expect(sent.status).toBe(202);
      expect(sent.turnId, "the send names its turn").toBeTruthy();
      const done = await waitAssistantDone(A, x, threadId, 1);
      expect(done.status).toBe("done");
      timeline(`AC3-${h}`, A.frames.filter((f) => f.thread_id === threadId).map(({ raw: _raw, ...rest }) => rest));

      const bubble = A.page.locator('[data-testid="chat-message"][data-role="assistant"]').last();
      await expect(bubble.locator("pre code").first(), "the reply has a rendered code block").toBeVisible({ timeout: 30_000 });
      const dom = await bubble.evaluate((el) => ({
        headings: [...el.querySelectorAll("h1,h2,h3,h4")].map((e) => e.textContent ?? ""),
        listItems: el.querySelectorAll("ul > li, ol > li").length,
        codeBlocks: [...el.querySelectorAll("pre code")].map((e) => e.textContent ?? ""),
      }));
      assertMarkdownDom(dom, want);
      const labels = await A.page.evaluate(() => (window as unknown as { __toolLabels?: string[] }).__toolLabels ?? []);
      assertToolEvents(A.frames, sent.turnId!, labels);
      const reply = A.page.locator('[data-testid="chat-message"][data-role="assistant"] pre code');
      if (h !== "claude") {
        await snap(A, `markdown-${h}`, undefined, undefined, [reply]);
        return;
      }
      // E3 AC1: the attached file's name on the user's own bubble reads as text in both themes and at both widths.
      const file = A.page.locator('[data-testid="chat-message"][data-role="user"] [data-testid="chat-attachment-file"]').last();
      await snap(A, `markdown-${h}`, undefined, undefined, [reply, file], async (where) => {
        // The size is its own span: the button's text runs name and size together ("ac3.txt17 B").
        await expect(file.locator("span").filter({ hasText: /^\d+ B$/ }), `${where}: the size is shown`).toHaveCount(1);
        const painted = await file.evaluate((btn, name) => {
          const label = [...btn.querySelectorAll("span")].find((s) => s.textContent === name);
          if (!label) return null;
          let bg = "rgba(0, 0, 0, 0)";
          for (let n: Element | null = label; n; n = n.parentElement) {
            const c = getComputedStyle(n).backgroundColor;
            if (!/rgba\([^)]*,\s*0\)|transparent/.test(c)) {
              bg = c;
              break;
            }
          }
          return { fg: getComputedStyle(label).color, bg, icons: btn.querySelectorAll("svg").length };
        }, `${RUN_ID}-ac3.txt`);
        expect(painted, `${where}: the file name ${RUN_ID}-ac3.txt is on the attachment card`).not.toBeNull();
        expect(painted!.icons, `${where}: file and download icons`).toBe(2);
        assertReadable(`${where} attachment name`, painted!);
      });
    });
  });
}
