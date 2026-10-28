import { createHash } from "node:crypto";
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { APIRequestContext, Browser, BrowserContext, Locator, Page } from "@playwright/test";
import { loginWithRetry } from "../../e2e/auth-helper";
import { ACTORS, type ActorId, OUT_DIR, SetupFail, cfg } from "./env";
import { type Frame, type PaintedMode, assertEffectiveMode, assertFramesDiffer } from "./detect";

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

// GET /me/ui-state/:scope answers 404 until the person stores something there (user_ui_state_handler.go);
// a fresh fixture user has stored nothing, and the browser also logs that 404 as a console error.
const isUnsetUiState = (pathname: string): boolean => pathname.startsWith("/api/v1/me/ui-state/");

/** F1: the global error marker E of the scenario. 4xx count unless registered by exact request. */
export class F1 {
  consoleErrors: string[] = [];
  pageErrors: string[] = [];
  unexpected: string[] = [];
  private expected: Array<ExpectedError & { hits: number }> = [];
  private networkDown = false;
  /** Failed loads while the browser was deliberately offline (AC5): expected, counted, not reported. */
  networkDownHits = 0;

  /** Opens and closes the window in which the context is offline on purpose. */
  setNetworkDown(on: boolean): void {
    this.networkDown = on;
  }

  private offlineNoise(text: string): boolean {
    if (!this.networkDown || !/ERR_INTERNET_DISCONNECTED|ERR_NETWORK_CHANGED|Failed to fetch|NetworkError|WebSocket/i.test(text)) return false;
    this.networkDownHits++;
    return true;
  }

  expect(e: ExpectedError): void {
    this.expected.push({ ...e, hits: 0 });
  }

  attach(page: Page): void {
    page.on("pageerror", (err) => {
      if (!this.offlineNoise(err.message)) this.pageErrors.push(err.message);
    });
    page.on("console", (msg) => {
      if (msg.type() !== "error" || this.offlineNoise(msg.text())) return;
      const url = msg.location().url;
      // The browser logs a failed fetch as a console error: it is the expected one only when the request is.
      if (url && this.isExpectedUrl(url)) return;
      if (url && msg.text().includes("status of 404") && isUnsetUiState(new URL(url).pathname)) return;
      this.consoleErrors.push(`${msg.text().slice(0, 200)}${url ? ` [${new URL(url).pathname.slice(0, 120)}]` : ""}`);
    });
    page.on("response", (resp) => {
      const u = new URL(resp.url());
      if (!u.pathname.startsWith("/api/")) return;
      const s = resp.status();
      if (s < 400) return;
      if (s === 401 && u.pathname.endsWith("/auth/refresh")) return;
      if (s === 404 && resp.request().method() === "GET" && isUnsetUiState(u.pathname)) return;
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

  /**
   * `requireExpected=false` when the tests that make the registered negatives were filtered out of the run.
   * `record=false` for detector controls: the job fails on any FAIL row, and a control's planned red is not one.
   */
  verify(who: string, requireExpected = true, record = true): void {
    const bad = [
      ...this.consoleErrors.map((m) => `console.error: ${m}`),
      ...this.pageErrors.map((m) => `pageerror: ${m}`),
      ...this.unexpected,
    ];
    const missed = requireExpected ? this.expected.filter((e) => e.hits === 0).map((e) => `${e.method} ${e.path} ${e.status} never seen`) : [];
    if (record) row("F1", who, bad.length + missed.length === 0 ? "PASS" : "FAIL", [...bad, ...missed].join(" | "));
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
      if (fr) frames.push({ ...fr, at: Date.now() });
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

/** The one error until() throws when its budget runs out; callers tell it from other errors by class, not by text. */
export class UntilTimeout extends Error {}

/** Frames, screenshots and an HTTP-free DOM check must carry this to be a real wait: no sleeps. */
export async function until<T>(what: string, fn: () => Promise<T | undefined | false>, ms = 120_000, every = 750): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new UntilTimeout(`timed out after ${ms} ms waiting for: ${what}`);
    await new Promise((r) => setTimeout(r, every));
  }
}

const APPEARANCE = "/api/v1/me/ui-state/appearance";

/** What the page paints right now: the class mode.ts toggles and the first opaque background from <body> up. */
export async function paintedMode(page: Page): Promise<PaintedMode> {
  return page.evaluate(() => {
    const rgb = (el: Element | null): number[] | null => {
      for (let e = el; e; e = e.parentElement) {
        const m = getComputedStyle(e).backgroundColor.match(/[\d.]+/g)?.map(Number);
        if (m && m.length >= 3 && (m.length < 4 || m[3]! > 0.5)) return m.slice(0, 3);
      }
      return null;
    };
    const c = rgb(document.body) ?? [255, 255, 255];
    const lin = (v: number) => {
      const x = v / 255;
      return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
    };
    return {
      htmlDark: document.documentElement.classList.contains("dark"),
      bgLuminance: 0.2126 * lin(c[0]!) + 0.7152 * lin(c[1]!) + 0.0722 * lin(c[2]!),
    };
  });
}

/**
 * The person's colour mode lives on the server (user_ui_state `appearance`) and the
 * sign-in sync overwrites localStorage "theme" with it: writing only localStorage
 * gives a "dark" frame painted light. So the server state, the local cache and the
 * OS preference are all set to the mode, and the painted mode is asserted.
 */
async function setMode(a: Actor, scheme: "light" | "dark", density: unknown): Promise<void> {
  const put = await a.api("PUT", APPEARANCE, { state: { mode: scheme, ...(density ? { density } : {}) } });
  if (put.status >= 300) throw new Error(`appearance PUT ${scheme} answered ${put.status}`);
  await a.page.emulateMedia({ colorScheme: scheme });
  await a.page.evaluate((s) => localStorage.setItem("theme", s), scheme);
}

async function settledMode(page: Page, scheme: "light" | "dark"): Promise<void> {
  // The sign-in sync is async after the reload; give it a bounded moment, then judge the last reading.
  let last: PaintedMode | undefined;
  try {
    await until(`painted mode ${scheme}`, async () => {
      last = await paintedMode(page);
      try {
        assertEffectiveMode(scheme, last);
        return true;
      } catch {
        return false;
      }
    }, 10_000, 250);
  } catch {
    /* judged below with the reading itself */
  }
  assertEffectiveMode(scheme, last ?? (await paintedMode(page)));
}

const threadInUrl = (page: Page): string | undefined => new URL(page.url()).pathname.match(/\/chat\/([0-9a-f-]{36})(?:\/|$)/)?.[1];

/**
 * What a ready chat screen is, read once without waiting: no skeleton anywhere,
 * and the screen the URL names. With a thread: its header (session state is
 * rendered only once the thread itself is loaded) and its feed; without: the
 * no-thread state. The composer alone proves nothing, it renders while the
 * history is still loading. Returns why it is not ready, "" when it is.
 */
export async function chatNotReady(page: Page): Promise<string> {
  const threadId = threadInUrl(page);
  return page.evaluate((tid) => {
    const q = (id: string) => document.querySelector(`[data-testid="${id}"]`);
    if (q("chat-feed-loading")) return "the thread history skeleton is shown";
    if (q("chat-sidebar-loading")) return "the chat list skeleton is shown";
    if (!tid) return q("chat-no-thread") ? "" : "the no-thread state is not shown";
    if (q("chat-not-found")) return `thread ${tid} is not found`;
    if (!q("chat-session-state")) return "the thread header is not loaded";
    if (!q("chat-feed")) return "the thread feed is not shown";
    return "";
  }, threadId);
}

/** Waits for chatNotReady to answer "" and throws with its last reason. */
export async function waitChatReady(page: Page, ms = 30_000): Promise<void> {
  let why = "";
  try {
    await until("ready chat screen", async () => (why = await chatNotReady(page)) === "", ms, 200);
  } catch {
    throw new Error(`chat screen not ready after ${ms} ms: ${why}`);
  }
}

/**
 * AC1 of the frame set, on the live page, once per run: with the thread history held
 * back, the old gate (composer or no-thread) passes over a skeleton, and the
 * readiness check must refuse that same page.
 */
async function readinessControl(a: Actor): Promise<void> {
  const { page } = a;
  const tid = threadInUrl(page);
  if (!tid) return;
  const route = new RegExp(`/threads/${tid}/messages(\\?|$)`);
  let release!: () => void;
  const held = new Promise<void>((r) => (release = r));
  let intercepted!: () => void;
  const reached = new Promise<void>((r) => (intercepted = r));
  await page.route(route, async (r) => {
    if (r.request().method() === "GET") {
      intercepted();
      await held;
    }
    await r.fallback();
  });
  try {
    await page.reload();
    const hit = await Promise.race([reached.then(() => true), page.waitForTimeout(30_000).then(() => false)]);
    if (!hit) {
      row("AC1-red-history-held", a.id, "FAIL", `no GET of thread ${tid} history reached the hold`);
      throw new Error("AC1 red control: the history GET was never intercepted, nothing was held");
    }
    await page.getByTestId("chat-composer").or(page.getByTestId("chat-no-thread")).first().waitFor({ timeout: 30_000 });
    const skeleton = await page.getByTestId("chat-feed-loading").count();
    if (skeleton === 0) {
      row("AC1-red-history-held", a.id, "FAIL", "history held back but no skeleton: the control proves nothing");
      throw new Error("AC1 red control: the held history did not leave a skeleton on screen");
    }
    row("AC1-old-gate-passes-skeleton", a.id, "RED-OK", "composer-or-no-thread passed with chat-feed-loading on screen");
    await redControl("AC1-red-history-held", a.id, async () => {
      const why = await chatNotReady(page);
      if (why) throw new Error(why);
    }, /skeleton/);
  } finally {
    release();
    await page.unroute(route);
  }
  await waitChatReady(page);
}

let modeControlDone = false;
let readyControlDone = false;

/**
 * 1440 and 393, light and dark, of the state the page is in now; the actor's appearance is restored after.
 * Every frame waits for a ready screen (no skeleton, the URL's thread loaded) after the reload and
 * again after `prepare`; `shows` are the state the frame is about and must be visible in each frame.
 */
export async function snap(
  a: Actor,
  name: string,
  prepare?: () => Promise<void>,
  cleanup?: () => Promise<void>,
  shows: Locator[] = [],
  check?: (where: string) => Promise<void>,
): Promise<void> {
  const { page } = a;
  const dir = join(OUT_DIR, "shots");
  mkdirSync(dir, { recursive: true });
  const was = page.viewportSize() ?? { width: 1440, height: 900 };
  const base = await a.api("GET", APPEARANCE);
  if (base.status !== 200 && base.status !== 404) throw new Error(`appearance GET answered ${base.status}`);
  const baseline = base.status === 200 ? ((base.json() as { state?: { mode?: string; density?: string } }).state ?? null) : null;
  let shotsOk = false;
  try {
    // Red control on the live page, once per run: a light page judged as dark must fail.
    if (!modeControlDone) {
      await setMode(a, "light", baseline?.density);
      await page.reload();
      await settledMode(page, "light");
      await redControl("AC7-red-light-as-dark", a.id, () => settledMode(page, "dark"));
      modeControlDone = true;
    }
    if (!readyControlDone && threadInUrl(page)) {
      await readinessControl(a);
      readyControlDone = true;
    }
    for (const vp of [{ width: 1440, height: 900 }, { width: 393, height: 852 }]) {
      const shas: Record<string, string> = {};
      for (const scheme of ["light", "dark"] as const) {
        await page.setViewportSize(vp);
        await setMode(a, scheme, baseline?.density);
        await page.reload();
        await waitChatReady(page);
        await prepare?.();
        await waitChatReady(page);
        for (const l of shows) await l.first().waitFor({ state: "visible", timeout: 30_000 }).catch(() => {
          throw new Error(`${name} ${vp.width}/${scheme}: ${l} is not visible after the reload`);
        });
        await settledMode(page, scheme);
        await check?.(`${name} ${vp.width}/${scheme}`);
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        if (overflow > 0) throw new Error(`${name} ${vp.width}/${scheme}: horizontal overflow ${overflow}px`);
        const late = await chatNotReady(page);
        if (late) throw new Error(`${name} ${vp.width}/${scheme}: ${late} at the moment of the shot`);
        const png = await page.screenshot({ path: join(dir, `${name}-${vp.width}-${scheme}.png`) });
        shas[scheme] = sha256(png);
        await cleanup?.();
      }
      assertFramesDiffer(`${name} ${vp.width}`, shas.light!, shas.dark!);
    }
    shotsOk = true;
  } finally {
    await page.emulateMedia({ colorScheme: null }).catch(() => undefined);
    await page.evaluate((m) => (m ? localStorage.setItem("theme", m) : localStorage.removeItem("theme")), baseline?.mode ?? null).catch(() => undefined);
    await page.setViewportSize(was);
    // Back to the actor's own choice; with nothing stored before, "system" is the app's own default.
    // A restore that fails or reads back differently is its own FAIL row; it throws only when the
    // shots passed, so it never hides the error of a failed shot.
    const restored = await restoreAppearance(a, baseline ?? { mode: "system" });
    if (shotsOk && restored) throw new Error(`appearance of ${a.id} not restored: ${restored}`);
  }
}

/** Puts the appearance back and reads it back; returns why it failed, "" when restored. */
async function restoreAppearance(a: Actor, want: { mode?: string; density?: string }): Promise<string> {
  let why = "";
  try {
    const put = await a.api("PUT", APPEARANCE, { state: want });
    if (put.status >= 300) why = `restore PUT answered ${put.status}`;
    else {
      const back = await a.api("GET", APPEARANCE);
      const got = back.status === 200 ? ((back.json() as { state?: { mode?: string; density?: string } }).state ?? {}) : {};
      if (got.mode !== want.mode || (want.density !== undefined && got.density !== want.density)) {
        why = `readback mode=${got.mode} density=${got.density}, expected mode=${want.mode} density=${want.density}`;
      }
    }
  } catch (e) {
    why = `restore crashed: ${(e as Error).message}`;
  }
  row("AC7-appearance-restore", a.id, why ? "FAIL" : "PASS", why);
  return why;
}
