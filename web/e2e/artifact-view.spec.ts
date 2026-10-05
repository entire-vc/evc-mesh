import {
  test,
  expect,
  type APIRequestContext,
  type BrowserContext,
  type Page,
} from "@playwright/test";
import { loginWithRetry } from "./auth-helper";

/**
 * E2E scenario: artifact-view (route /a/:artifactId, card #eb6fde4e)
 *
 * What this suite proves:
 *   1. an artifact uploaded to a project WITHOUT Team Relay integration —
 *      the common case — opens on its own Mesh page and renders there: CSV
 *      as a table, JSON pretty-printed (not the stored one-line dump);
 *   2. the click path from the task page (Preview / Open in new tab) lands
 *      on /a/<id> in a NEW TAB, under the session — no third-party origin
 *      in between (the docs.entire.vc detour this card removes; the sandbox
 *      has no TR integration, so any such request would itself be the bug);
 *   3. the page loads without console errors, uncaught errors or failed
 *      API calls (F1 harness rules).
 *
 * Own login, own browser context — not shared with the other spec files.
 * Mesh rotates refresh tokens one-shot; replaying a session across contexts
 * trips theft detection and revokes every session for the user. See
 * authed-app.spec.ts for the full reasoning.
 *
 * DEPLOY GATE: this job runs against the DEPLOYED app (.gitlab-ci.yml,
 * authed-e2e → $MESH_PROD_APP_BASE_URL), and the /a/ route ships with this
 * very change — before the deploy lands, a navigation to /a/<id> bounces to
 * the workspace Activity page and every assertion below would chase a page
 * that isn't there. beforeAll probes the deployed app once; until
 * deploy:mesh-frontend ships the MR the suite skips LOUDLY, with this reason
 * in the job log — never silently (the #50420d50 lesson). The first pipeline
 * after the deploy (main, or any MR against it) finds the route and the
 * suite activates itself, which is exactly when its assertions mean
 * anything.
 */

test.describe.configure({ mode: "serial" });

const NOT_DEPLOYED_REASON =
  "artifact page /a/<id> is not on the deployed app yet — suite self-activates after deploy:mesh-frontend ships the route";

/** Set by the beforeAll probe; read by each test's skip guard. */
let routeDeployed = true;

const SANDBOX_WS_SLUG = "e2e-ci-sandbox";
const SANDBOX_PROJECT_SLUG = "e2e-fixtures";
const SCRATCH_PREFIX = "[e2e-artifact-view]";

let context: BrowserContext;
let page: Page;
let api: APIRequestContext;
let accessToken = "";
let projectId = "";
let wsSlug = "";
let projectSlug = "";
let taskId = "";
let csvArtifactId = "";
let jsonArtifactId = "";
let created = false;

const pageErrors: string[] = [];
const failedApiCalls: string[] = [];
/** Any request that leaves the app's own origin while opening an artifact. */
const foreignOriginRequests: string[] = [];

function authHeaders(): Record<string, string> {
  return { Authorization: `Bearer ${accessToken}` };
}

// Unique per run so concurrent CI runs never share a title.
const runTag = process.env.CI_JOB_ID
  ? `gl-${process.env.CI_JOB_ID}`
  : process.env.GITHUB_RUN_ID
    ? `gh-${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT ?? "1"}`
    : `local-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
const taskTitle = `${SCRATCH_PREFIX} route ${runTag}`;

async function uploadArtifact(
  name: string,
  mimeType: string,
  body: string
): Promise<string> {
  const res = await api.post(`/api/v1/tasks/${taskId}/artifacts`, {
    headers: authHeaders(),
    multipart: {
      file: { name, mimeType, buffer: Buffer.from(body, "utf-8") },
      name,
      artifact_type: "data",
    },
  });
  expect(res.status(), `uploading ${name} failed: ${await res.text()}`).toBe(201);
  const artifact = (await res.json()) as { id: string };
  return artifact.id;
}

test.beforeAll(async ({ browser }) => {
  // A 429 retry honours the server's Retry-After (up to 65s).
  test.setTimeout(120_000);

  context = await browser.newContext();

  const { accessToken: token } = await loginWithRetry(
    context.request,
    process.env.E2E_USER_EMAIL!,
    process.env.E2E_USER_PASSWORD!
  );
  accessToken = token;
  api = context.request;

  // The deploy probe (see header). A zero UUID cannot exist, so on a build
  // WITH the route the page stays on /a/ and shows its not-found state; on a
  // build WITHOUT it the app redirects to /w/<first-workspace>/activity.
  // Listener-free page: it must not pollute the foreign-origin accounting.
  {
    const probe = await context.newPage();
    await probe.goto("/a/00000000-0000-0000-0000-000000000000", {
      waitUntil: "domcontentloaded",
    });
    try {
      await probe.waitForURL(/\/w\/.+\/activity$/, { timeout: 4_000 });
      routeDeployed = false;
    } catch {
      routeDeployed = true;
    }
    await probe.close();
    if (!routeDeployed) return; // no fixtures for a run whose tests all skip
  }

  const wsRes = await api.get("/api/v1/workspaces", { headers: authHeaders() });
  expect(wsRes.status(), "/api/v1/workspaces must accept the session").toBe(200);
  const workspaces = (await wsRes.json()) as Array<{ id: string; slug: string }>;
  const workspace = workspaces.find((w) => w.slug === SANDBOX_WS_SLUG);
  expect(
    workspace,
    `the E2E credential must be a member of ${SANDBOX_WS_SLUG}`
  ).toBeTruthy();
  wsSlug = workspace!.slug;

  const projRes = await api.get(`/api/v1/workspaces/${workspace!.id}/projects`, {
    headers: authHeaders(),
  });
  expect(projRes.status(), "the sandbox project list must be readable").toBe(200);
  const projPayload = (await projRes.json()) as {
    items?: Array<{ id: string; slug: string }>;
  };
  const project = (projPayload.items ?? []).find(
    (p) => p.slug === SANDBOX_PROJECT_SLUG
  );
  expect(project, `project "${SANDBOX_PROJECT_SLUG}" must exist in the sandbox`).toBeTruthy();
  projectId = project!.id;
  projectSlug = project!.slug;

  const createRes = await api.post(`/api/v1/projects/${projectId}/tasks`, {
    headers: authHeaders(),
    data: { title: taskTitle },
  });
  expect(
    createRes.status(),
    `POST .../tasks failed: ${createRes.status()} ${await createRes.text()}`
  ).toBe(201);
  taskId = ((await createRes.json()) as { id: string }).id;
  created = true;

  csvArtifactId = await uploadArtifact(
    "e2e-costs.csv",
    "text/csv",
    'item,cost\n"laptop, used",100\nmouse,5\n'
  );
  // One line on purpose: the page's job is to pretty-print it.
  jsonArtifactId = await uploadArtifact(
    "e2e-result.json",
    "application/json",
    '{"model":"probe","cost":1.5,"ok":true}'
  );

  page = await context.newPage();
  page.on("pageerror", (err) => pageErrors.push(err.message));
  page.on("request", (req) => {
    const url = new URL(req.url());
    // The local dev stack presigns against MinIO on a neighbouring port —
    // same machine, not a third party. Anything else off-origin (a Team
    // Relay host, an analytics beacon) is exactly what this suite watches for.
    if (/^(localhost|127\.0\.0\.1)$/.test(url.hostname)) return;
    if (url.origin !== new URL(page.url()).origin) {
      foreignOriginRequests.push(req.url());
    }
  });
  page.on("response", (resp) => {
    const url = new URL(resp.url());
    if (!url.pathname.startsWith("/api/v1/")) return;
    if (resp.status() >= 400 && !url.pathname.endsWith("/auth/refresh")) {
      failedApiCalls.push(`${resp.status()} ${resp.request().method()} ${url.pathname}`);
    }
  });
});

test.afterAll(async () => {
  if (created && taskId) {
    const delRes = await api.delete(`/api/v1/tasks/${taskId}`, { headers: authHeaders() });
    console.log(`[cleanup] DELETE task ${taskId} -> ${delRes.status()}`);
    expect(delRes.ok(), `DELETE /api/v1/tasks/${taskId} failed`).toBe(true);
    const getRes = await api.get(`/api/v1/tasks/${taskId}`, { headers: authHeaders() });
    expect(getRes.status(), "the deleted task must 404 on a fresh GET").toBe(404);
  }
  await context?.close();
});

test("a CSV artifact renders as a table on its own /a/<id> page", async () => {
  test.skip(!routeDeployed, NOT_DEPLOYED_REASON);

  await page.goto(`/a/${csvArtifactId}`, { waitUntil: "domcontentloaded" });

  await expect(page.locator("table")).toBeVisible({ timeout: 15_000 });
  await expect(page.locator("thead th").first()).toHaveText("item");
  // The quoted comma survives as ONE cell, not two.
  await expect(page.locator("tbody td").first()).toHaveText("laptop, used");
  // The page is where the reader landed — not a redirect elsewhere.
  expect(new URL(page.url()).pathname).toBe(`/a/${csvArtifactId}`);
});

test("a JSON artifact is pretty-printed, not dumped as the stored line", async () => {
  test.skip(!routeDeployed, NOT_DEPLOYED_REASON);

  await page.goto(`/a/${jsonArtifactId}`, { waitUntil: "domcontentloaded" });

  const pre = page.locator("pre").first();
  await expect(pre).toBeVisible({ timeout: 15_000 });
  const text = await pre.textContent();
  expect(text, "every key starts its own indented line").toContain('\n  "model"');
  expect(text, "the stored single-line body must not be shown verbatim").not.toBe(
    '{"model":"probe","cost":1.5,"ok":true}'
  );
});

test("clicking Preview on the task page opens /a/<id> in a new tab", async () => {
  test.skip(!routeDeployed, NOT_DEPLOYED_REASON);

  await page.goto(`/w/${wsSlug}/p/${projectSlug}/t/${taskId}`, {
    waitUntil: "domcontentloaded",
  });
  await page.getByRole("button", { name: "Artifacts", exact: true }).click();

  const row = page.locator(`[data-artifact-id="${csvArtifactId}"]`);
  await expect(row).toBeVisible({ timeout: 15_000 });

  const popupPromise = page.waitForEvent("popup");
  await row.getByTitle("Preview").click();
  const popup = await popupPromise;

  await expect(popup.locator("table")).toBeVisible({ timeout: 15_000 });
  expect(new URL(popup.url()).pathname).toBe(`/a/${csvArtifactId}`);
  await popup.close();
});

test("the whole flow stayed on this origin and made no failing calls", async () => {
  test.skip(!routeDeployed, NOT_DEPLOYED_REASON);

  // The removed behaviour sent the reader to docs.entire.vc; any foreign
  // origin request during these scenarios is that class of bug returning.
  // (Presigned storage URLs share the app's origin through the /s3 proxy.)
  expect(foreignOriginRequests).toEqual([]);
  expect(failedApiCalls).toEqual([]);
  expect(pageErrors).toEqual([]);
});
