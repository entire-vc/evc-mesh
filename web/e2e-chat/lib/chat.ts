import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page } from "@playwright/test";
import type { Actor, Ctx } from "./harness";
import { chatBase, chatUrl, sha256, track, until } from "./harness";
import type { Harness } from "./env";
import { cfg, OUT_DIR } from "./env";
import { assertTurnRunning } from "./detect";

export interface ApiMessage {
  id: string;
  role: "user" | "assistant";
  body: string;
  status: string;
  turn_id: string | null;
  created_at: string;
  attachments: Array<{ id: string; mime: string; size: number; width: number | null; height: number | null; file_name: string }>;
}

/** The two live harnesses and the offline fixture agent (S07). */
export type AgentRole = Harness | "offline";

export const agentId = (h: AgentRole) => cfg().agents[h];

export async function agentName(a: Actor, x: Ctx, id: string): Promise<string> {
  const res = await a.api("GET", `${chatBase(x)}/agents`);
  const hit = (res.json() as { items: Array<{ agent_id: string; name: string }> }).items.find((i) => i.agent_id === id);
  if (!hit) throw new Error(`agent ${id} is not offered to ${a.id} by /chat/agents`);
  return hit.name;
}

export async function listThreadIds(a: Actor, x: Ctx, agent: string): Promise<string[]> {
  const out: string[] = [];
  let cursor = "";
  for (let i = 0; i < 50; i++) {
    const res = await a.api("GET", `${chatBase(x)}/threads?agent_id=${agent}&limit=50${cursor}`);
    expect(res.status, "thread list must be readable").toBe(200);
    const j = res.json() as { items: Array<{ id: string }>; has_more: boolean; next_cursor: string | null };
    out.push(...j.items.map((t) => t.id));
    if (!j.has_more || !j.next_cursor) break;
    cursor = `&cursor=${encodeURIComponent(j.next_cursor)}`;
  }
  return out;
}

export async function history(a: Actor, x: Ctx, threadId: string): Promise<{ items: ApiMessage[]; active_turn: { id: string; state: string } | null }> {
  const res = await a.api("GET", `${chatBase(x)}/threads/${threadId}/messages?limit=100`);
  expect(res.status, `history of ${threadId} must be readable by its author`).toBe(200);
  const j = res.json() as { items: ApiMessage[]; active_turn: { id: string; state: string } | null };
  // The API orders by (created_at, id) DESC; assertions read oldest first, in the API's own order, not by timestamp ties.
  return { ...j, items: [...j.items].reverse() };
}

/** Clicks "New chat with <agent>" in the real UI and returns the one new thread. */
export async function newChatViaUi(a: Actor, x: Ctx, h: AgentRole): Promise<string> {
  const id = agentId(h);
  const name = await agentName(a, x, id);
  const before = await listThreadIds(a, x, id);
  await a.page.goto(chatUrl(x));
  await a.page.getByRole("button", { name: `New chat with ${name}` }).first().click();
  await a.page.waitForURL(/\/chat\/[0-9a-f-]{36}$/, { timeout: 30_000 });
  const threadId = a.page.url().split("/").pop()!;
  track(a.id, threadId);
  const after = await listThreadIds(a, x, id);
  expect(after.length, "exactly one new thread").toBe(before.length + 1);
  expect(after).toContain(threadId);
  return threadId;
}

/** PNG with a visual code drawn into it: the code is in the pixels, not in a name or a message. */
export async function makeCodePng(page: Page, code: string): Promise<{ bytes: Buffer; width: number; height: number; sha: string }> {
  const b64 = await page.evaluate(async (c) => {
    const cv = document.createElement("canvas");
    cv.width = 640;
    cv.height = 240;
    const g = cv.getContext("2d")!;
    g.fillStyle = "#ffffff";
    g.fillRect(0, 0, cv.width, cv.height);
    g.fillStyle = "#000000";
    g.font = "bold 120px monospace";
    g.textAlign = "center";
    g.textBaseline = "middle";
    g.fillText(c, cv.width / 2, cv.height / 2);
    const blob: Blob = await new Promise((r) => cv.toBlob((b) => r(b!), "image/png"));
    const buf = new Uint8Array(await blob.arrayBuffer());
    let s = "";
    for (const v of buf) s += String.fromCharCode(v);
    return btoa(s);
  }, code);
  const bytes = Buffer.from(b64, "base64");
  return { bytes, width: 640, height: 240, sha: sha256(bytes) };
}

/**
 * A real paste: the PNG goes into the system clipboard (clipboard-write permission)
 * and the composer receives the browser's own paste event from Ctrl+V. No file
 * chooser, no synthetic ClipboardEvent.
 *
 * The browser sanitizes a clipboard image: it decodes the PNG and encodes the pixels
 * again, so the pasted file has other bytes than the canvas PNG. Returns the PNG the
 * clipboard holds (clipboard-read permission): those are the bytes the composer gets.
 */
export async function pastePng(page: Page, png: Buffer): Promise<{ bytes: Buffer; sha: string }> {
  const b64 = await page.evaluate(async (src) => {
    const bin = Uint8Array.from(atob(src), (ch) => ch.charCodeAt(0));
    await navigator.clipboard.write([new ClipboardItem({ "image/png": new Blob([bin], { type: "image/png" }) })]);
    const [item] = await navigator.clipboard.read();
    const buf = new Uint8Array(await (await item!.getType("image/png")).arrayBuffer());
    let s = "";
    for (const v of buf) s += String.fromCharCode(v);
    return btoa(s);
  }, png.toString("base64"));
  await page.getByTestId("chat-input").focus();
  await page.keyboard.press("ControlOrMeta+V");
  const bytes = Buffer.from(b64, "base64");
  return { bytes, sha: sha256(bytes) };
}

/**
 * A turn that stays running long enough to press Stop. The interactive transcript delivers whole
 * blocks only, so a long single-block reply streams nothing until it is over: the opening sentence
 * arrives as a delta, something after it must keep the turn alive for the click.
 * codex: a shell wait. Its reply comes in one piece at turn end, so the shell call itself is what S06
 * sees running before Stop. Not a bare `sleep`: the agent's shell refuses a standalone foreground sleep,
 * moves it to the background and the turn ends within seconds (pipeline 19465, S06 claude).
 * claude: no shell at all (Bash is denied on the claude harness, see AC3 in composer.spec.ts), so a
 * shell wait ends the turn in ~7 s (job 410449). One read-only tool call closes the opening block,
 * then a long list is written as one block that takes well over a minute to generate.
 */
export function holdTurnPrompt(h: Harness): string {
  return h === "claude"
    ? "First write one short sentence saying you are starting. Then use your file-search tool once to list the files in the current directory. " +
        "Then, without using any more tools, write a numbered list of 400 items, each item one full sentence about a different city of the world."
    : "First write one short sentence saying you are starting. Then run exactly this shell command in the foreground and wait for it to finish: python3 -c 'import time; time.sleep(150)' . Then write a list of 50 items.";
}

/**
 * Reads the nth assistant reply from history and asserts it is still streaming. `byTurn` (codex):
 * no reply exists before the end of the turn, so the thread's active turn is what must still stream.
 */
export async function expectTurnRunning(a: Actor, x: Ctx, threadId: string, nth: number, sentAt: number, byTurn = false): Promise<void> {
  const h = await history(a, x, threadId);
  const m = h.items.filter((i) => i.role === "assistant")[nth - 1];
  assertTurnRunning(m, sentAt, Date.now(), byTurn ? { active: h.active_turn } : undefined);
}

export const codeOf = (s: string) => s.toUpperCase().replace(/[^A-Z0-9]/g, "");

export async function waitAssistantDone(a: Actor, x: Ctx, threadId: string, nth: number, ms = 240_000): Promise<ApiMessage> {
  return until(`assistant reply #${nth} terminal`, async () => {
    const h = await history(a, x, threadId);
    const m = h.items.filter((i) => i.role === "assistant")[nth - 1];
    return m && ["done", "failed", "cancelled"].includes(m.status) ? m : false;
  }, ms, 1000);
}

/**
 * A real drop on the composer: the browser's own dragenter/dragover/drop events
 * carry a DataTransfer that holds the file, as when a file is dragged from the
 * desktop. The bytes arrive unchanged (no clipboard re-encoding), so the stored
 * attachment must hash to exactly these bytes.
 */
export async function dropFile(page: Page, bytes: Buffer, name: string, mime: string): Promise<void> {
  const dt = await page.evaluateHandle(([b64, n, m]) => {
    const bin = Uint8Array.from(atob(b64), (ch) => ch.charCodeAt(0));
    const d = new DataTransfer();
    d.items.add(new File([bin], n, { type: m }));
    return d;
  }, [bytes.toString("base64"), name, mime] as const);
  const target = page.getByTestId("chat-composer");
  for (const type of ["dragenter", "dragover", "drop"]) await target.dispatchEvent(type, { dataTransfer: dt });
  await dt.dispose();
}

/** Runs `act` and returns the one POST of a message to the thread it causes: status and the turn it started. */
export async function sendVia(page: Page, threadId: string, act: () => Promise<void>): Promise<{ status: number; turnId: string | null }> {
  const [res] = await Promise.all([
    page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith(`/threads/${threadId}/messages`), { timeout: 30_000 }),
    act(),
  ]);
  const body = (await res.json().catch(() => ({}))) as { turn?: { id?: string } };
  return { status: res.status(), turnId: body.turn?.id ?? null };
}

/** Counts the message POSTs the page makes to the thread from now on (AC2: a newline must send nothing). */
export function countSends(page: Page, threadId: string): { count: () => number; stop: () => void } {
  let n = 0;
  const on = (r: { method: () => string; url: () => string }) => {
    if (r.method() === "POST" && new URL(r.url()).pathname.endsWith(`/threads/${threadId}/messages`)) n++;
  };
  page.on("request", on);
  return { count: () => n, stop: () => page.off("request", on) };
}

/** The user message settles with the reply: `done` in the history and in the bubble, never left queued. */
export async function assertUserSettled(a: Actor, x: Ctx, threadId: string, body: string): Promise<void> {
  const u = (await history(a, x, threadId)).items.filter((m) => m.role === "user" && m.body === body);
  expect(u, "exactly one stored user message with this body").toHaveLength(1);
  expect(u[0]!.status, "the user message is done once the reply is").toBe("done");
  const bubble = a.page.locator('[data-testid="chat-message"][data-role="user"]').filter({ hasText: body.split("\n")[0]! });
  await expect(bubble).toHaveCount(1);
  await expect(bubble).toHaveAttribute("data-status", "done", { timeout: 30_000 });
}

/**
 * The settled bubble at 1440 and 393 as the live page shows it, without a reload:
 * a reload reads the status from the server and would hide a stale client state.
 *
 * Without a reload the resize also starts the sidebar's 200 ms slide off-screen,
 * and a screenshot taken at once caught it part-way: a strip of the sidebar on the
 * left of the 393 shot. So each shot waits until no CSS transition is running, and
 * at 393 the sidebar must be fully off-screen.
 */
export async function shootSettledBubble(a: Actor, body: string): Promise<void> {
  const { page } = a;
  const dir = join(OUT_DIR, "shots");
  mkdirSync(dir, { recursive: true });
  const was = page.viewportSize() ?? { width: 1440, height: 900 };
  const bubble = page.locator('[data-testid="chat-message"][data-role="user"]').filter({ hasText: body.split("\n")[0]! });
  try {
    for (const vp of [{ width: 1440, height: 900 }, { width: 393, height: 852 }]) {
      await page.setViewportSize(vp);
      await page.waitForFunction(() => document.getAnimations().every((t) => !(t instanceof CSSTransition) || t.playState !== "running"));
      await bubble.scrollIntoViewIfNeeded();
      await expect(bubble, `${vp.width}: the bubble is done`).toHaveAttribute("data-status", "done");
      await expect(bubble, `${vp.width}: no queued label on a settled bubble`).not.toContainText(/queued|в очереди/i);
      if (body.includes("\n")) {
        await expect(bubble.locator("p br"), `${vp.width}: each typed line is its own row`).toHaveCount(body.split("\n").length - 1);
      }
      if (vp.width < 768) {
        const sidebar = page.getByTestId("app-sidebar");
        await expect
          .poll(async () => {
            const box = await sidebar.boundingBox();
            return box === null ? 0 : Math.max(0, Math.round(box.x + box.width));
          }, { message: `${vp.width}: visible width of the sidebar` })
          .toBe(0);
        await page.waitForFunction(() => document.getAnimations().every((t) => !(t instanceof CSSTransition) || t.playState !== "running"));
      }
      await page.screenshot({ path: join(dir, `user-settled-claude-${vp.width}.png`) });
    }
  } finally {
    await page.setViewportSize(was);
  }
}
