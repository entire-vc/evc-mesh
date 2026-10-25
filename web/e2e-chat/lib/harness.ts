import { createHash } from "node:crypto";
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { APIRequestContext, Browser, BrowserContext, Page } from "@playwright/test";
import { loginWithRetry } from "../../e2e/auth-helper";
import { ACTORS, type ActorId, OUT_DIR, SetupFail, cfg } from "./env";
import type { Frame } from "./detect";

mkdirSync(OUT_DIR, { recursive: true });

export const sha256 = (b: Buffer | Uint8Array) => createHash("sha256").update(b).digest("hex");

/** One line per S/P/F1 result, written the moment it is known (artifact: results.tsv). */
export function row(id: string, who: string, status: "PASS" | "FAIL" | "RED-OK", detail = ""): void {
  appendFileSync(join(OUT_DIR, "results.tsv"), `${id}\t${who}\t${status}\t${detail.replace(/\s+/g, " ").slice(0, 300)}\n`);
}

/** Runs a step, records PASS/FAIL, rethrows. */
export async function step<T>(id: string, who: string, fn: () => Promise<T>): Promise<T> {
  try {
    const out = await fn();
    row(id, who, "PASS");
    return out;
  } catch (e) {
    row(id, who, "FAIL", (e as Error).message);
    throw e;
  }
}

/**
 * A red control passes only when the detector under test throws. With `reason`
 * the error must also be the detector's own: a setup error, a TypeError or a
 * dead page would otherwise count as a caught mutant.
 */
export async function redControl(id: string, who: string, mutated: () => unknown | Promise<unknown>, reason?: RegExp): Promise<void> {
  let err: Error | undefined;
  try {
    await mutated();
  } catch (e) {
    err = e instanceof Error ? e : new Error(String(e));
  }
  if (!err) {
    row(id, who, "FAIL", "red control stayed green: the check cannot fail");
    throw new Error(`${id} red control (${who}) stayed green — the check cannot fail`);
  }
  if (reason && !reason.test(err.message)) {
    row(id, who, "FAIL", `red control failed for another reason: ${err.message}`);
    throw new Error(`${id} red control (${who}) failed for another reason, not by its detector: ${err.message}`);
  }
  row(id, who, "RED-OK", reason ? err.message : "");
}

export function timeline(name: string, events: unknown[]): void {
  writeFileSync(join(OUT_DIR, `timeline-${name}.json`), JSON.stringify(events, null, 1));
}

export interface ExpectedError {
  method: string;
  path: RegExp;
  status: number;
}

/** F1: the global error marker E of the scenario. 4xx count unless registered by exact request. */
export class F1 {
  consoleErrors: string[] = [];
  pageErrors: string[] = [];
  unexpected: string[] = [];
  private expected: Array<ExpectedError & { hits: number }> = [];

  expect(e: ExpectedError): void {
    this.expected.push({ ...e, hits: 0 });
  }

  attach(page: Page): void {
    page.on("pageerror", (err) => this.pageErrors.push(err.message));
    page.on("console", (msg) => {
      if (msg.type() !== "error") return;
      const url = msg.location().url;
      // The browser logs a failed fetch as a console error: it is the expected one only when the request is.
      if (url && this.isExpectedUrl(url)) return;
      this.consoleErrors.push(`${msg.text().slice(0, 200)}${url ? ` [${new URL(url).pathname.slice(0, 120)}]` : ""}`);
    });
    page.on("response", (resp) => {
      const u = new URL(resp.url());
      if (!u.pathname.startsWith("/api/")) return;
      const s = resp.status();
      if (s < 400) return;
      if (s === 401 && u.pathname.endsWith("/auth/refresh")) return;
      if (s === 404 && resp.request().method() === "GET" && u.pathname.startsWith("/api/v1/me/ui-state/")) return;
      const hit = this.expected.find((e) => e.status === s && e.method === resp.request().method() && e.path.test(u.pathname));
      if (hit) {
        hit.hits++;
        return;
      }
      this.unexpected.push(`${s >= 500 ? "5xx" : "4xx"} ${s} ${resp.request().method()} ${u.pathname}`);
    });
  }

  private isExpectedUrl(url: string): boolean {
    const u = new URL(url);
    return this.expected.some((e) => e.path.test(u.pathname));
  }

  /** `requireExpected=false` when the tests that make the registered negatives were filtered out of the run. */
  verify(who: string, requireExpected = true): void {
    const bad = [
      ...this.consoleErrors.map((m) => `console.error: ${m}`),
      ...this.pageErrors.map((m) => `pageerror: ${m}`),
      ...this.unexpected,
    ];
    const missed = requireExpected ? this.expected.filter((e) => e.hits === 0).map((e) => `${e.method} ${e.path} ${e.status} never seen`) : [];
    row("F1", who, bad.length + missed.length === 0 ? "PASS" : "FAIL", [...bad, ...missed].join(" | "));
    if (bad.length > 0) throw new Error(`F1 (${who}): ${bad.slice(0, 5).join(" | ")}`);
    if (missed.length > 0) throw new Error(`F1 (${who}): an expected negative never happened: ${missed.join(" | ")}`);
  }
}

export interface Actor {
  id: ActorId;
  userId: string;
  token: string;
  context: BrowserContext;
  page: Page;
  f1: F1;
  frames: Frame[];
  /** The same call made by the page itself (routable by context.route, seen by F1): the subject under test. */
  fetch: (method: string, path: string, body?: unknown) => Promise<{ status: number; text: string }>;
  /** Authenticated API call with this actor's own session (observer, not routable). */
  api: (method: string, path: string, body?: unknown, opts?: { maxRedirects?: number }) => Promise<{ status: number; text: string; json: () => unknown; headers: Record<string, string>; bytes: () => Promise<Buffer> }>;
  close: () => Promise<void>;
}

function parseFrame(payload: string): Frame | null {
  try {
    const msg = JSON.parse(payload) as { type?: string; data?: Record<string, unknown> };
    const d = msg.data ?? {};
    const event = typeof d.event === "string" ? d.event : (msg.type ?? "");
    const message = d.message as { id?: string } | undefined;
    return {
      event,
      thread_id: d.thread_id as string | undefined,
      turn_id: (d.turn_id as string | undefined) ?? ((d.turn as { id?: string } | undefined)?.id),
      message_id: (d.message_id as string | undefined) ?? message?.id,
      seq: typeof d.seq === "number" ? d.seq : undefined,
      textLen: typeof d.text === "string" ? [...d.text].length : undefined,
      raw: payload,
    };
  } catch {
    return null;
  }
}

/**
 * Init script: every message of every WebSocket the page constructs goes to the
 * __e2eChatFrame binding. window.WebSocket is held behind an accessor, so a class
 * installed later (Playwright's routeWebSocket mock) is wrapped as well.
 */
function tapWebSocket(): void {
  const w = window as unknown as { WebSocket: typeof WebSocket; __e2eChatFrame?: (p: string) => void };
  const tapped = new WeakSet<object>();
  const wrap = (W: typeof WebSocket): typeof WebSocket => {
    if (tapped.has(W)) return W;
    const N = new Proxy(W, {
      construct(t, args, nt) {
        const sock = Reflect.construct(t, args, nt) as WebSocket;
        sock.addEventListener("message", (ev) => {
          if (typeof ev.data === "string") void w.__e2eChatFrame?.(ev.data);
        });
        return sock;
      },
    });
    tapped.add(N);
    return N;
  };
  let current = wrap(w.WebSocket);
  Object.defineProperty(window, "WebSocket", { configurable: true, get: () => current, set: (W: typeof WebSocket) => { current = wrap(W); } });
}

/** One real login and one isolated browser context per actor; storage is never shared. */
export async function openActor(browser: Browser, id: ActorId): Promise<Actor> {
  const c = cfg();
  const context = await browser.newContext({ baseURL: c.baseURL, viewport: { width: 1440, height: 900 }, permissions: ["clipboard-read", "clipboard-write"] });
  let page: Page | undefined;
  try {
    const { accessToken } = await loginWithRetry(context.request, c.email(id), c.password(id));
    const me = await context.request.get("/api/v1/auth/me", { headers: { Authorization: `Bearer ${accessToken}` } });
    if (me.status() !== 200) throw new SetupFail(`actor ${id}: /auth/me answered ${me.status()}`);
    const who = (await me.json()) as { id?: string; email?: string };
    if (!who.id) throw new SetupFail(`actor ${id}: /auth/me carries no user id`);
    if (who.email?.toLowerCase() !== c.email(id).toLowerCase()) throw new SetupFail(`actor ${id}: session belongs to another user`);
    // Frames are taken inside the page, from every socket the app opens, as the app
    // receives them. CDP's page.on("websocket") does not see a socket served by
    // page.routeWebSocket, so the P05 mutant would be invisible to it.
    const frames: Frame[] = [];
    await context.exposeBinding("__e2eChatFrame", (_src, payload: string) => {
      const fr = parseFrame(payload);
      if (fr) frames.push(fr);
    });
    await context.addInitScript(tapWebSocket);
    page = await context.newPage();
    const f1 = new F1();
    f1.attach(page);
    const auth = { Authorization: `Bearer ${accessToken}` };
    const req: APIRequestContext = context.request;
    return {
      id, userId: who.id, token: accessToken, context, page, f1, frames,
      async fetch(method, path, body) {
        return page!.evaluate(async ([m, u, b, t]) => {
          const r = await fetch(u as string, { method: m as string, headers: { Authorization: `Bearer ${t}`, ...(b ? { "Content-Type": "application/json" } : {}) }, body: b ? (b as string) : undefined, redirect: "manual" });
          return { status: r.status === 0 ? 302 : r.status, text: await r.text().catch(() => "") };
        }, [method, path, body === undefined ? "" : JSON.stringify(body), accessToken]);
      },
      async api(method, path, body, opts) {
        const res = await req.fetch(path, { method, headers: auth, data: body as never, maxRedirects: opts?.maxRedirects });
        const text = await res.text().catch(() => "");
        return { status: res.status(), text, json: () => JSON.parse(text), headers: res.headers(), bytes: () => res.body() };
      },
      close: () => context.close(),
    };
  } catch (e) {
    await context.close();
    throw e;
  }
}

/** The four actors must be four different users: separate tokens alone prove nothing. */
export async function openAllActors(browser: Browser): Promise<Record<ActorId, Actor>> {
  const out = {} as Record<ActorId, Actor>;
  try {
    // Sequential: /auth/login is capped at 5/min per IP and loginWithRetry honours Retry-After.
    for (const a of ACTORS) out[a] = await openActor(browser, a);
    const ids = ACTORS.map((a) => out[a].userId);
    if (new Set(ids).size !== ACTORS.length) throw new SetupFail("A, B, C, V do not resolve to four distinct users");
    return out;
  } catch (e) {
    await Promise.allSettled(Object.values(out).map((a) => a.close()));
    throw e;
  }
}

export interface Ctx {
  workspaceId: string;
  projectId: string;
  wsSlug: string;
  projectSlug: string;
}

export async function resolveCtx(a: Actor): Promise<Ctx> {
  const c = cfg();
  const ws = (await a.api("GET", "/api/v1/workspaces")).json() as { items?: Array<{ id: string; slug: string }> } | Array<{ id: string; slug: string }>;
  const list = Array.isArray(ws) ? ws : (ws.items ?? []);
  const w = list.find((x) => x.slug === c.wsSlug);
  if (!w) throw new SetupFail(`actor ${a.id} does not see workspace ${c.wsSlug}`);
  const pr = await a.api("GET", `/api/v1/workspaces/${w.id}/projects`);
  const p = ((pr.json() as { items?: Array<{ id: string; slug: string }> }).items ?? []).find((x) => x.slug === c.projectSlug);
  if (!p) throw new SetupFail(`actor ${a.id} does not see project ${c.projectSlug}`);
  return { workspaceId: w.id, projectId: p.id, wsSlug: c.wsSlug, projectSlug: c.projectSlug };
}

const THREADS_FILE = join(OUT_DIR, ".threads.jsonl");

/** Every thread a spec creates is recorded here; teardown deletes exactly these, by their author. */
export function track(actor: ActorId, threadId: string): void {
  appendFileSync(THREADS_FILE, `${JSON.stringify({ actor, threadId })}\n`);
}

export function trackedThreads(): Array<{ actor: ActorId; threadId: string }> {
  try {
    return readFileSync(THREADS_FILE, "utf8").split("\n").filter(Boolean).map((l) => JSON.parse(l));
  } catch {
    return [];
  }
}

export const chatBase = (x: Ctx) => `/api/v1/projects/${x.projectId}/chat`;
export const chatUrl = (x: Ctx, threadId?: string) => `/w/${x.wsSlug}/p/${x.projectSlug}/chat${threadId ? `/${threadId}` : ""}`;

/** Frames, screenshots and an HTTP-free DOM check must carry this to be a real wait: no sleeps. */
export async function until<T>(what: string, fn: () => Promise<T | undefined | false>, ms = 120_000, every = 750): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for: ${what}`);
    await new Promise((r) => setTimeout(r, every));
  }
}

/** 1440 and 393, light and dark, of the state the page is in now. */
export async function snap(page: Page, name: string, prepare?: () => Promise<void>, cleanup?: () => Promise<void>): Promise<void> {
  const dir = join(OUT_DIR, "shots");
  mkdirSync(dir, { recursive: true });
  const was = page.viewportSize() ?? { width: 1440, height: 900 };
  for (const vp of [{ width: 1440, height: 900 }, { width: 393, height: 852 }]) {
    for (const scheme of ["light", "dark"] as const) {
      await page.setViewportSize(vp);
      await page.evaluate((s) => localStorage.setItem("theme", s), scheme);
      await page.reload();
      await page.getByTestId("chat-composer").or(page.getByTestId("chat-no-thread")).first().waitFor({ timeout: 30_000 });
      await prepare?.();
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      if (overflow > 0) throw new Error(`${name} ${vp.width}/${scheme}: horizontal overflow ${overflow}px`);
      await page.screenshot({ path: join(dir, `${name}-${vp.width}-${scheme}.png`) });
      await cleanup?.();
    }
  }
  await page.setViewportSize(was);
}
