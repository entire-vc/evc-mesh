import {
  test,
  expect,
  type BrowserContext,
  type Page,
} from "@playwright/test";

/**
 * E2E scenario: authed-app-load
 * Scenario doc: web/e2e-scenarios/task-board-load.md
 *
 * What this suite proves:
 *   1. the E2E credentials actually authenticate against the deployed API;
 *   2. the API honours that session and reports OUR user (not just "a" 200);
 *   3. the authenticated shell routes into the workspace instead of bouncing
 *      to /login, and renders without console errors, uncaught page errors or
 *      5xx responses (F1 harness).
 *
 * Why one shared context, logged in once (and why NOT storageState):
 * Mesh rotates refresh tokens one-shot — presenting an already-revoked token
 * is treated as theft and revokes EVERY session for that user
 * (internal/auth/service.go, ErrTokenReused). A saved storageState replayed
 * into a second browser context presents the same cookie twice, so a
 * storageState-based suite would kill its own session on the second test. One
 * context, one login: within it navigator.locks serializes refreshes the way
 * the app intends. This also keeps the suite to a single hit on
 * /api/v1/auth/login, which is rate-limited to 5/min per IP.
 */

test.describe.configure({ mode: "serial" });

let context: BrowserContext;
let page: Page;
let accessToken: string;
let consoleErrors: string[] = [];
let pageErrors: string[] = [];
const failedApiCalls: string[] = [];

const email = () => process.env.E2E_USER_EMAIL!;

test.beforeAll(async ({ browser }) => {
  context = await browser.newContext();

  // Log in through the real endpoint the app uses. The response carries the
  // access token; the httpOnly refresh cookie lands in this context's jar.
  const res = await context.request.post("/api/v1/auth/login", {
    data: { email: email(), password: process.env.E2E_USER_PASSWORD },
  });
  expect(
    res.status(),
    `login as ${email()} must succeed — E2E credentials are wrong or the user is disabled`
  ).toBe(200);

  const body = (await res.json()) as {
    tokens?: { access_token?: string };
    user?: { email?: string };
  };
  expect(body.tokens?.access_token, "login must return an access token").toBeTruthy();
  accessToken = body.tokens!.access_token!;

  page = await context.newPage();
  await page.addInitScript(() => {
    (window as unknown as Record<string, unknown>).__ceErrs = [];
    const orig = console.error.bind(console);
    console.error = (...args: unknown[]) => {
      ((window as unknown as Record<string, unknown[]>).__ceErrs).push(
        args.map(String).join(" ")
      );
      orig(...args);
    };
  });
  page.on("pageerror", (err) => pageErrors.push(err.message));

  // Every API call the authenticated shell makes must succeed. This is the
  // assert that keeps working as the page evolves: it needs no knowledge of
  // which endpoints today's landing page happens to hit.
  page.on("response", (resp) => {
    const url = new URL(resp.url());
    if (!url.pathname.startsWith("/api/v1/")) return;
    // The app deliberately probes /auth/refresh before it knows whether the
    // visitor is logged in; a 401 there is the documented "not logged in"
    // answer, not a defect.
    if (resp.status() >= 400 && !url.pathname.endsWith("/auth/refresh")) {
      failedApiCalls.push(`${resp.status()} ${resp.request().method()} ${url.pathname}`);
    }
  });
});

test.afterAll(async () => {
  await context?.close();
});

test("the API honours the session and reports our user", async () => {
  const auth = { Authorization: `Bearer ${accessToken}` };

  // Unconditional. The previous version wrapped its only assertion in
  // `if (resp.status() === 200)`, so a 401 asserted nothing at all.
  const me = await context.request.get("/api/v1/auth/me", { headers: auth });
  expect(me.status(), "/api/v1/auth/me must accept the session").toBe(200);

  const who = (await me.json()) as { email?: string; is_active?: boolean };
  expect(who.email?.toLowerCase()).toBe("PLANTED-BREAK-not-our-user@example.invalid");
  expect(who.is_active, "the E2E user must be active").toBe(true);

  // The authenticated data plane answers with a real payload, reached the way
  // the app reaches it: workspaces first, then that workspace's tasks.
  // (`/api/v1/tasks` is agent-key territory and 404s for a user token — an
  // assert against it would have been red for the wrong reason.)
  const wss = await context.request.get("/api/v1/workspaces", { headers: auth });
  expect(wss.status(), "/api/v1/workspaces must accept the session").toBe(200);
  const workspaces = (await wss.json()) as Array<{ id?: string; slug?: string }>;
  expect(Array.isArray(workspaces), "workspaces payload must be an array").toBe(true);
  expect(
    workspaces.length,
    "the E2E user must belong to at least one workspace, or the UI has nothing to render"
  ).toBeGreaterThan(0);

  // Workspace task search is the user-facing read path (it requires `search`;
  // omitting it is a 400, not an auth failure — worth knowing when this goes red).
  const tasks = await context.request.get(
    `/api/v1/workspaces/${workspaces[0]!.id}/tasks?search=a&limit=1`,
    { headers: auth }
  );
  expect(tasks.status(), "workspace tasks must be readable by the session").toBe(200);
  const payload = (await tasks.json()) as {
    items?: unknown;
    total_count?: number;
  };
  // `typeof [] === "object"` was the old assert — true for {} too, so it could
  // not tell a page of results from an error body.
  expect(Array.isArray(payload.items), "tasks payload must carry an items array").toBe(true);
  expect(
    payload.total_count,
    "the read path must return real rows, not an empty page"
  ).toBeGreaterThan(0);
});

test("the authenticated shell loads into the workspace, cleanly", async () => {
  await page.goto("/", { waitUntil: "networkidle" });

  // AUTH assert: AppLayout sends anonymous visitors to /login. Staying off it
  // is the proof the browser session was accepted, not a presence check.
  expect(
    new URL(page.url()).pathname,
    "an authenticated session must not be bounced to /login"
  ).toMatch(/^\/PLANTED-BREAK-this-path-cannot-exist/);

  // BEHAVIOR assert: the shell resolved a workspace and routed into it
  // ("/" → /w/<workspace-slug>/activity in AppLayout).
  await expect
    .poll(() => new URL(page.url()).pathname, { timeout: 15_000 })
    .toMatch(/^\/w\/[^/]+\/.+/);

  // Workspace navigation only renders inside the authed layout.
  await expect(page.locator('a[href^="/w/"]').first()).toBeVisible({
    timeout: 15_000,
  });

  // F1 harness — unconditional. These assertions used to sit behind
  // PW_FAIL_ON_CONSOLE_ERROR / PW_FAIL_ON_5XX, which CI never set.
  consoleErrors = await page.evaluate(
    () => (window as unknown as Record<string, string[]>).__ceErrs ?? []
  );
  const http5xx = await page.evaluate(() =>
    performance
      .getEntriesByType("resource")
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      .filter((r: any) => r.responseStatus >= 500)
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      .map((r: any) => `${r.responseStatus} ${r.name}`)
  );
  expect(
    failedApiCalls,
    "the authenticated shell made API calls that failed"
  ).toEqual([]);
  expect(consoleErrors, "console.error calls detected (F1)").toEqual([]);
  expect(pageErrors, "uncaught page errors detected (F1)").toEqual([]);
  expect(http5xx, "HTTP 5xx responses detected (F1)").toEqual([]);
});
