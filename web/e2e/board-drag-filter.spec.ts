import {
  test,
  expect,
  type APIRequestContext,
  type BrowserContext,
  type Page,
} from "@playwright/test";
import { loginWithRetry } from "./auth-helper";

/**
 * E2E scenario: board-drag-filter
 * Scenario doc: web/e2e-scenarios/board-drag-filter.md
 *
 * What this suite proves:
 *   1. dragging a card to another column changes its status SERVER-SIDE, not
 *      just its position in the DOM (§1n — a repaint is not a move);
 *   2. the toolbar search filter actually narrows the visible card set to the
 *      matching task, and restores it when cleared.
 *
 * Own login, own browser context — not shared with authed-app.spec.ts or any
 * other spec file. Mesh rotates refresh tokens one-shot; replaying a session
 * across contexts trips theft detection and revokes every session for the
 * user (internal/auth/service.go, ErrTokenReused). See task-board-load.md.
 */

test.describe.configure({ mode: "serial" });

const SANDBOX_WS_SLUG = "e2e-ci-sandbox";
const SANDBOX_PROJECT_SLUG = "e2e-fixtures";
const SCRATCH_PREFIX = "[e2e-board]";

let context: BrowserContext;
let page: Page;
let api: APIRequestContext;
let accessToken = "";
let projectId = "";
let wsSlug = "";
let projectSlug = "";

let dragTaskId = "";
let created = false;
let sourceStatusId = "";
let targetStatusId = "";

const consoleErrors: string[] = [];
const pageErrors: string[] = [];
const failedApiCalls: string[] = [];

function authHeaders(): Record<string, string> {
  return { Authorization: `Bearer ${accessToken}` };
}

// Unique per run so concurrent CI runs (two PRs, or a GitLab + GitHub mirror
// run in flight at once) never share a title or a search hit.
const runTag = process.env.CI_JOB_ID
  ? `gl-${process.env.CI_JOB_ID}`
  : process.env.GITHUB_RUN_ID
    ? `gh-${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT ?? "1"}`
    : `local-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
const dragTaskTitle = `${SCRATCH_PREFIX} drag+filter ${runTag}`;

const boardUrl = () => `/w/${wsSlug}/p/${projectSlug}`;

function taskCard(taskId: string) {
  return page.locator(`[data-testid="task-card"][data-task-id="${taskId}"]`);
}

function columnTaskLocator(columnStatusId: string, taskId: string) {
  return page.locator(
    `[data-testid="board-column"][data-status-id="${columnStatusId}"] [data-testid="task-card"][data-task-id="${taskId}"]`
  );
}

test.beforeAll(async ({ browser }) => {
  // A 429 retry honours the server's Retry-After (up to 65s) — give the hook
  // room for that plus the rest of its own setup work.
  test.setTimeout(120_000);

  context = await browser.newContext();

  const { accessToken: token } = await loginWithRetry(
    context.request,
    process.env.E2E_USER_EMAIL!,
    process.env.E2E_USER_PASSWORD!
  );
  accessToken = token;
  api = context.request;

  const wsRes = await api.get("/api/v1/workspaces", { headers: authHeaders() });
  expect(wsRes.status(), "/api/v1/workspaces must accept the session").toBe(200);
  const workspaces = (await wsRes.json()) as Array<{ id: string; slug: string }>;
  const workspace = workspaces.find((w) => w.slug === SANDBOX_WS_SLUG);
  expect(
    workspace,
    `the E2E credential must be a member of ${SANDBOX_WS_SLUG} — see task-board-load.md`
  ).toBeTruthy();
  wsSlug = workspace!.slug;

  const projRes = await api.get(`/api/v1/workspaces/${workspace!.id}/projects`, {
    headers: authHeaders(),
  });
  expect(projRes.status(), "the sandbox project list must be readable").toBe(200);
  const projPayload = (await projRes.json()) as {
    items?: Array<{ id: string; slug: string }>;
  };
  const project = (projPayload.items ?? []).find((p) => p.slug === SANDBOX_PROJECT_SLUG);
  expect(project, `project "${SANDBOX_PROJECT_SLUG}" must exist in the sandbox`).toBeTruthy();
  projectId = project!.id;
  projectSlug = project!.slug;

  // Read the project's statuses live — the drag needs two non-closed columns
  // to mean anything, and this must not be assumed from a fixture file.
  const statusesRes = await api.get(`/api/v1/projects/${projectId}/statuses`, {
    headers: authHeaders(),
  });
  expect(statusesRes.status(), "reading project statuses must succeed").toBe(200);
  const statusesBody = (await statusesRes.json()) as
    | Array<{ id: string; position: number; category: string }>
    | { items: Array<{ id: string; position: number; category: string }> };
  const allStatuses = Array.isArray(statusesBody) ? statusesBody : statusesBody.items;
  const visibleStatuses = allStatuses
    .filter((s) => s.category !== "done" && s.category !== "cancelled")
    .sort((a, b) => a.position - b.position);
  expect(
    visibleStatuses.length,
    "board-drag-filter needs at least two non-closed statuses on the sandbox project"
  ).toBeGreaterThanOrEqual(2);
  sourceStatusId = visibleStatuses[0]!.id;
  targetStatusId = visibleStatuses[1]!.id;

  const createRes = await api.post(`/api/v1/projects/${projectId}/tasks`, {
    headers: authHeaders(),
    data: { title: dragTaskTitle, status_id: sourceStatusId },
  });
  expect(
    createRes.status(),
    `POST .../tasks failed: ${createRes.status()} ${await createRes.text()}`
  ).toBe(201);
  const task = (await createRes.json()) as { id: string };
  dragTaskId = task.id;
  created = true;

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
  page.on("response", (resp) => {
    const url = new URL(resp.url());
    if (!url.pathname.startsWith("/api/v1/")) return;
    if (resp.status() >= 400 && !url.pathname.endsWith("/auth/refresh")) {
      failedApiCalls.push(`${resp.status()} ${resp.request().method()} ${url.pathname}`);
    }
  });
});

test.afterAll(async () => {
  if (created && dragTaskId) {
    const delRes = await api.delete(`/api/v1/tasks/${dragTaskId}`, { headers: authHeaders() });
    console.log(`[cleanup] DELETE task ${dragTaskId} -> ${delRes.status()}`);
    expect(delRes.ok(), `DELETE /api/v1/tasks/${dragTaskId} failed: ${delRes.status()}`).toBe(
      true
    );

    // Deep-verify the cleanup: a 204 without the row actually gone would
    // otherwise report false success (docs-write-path.md closes the same gap
    // for documents).
    const getRes = await api.get(`/api/v1/tasks/${dragTaskId}`, { headers: authHeaders() });
    expect(getRes.status(), "the deleted task must 404 on a fresh GET").toBe(404);
  }
  await context?.close();
});

test("dragging a card to another column moves it server-side, not just in the DOM", async () => {
  await page.goto(boardUrl(), { waitUntil: "networkidle" });
  await expect(taskCard(dragTaskId)).toBeVisible({ timeout: 15_000 });
  await expect(columnTaskLocator(sourceStatusId, dragTaskId)).toHaveCount(1);

  const cardBox = await taskCard(dragTaskId).boundingBox();
  expect(cardBox, "drag card has no bounding box").toBeTruthy();

  const targetColumnBox = await page.evaluate((statusId) => {
    const col = document.querySelector(`[data-testid="board-column"][data-status-id="${statusId}"]`);
    if (!col) return null;
    const r = col.getBoundingClientRect();
    return { x: r.x, y: r.y, width: r.width, height: r.height };
  }, targetStatusId);
  expect(targetColumnBox, "target column is not visible on the board").toBeTruthy();

  const startX = cardBox!.x + cardBox!.width / 2;
  const startY = cardBox!.y + cardBox!.height / 2;
  const endX = targetColumnBox!.x + targetColumnBox!.width / 2;
  const endY = targetColumnBox!.y + 30;

  // Same technique web/perf/counters.spec.ts's board.drag measurement already
  // proved reliable against this page's dnd-kit PointerSensor/closestCorners:
  // move past the 5px activationConstraint, then several intermediate steps
  // so collision detection sees the pointer pass over the target column
  // rather than jumping straight from card to endpoint.
  await page.mouse.move(startX, startY);
  await page.mouse.down();
  await page.mouse.move(startX + 10, startY, { steps: 2 });
  await page.mouse.move(endX, startY, { steps: 10 });
  await page.mouse.move(endX, endY, { steps: 10 });
  await page.mouse.up();

  // DOM: gone from the source column, present exactly once in the target.
  await expect(columnTaskLocator(sourceStatusId, dragTaskId)).toHaveCount(0);
  await expect(columnTaskLocator(targetStatusId, dragTaskId)).toHaveCount(1);

  // SERVER: a fresh, independent GET — not the store's cached copy — must
  // report the TARGET status id exactly. "Not the source id" alone would
  // also pass for a drop into a third, unintended column.
  const persisted = await api.get(`/api/v1/tasks/${dragTaskId}`, { headers: authHeaders() });
  expect(persisted.status(), "reading the dragged task back must succeed").toBe(200);
  const persistedTask = (await persisted.json()) as { status_id: string };
  expect(
    persistedTask.status_id,
    "the drag must persist server-side to the TARGET status, not just repaint the column"
  ).toBe(targetStatusId);

  expect(failedApiCalls, "the drag made API calls that failed").toEqual([]);
  expect(pageErrors, "uncaught page errors during the drag").toEqual([]);
});

test("the toolbar search filter narrows to the matching card and back", async () => {
  const cardCountBefore = await page.locator('[data-testid="task-card"]').count();
  expect(
    cardCountBefore,
    "the board must show more than one card before filtering, or a search that returns everything could pass by accident"
  ).toBeGreaterThan(1);
  await expect(taskCard(dragTaskId)).toBeVisible();

  // The board toolbar's search box shares its placeholder with the global
  // header search (web/src/components/layout/header.tsx) — a cmdk-style
  // combobox. Disambiguate by role: the toolbar's is a plain shadcn <Input>
  // (implicit role=textbox), the header's is role=combobox.
  const searchBox = page.getByRole("textbox", { name: "Search tasks..." });
  await searchBox.fill(runTag);

  // BEHAVIOR assert: exactly one card left, and it is the right one — a
  // search box that (wrongly) always narrowed to "the first card" would
  // still pass a bare count-of-1 assertion, so name the id too.
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(1, { timeout: 10_000 });
  await expect(taskCard(dragTaskId)).toBeVisible();

  await searchBox.fill("");

  // Reversible: clearing the filter restores the exact pre-filter count, not
  // just "more than one" (which a filter stuck in a different broken state
  // could also satisfy).
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(cardCountBefore, {
    timeout: 10_000,
  });

  expect(failedApiCalls, "filtering made API calls that failed").toEqual([]);
  expect(pageErrors, "uncaught page errors while filtering").toEqual([]);

  const consoleErrs = await page.evaluate(
    () => (window as unknown as Record<string, string[]>).__ceErrs ?? []
  );
  consoleErrors.push(...consoleErrs);
  expect(consoleErrors, "console.error calls detected (F1)").toEqual([]);

  const http5xx = await page.evaluate(() =>
    performance
      .getEntriesByType("resource")
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      .filter((r: any) => r.responseStatus >= 500)
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      .map((r: any) => `${r.responseStatus} ${r.name}`)
  );
  expect(http5xx, "HTTP 5xx responses detected (F1)").toEqual([]);
});
