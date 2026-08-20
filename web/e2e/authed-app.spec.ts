import {
  test,
  expect,
  type APIRequestContext,
  type BrowserContext,
  type Page,
} from "@playwright/test";

/**
 * E2E scenarios: authed-app-load + docs-write-path
 * Scenario docs: web/e2e-scenarios/task-board-load.md
 *                web/e2e-scenarios/docs-write-path.md
 *
 * What this suite proves:
 *   1. the E2E credentials actually authenticate against the deployed API;
 *   2. the API honours that session and reports OUR user (not just "a" 200);
 *   3. the credential reaches the E2E sandbox workspace and NOTHING else —
 *      re-adding it to a real workspace turns this suite red on purpose;
 *   4. the authenticated shell routes into the workspace instead of bouncing
 *      to /login, and renders without console errors, uncaught page errors or
 *      5xx responses (F1 harness);
 *   5. a user driving the real UI CREATES a document and the server has it —
 *      the write path §1n asks for, which a read-only credential could not
 *      exercise at all;
 *   6. document comments can be written, resolved and unresolved under that
 *      same session.
 *
 * Everything test 5 and 6 create is deleted in afterAll, children before
 * parents (a document delete is soft and its comment routes resolve the
 * document first, so a comment outliving its document becomes unreachable
 * over the API).
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

/**
 * Where CI is allowed to write. See web/e2e-scenarios/docs-write-path.md and
 * the "Run Playwright E2E" step in .github/workflows/ci.yml for the full
 * standing decision; the short version is that E2E_USER_EMAIL belongs to
 * exactly one workspace, this one, and it is a scratch workspace whose only
 * permanent content is the fixtures below.
 */
const SANDBOX_WS_SLUG = "e2e-ci-sandbox";
const SANDBOX_PROJECT_SLUG = "e2e-fixtures";
/** A task that must survive every run — the read assert looks it up by title. */
const FIXTURE_TASK_TITLE = "E2E read fixture — do not delete";
/** Every throwaway document this suite makes is named with this prefix. */
const SCRATCH_PREFIX = "[e2e]";

let context: BrowserContext;
let page: Page;
let accessToken: string;
let consoleErrors: string[] = [];
let pageErrors: string[] = [];
const failedApiCalls: string[] = [];

let workspaceId: string;
let projectId: string;

/** Ids to remove in afterAll, and the order to remove them in. */
const createdCommentIds: string[] = [];
const createdDocumentIds: string[] = [];

const email = () => process.env.E2E_USER_EMAIL!;
const auth = () => ({ Authorization: `Bearer ${accessToken}` });

/** Unique per CI run (and per local run) so parallel branches cannot collide. */
const runTag = process.env.GITHUB_RUN_ID
  ? `${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT ?? "1"}`
  : `local-${Date.now()}`;

type DocumentRow = { id: string; title: string; created_at?: string };

async function listDocuments(req: APIRequestContext): Promise<DocumentRow[]> {
  const res = await req.get(`/api/v1/projects/${projectId}/documents`, {
    headers: auth(),
  });
  expect(res.status(), "the sandbox document list must be readable").toBe(200);
  const payload = (await res.json()) as { items?: DocumentRow[] };
  expect(Array.isArray(payload.items), "documents payload must carry items").toBe(true);
  return payload.items!;
}

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
  // Cleanup runs even when a test failed mid-way: anything that came back with
  // an id is on these lists, not "whatever the plan said should exist".
  if (accessToken && projectId) {
    for (const id of createdCommentIds) {
      const res = await context.request.delete(`/api/v1/document-comments/${id}`, {
        headers: auth(),
      });
      console.log(`[cleanup] DELETE document-comment ${id} -> ${res.status()}`);
    }
    for (const id of createdDocumentIds) {
      const res = await context.request.delete(`/api/v1/documents/${id}`, {
        headers: auth(),
      });
      console.log(`[cleanup] DELETE document ${id} -> ${res.status()}`);
    }

    // Evidence, printed into the job log: what the sandbox holds afterwards.
    const left = await listDocuments(context.request);
    console.log(
      `[cleanup] documents left in ${SANDBOX_PROJECT_SLUG}: ${left.length} ` +
        `[${left.map((d) => d.title).join(", ")}]`
    );
    // Asserted against THIS run's ids, not against "no scratch documents at
    // all": two PR runs can be in flight at once, and failing because somebody
    // else's document is mid-flight would be a flake, not a finding.
    expect(
      left.map((d) => d.id).filter((id) => createdDocumentIds.includes(id)),
      "this run must leave no documents behind"
    ).toEqual([]);
  }
  await context?.close();
});

test("the API honours the session and reports our user", async () => {
  // Unconditional. The previous version wrapped its only assertion in
  // `if (resp.status() === 200)`, so a 401 asserted nothing at all.
  const me = await context.request.get("/api/v1/auth/me", { headers: auth() });
  expect(me.status(), "/api/v1/auth/me must accept the session").toBe(200);

  const who = (await me.json()) as { email?: string; is_active?: boolean };
  expect(who.email?.toLowerCase()).toBe(email().toLowerCase());
  expect(who.is_active, "the E2E user must be active").toBe(true);

  const wss = await context.request.get("/api/v1/workspaces", { headers: auth() });
  expect(wss.status(), "/api/v1/workspaces must accept the session").toBe(200);
  const workspaces = (await wss.json()) as Array<{ id?: string; slug?: string }>;
  expect(Array.isArray(workspaces), "workspaces payload must be an array").toBe(true);

  // BLAST-RADIUS assert, not a shape check. This credential lives in GitHub
  // secrets and runs on every PR, so it is a member of the E2E sandbox and of
  // nothing else. If somebody adds it to a real workspace, this line goes red
  // the same day instead of the fact being discovered later.
  expect(
    workspaces.map((w) => w.slug).sort(),
    "the CI credential must reach the E2E sandbox and no other workspace"
  ).toEqual([SANDBOX_WS_SLUG]);
  workspaceId = workspaces[0]!.id!;

  const projects = await context.request.get(
    `/api/v1/workspaces/${workspaceId}/projects`,
    { headers: auth() }
  );
  expect(projects.status(), "the sandbox project list must be readable").toBe(200);
  const projPayload = (await projects.json()) as {
    items?: Array<{ id: string; slug: string }>;
  };
  const project = (projPayload.items ?? []).find((p) => p.slug === SANDBOX_PROJECT_SLUG);
  expect(
    project,
    `project "${SANDBOX_PROJECT_SLUG}" must exist in the sandbox — the write tests need somewhere to write`
  ).toBeTruthy();
  projectId = project!.id;

  // Workspace task search is the user-facing read path (it requires `search`;
  // omitting it is a 400, not an auth failure — worth knowing when this goes red).
  const tasks = await context.request.get(
    `/api/v1/workspaces/${workspaceId}/tasks?search=fixture&limit=10`,
    { headers: auth() }
  );
  expect(tasks.status(), "workspace tasks must be readable by the session").toBe(200);
  const payload = (await tasks.json()) as {
    items?: Array<{ title?: string }>;
    total_count?: number;
  };
  // `typeof [] === "object"` was the old assert — true for {} too, so it could
  // not tell a page of results from an error body. Naming the fixture row is
  // stronger than `total_count > 0`: it survives an unrelated task appearing
  // and fails if the fixture is deleted.
  expect(Array.isArray(payload.items), "tasks payload must carry an items array").toBe(true);
  expect(
    payload.items!.map((t) => t.title),
    "the read path must return the seeded fixture task"
  ).toContain(FIXTURE_TASK_TITLE);
});

test("the authenticated shell loads into the workspace, cleanly", async () => {
  await page.goto("/", { waitUntil: "networkidle" });

  // AUTH assert: AppLayout sends anonymous visitors to /login. Staying off it
  // is the proof the browser session was accepted, not a presence check.
  expect(
    new URL(page.url()).pathname,
    "an authenticated session must not be bounced to /login"
  ).not.toMatch(/^\/login/);

  // BEHAVIOR assert: the shell resolved a workspace and routed into it
  // ("/" → /w/<workspace-slug>/… in AppLayout).
  await expect
    .poll(() => new URL(page.url()).pathname, { timeout: 15_000 })
    .toMatch(new RegExp(`^/w/${SANDBOX_WS_SLUG}/.+`));

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

test("a user creates a document through the UI and the server has it", async () => {
  const title = `${SCRATCH_PREFIX} document ${runTag}`;

  const before = await listDocuments(context.request);
  console.log(
    `[write] documents before: ${before.length} [${before.map((d) => d.title).join(", ")}]`
  );

  // Sweep leftovers from a run that died before its afterAll. Age-gated by an
  // hour so a document another PR's run is using right now is never touched.
  const staleBefore = Date.now() - 60 * 60 * 1000;
  for (const doc of before) {
    if (!doc.title.startsWith(SCRATCH_PREFIX)) continue;
    if (!doc.created_at || Date.parse(doc.created_at) > staleBefore) continue;
    const res = await context.request.delete(`/api/v1/documents/${doc.id}`, {
      headers: auth(),
    });
    console.log(`[write] swept stale "${doc.title}" -> ${res.status()}`);
  }
  expect(
    before.map((d) => d.title),
    "the title this run is about to create must not already exist"
  ).not.toContain(title);

  await page.goto(`/w/${SANDBOX_WS_SLUG}/p/${SANDBOX_PROJECT_SLUG}/docs`, {
    waitUntil: "networkidle",
  });

  // The docs page offers "New page" on the empty state and "New" above the
  // tree; either one opens the same dialog.
  await page
    .getByRole("button", { name: /^New(\s+page)?$/ })
    .first()
    .click();
  await page.locator("#doc-title").fill(title);

  // Watch the request the click makes, so a refused write says "403" instead of
  // "element not found 15 seconds later". Measured: with the credential demoted
  // back to `viewer` this line reports the 403 immediately, which is the whole
  // point of the change — the old role could not get past here at all.
  const createResponse = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      /^\/api\/v1\/projects\/[^/]+\/documents$/.test(new URL(r.url()).pathname)
  );
  await page.getByRole("button", { name: "Create" }).click();
  expect(
    (await createResponse).status(),
    "the UI's create request must be accepted — a viewer-only credential answers 403 here"
  ).toBe(201);

  // BEHAVIOR assert: the document the user just made is rendered back to them.
  // Not `toBeVisible()` on something that was already on screen — this string
  // did not exist anywhere before the click.
  await expect(page.getByText(title, { exact: true }).first()).toBeVisible({
    timeout: 15_000,
  });

  // GROUND TRUTH: the server, asked independently, has exactly one such row.
  const after = await listDocuments(context.request);
  const mine = after.filter((d) => d.title === title);
  // Register for cleanup BEFORE asserting. The negative control caught this the
  // hard way: with the id recorded after the assert, the failing run left its
  // document in the sandbox and afterAll cheerfully reported "nothing left
  // behind" over an empty list. Whatever carries this run's title is this run's
  // to remove, whether or not the assert about it holds.
  createdDocumentIds.push(...mine.map((d) => d.id));
  expect(
    mine.length,
    "the UI reported success, so the server must hold exactly one document with this title"
  ).toBe(1);
  // Deliberately no `after.length === before.length + 1`: another PR's run may
  // legitimately add its own document in between. The unique title is the
  // concurrency-safe form of the same claim.

  // The F1 harness covers the create round-trip too: a 4xx/5xx behind a
  // cheerful UI would be caught here rather than being invisible.
  expect(failedApiCalls, "creating the document made API calls that failed").toEqual([]);
  expect(pageErrors, "uncaught page errors while creating the document").toEqual([]);
});

test("a comment is written to that document, resolved and unresolved", async () => {
  const docId = createdDocumentIds[0];
  expect(docId, "the previous test must have created a document").toBeTruthy();

  // Give the document something to anchor to. PATCH exercises the update side
  // of the write path (PermUploadArtifact), which create alone does not.
  const quote = "the write path is exercised";
  const patch = await context.request.patch(`/api/v1/documents/${docId}`, {
    headers: auth(),
    data: { body: `Run ${runTag}: ${quote} against the deployed host.\n` },
  });
  expect(patch.status(), "the document body must be writable").toBe(200);

  const created = await context.request.post(`/api/v1/documents/${docId}/comments`, {
    headers: auth(),
    data: { body: `E2E write-path check ${runTag}`, quote },
  });
  expect(
    created.status(),
    "POST /documents/:id/comments must succeed — a viewer-only credential answers 403 here, which is the defect this suite exists to keep fixed"
  ).toBe(201);
  const comment = (await created.json()) as {
    id: string;
    body: string;
    anchor?: { exact?: string; orphaned?: boolean };
    resolved_at?: string | null;
  };
  createdCommentIds.push(comment.id);

  // The server resolved the quote against the real body rather than storing
  // whatever offsets a client claimed (pkg/mdoc.SpanMatchesQuote).
  expect(comment.anchor?.exact, "the stored anchor must quote the document").toBe(quote);
  expect(comment.anchor?.orphaned, "a freshly anchored comment is not orphaned").toBe(false);

  const resolve = await context.request.post(
    `/api/v1/document-comments/${comment.id}/resolve`,
    { headers: auth() }
  );
  expect(resolve.status(), "resolving the comment must succeed").toBe(200);
  expect(
    ((await resolve.json()) as { resolved_at?: string | null }).resolved_at,
    "a resolved comment carries resolved_at"
  ).toBeTruthy();

  const unresolve = await context.request.post(
    `/api/v1/document-comments/${comment.id}/unresolve`,
    { headers: auth() }
  );
  expect(unresolve.status(), "unresolving the comment must succeed").toBe(200);
  expect(
    ((await unresolve.json()) as { resolved_at?: string | null }).resolved_at ?? null,
    "an unresolved comment has no resolved_at"
  ).toBeNull();

  // Read it back through the list endpoint the reading view uses, so the
  // assert is about what a user would see and not only about the write reply.
  const list = await context.request.get(`/api/v1/documents/${docId}/comments`, {
    headers: auth(),
  });
  expect(list.status(), "the comment list must be readable").toBe(200);
  const rows = ((await list.json()) as { items?: Array<{ id: string }> }).items;
  expect(Array.isArray(rows), "the thread list must be a page with items").toBe(true);
  expect(
    rows!.map((r) => r.id),
    "the comment must be in the document's thread list"
  ).toContain(comment.id);
});
