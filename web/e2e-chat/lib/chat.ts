import { expect, type Page } from "@playwright/test";
import type { Actor, Ctx } from "./harness";
import { chatBase, chatUrl, sha256, track, until } from "./harness";
import type { Harness } from "./env";
import { cfg } from "./env";

export interface ApiMessage {
  id: string;
  role: "user" | "assistant";
  body: string;
  status: string;
  turn_id: string | null;
  created_at: string;
  attachments: Array<{ id: string; mime: string; size: number; width: number | null; height: number | null; file_name: string }>;
}

export const agentId = (h: Harness) => cfg().agents[h];

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
export async function newChatViaUi(a: Actor, x: Ctx, h: Harness): Promise<string> {
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
 */
export async function pastePng(page: Page, png: Buffer): Promise<void> {
  await page.evaluate(async (b64) => {
    const bin = Uint8Array.from(atob(b64), (ch) => ch.charCodeAt(0));
    await navigator.clipboard.write([new ClipboardItem({ "image/png": new Blob([bin], { type: "image/png" }) })]);
  }, png.toString("base64"));
  await page.getByTestId("chat-input").focus();
  await page.keyboard.press("ControlOrMeta+V");
}

export const codeOf = (s: string) => s.toUpperCase().replace(/[^A-Z0-9]/g, "");

export async function waitAssistantDone(a: Actor, x: Ctx, threadId: string, nth: number, ms = 240_000): Promise<ApiMessage> {
  return until(`assistant reply #${nth} terminal`, async () => {
    const h = await history(a, x, threadId);
    const m = h.items.filter((i) => i.role === "assistant")[nth - 1];
    return m && ["done", "failed", "cancelled"].includes(m.status) ? m : false;
  }, ms, 1000);
}
