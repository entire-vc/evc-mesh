import { test, expect } from "@playwright/test";

test.use({ channel: process.env.LOCAL_STACK_BROWSER_CHANNEL });

// Real HTTP/storage on scripts/local-stack.sh's disposable server only.
test("dependencies load on open and stay consistent after real API mutations at both widths", async ({ page, context }) => {
  const login = await context.request.post("/api/v1/auth/login", { data: {
    email: "local-stack@example.test", password: "LocalStack1", // pragma: allowlist secret
  } });
  expect(login.status()).toBe(200);
  const { tokens, user } = await login.json();
  const headers = { Authorization: `Bearer ${tokens.access_token}` };
  async function post(path: string, data: unknown) {
    const r = await context.request.post(path, { headers, data });
    expect(r.status()).toBe(201);
    return r.json();
  }
  const ws = await post("/api/v1/workspaces", { name: `Dependencies ${Date.now()}` });
  const project = await post(`/api/v1/workspaces/${ws.id}/projects`, { name: "Current project" });
  const cross = await post(`/api/v1/workspaces/${ws.id}/projects`, { name: "Other project" });
  const statuses = await (await context.request.get(`/api/v1/projects/${cross.id}/statuses`, { headers })).json();
  const current = await post(`/api/v1/projects/${project.id}/tasks`, { title: "Dependency source acceptance" });
  const blocker = await post(`/api/v1/projects/${cross.id}/tasks`, {
    title: "Cross-project prerequisite with a long title that wraps on small screens",
    status_id: statuses.find((s: { category: string }) => s.category === "in_progress").id,
    assignee_id: user.id, assignee_type: "user",
  });
  const finished = await post(`/api/v1/projects/${cross.id}/tasks`, {
    title: "Finished prerequisite", status_id: statuses.find((s: { category: string }) => s.category === "done").id,
  });
  const related = await post(`/api/v1/projects/${project.id}/tasks`, { title: "New reference" });
  for (const task of [blocker, finished]) await post(`/api/v1/tasks/${current.id}/dependencies`, { depends_on_task_id: task.id, dependency_type: "blocks" });
  const requests: string[] = [];
  page.on("request", r => requests.push(new URL(r.url()).pathname));
  const path = `/w/${ws.slug}/p/${project.slug}/t/${current.id}`;
  for (const width of [1440, 393]) {
    await page.setViewportSize({ width, height: 900 });
    requests.length = 0;
    await page.goto(path);
    const tab = page.getByRole("button", { name: /^Dependencies (Loading|Unavailable|\d+ ·)/ }).filter({ visible: true }).first();
    await expect(tab).toHaveText(/^Dependencies\s*2 · 1 open$/);
    // Vite StrictMode replays the opening effect; neither tab mount nor badge may fetch again.
    const eagerLoads = requests.filter(p => p === `/api/v1/tasks/${current.id}/dependencies`).length;
    expect(eagerLoads).toBeGreaterThan(0);
    await tab.click();
    const blockerRow = page.getByRole("button", { name: new RegExp(blocker.title) });
    await expect(blockerRow).toBeVisible();
    expect(requests.filter(p => p === `/api/v1/tasks/${current.id}/dependencies`)).toHaveLength(eagerLoads);
    await expect(page.getByText("Open blocker", { exact: true })).toHaveCount(1);
    await expect(blockerRow.locator(".." )).toContainText("In Progress");
    await expect(blockerRow.locator(".." )).toContainText("Local Stack");
    await page.getByRole("button", { name: "Add", exact: true }).click();
    await page.getByPlaceholder(/550e8400/).fill(finished.id);
    await page.getByRole("combobox", { name: "Relationship type" }).selectOption("relates_to");
    const rejected = page.waitForResponse(r => r.url().endsWith(`/tasks/${current.id}/dependencies`) && r.request().method() === "POST");
    await page.getByRole("button", { name: "Add Dependency" }).click();
    expect((await rejected).status()).toBe(409);
    await expect(page.getByText("Failed to add dependency.", { exact: true })).toBeVisible();
    await expect(tab).toHaveText(/^Dependencies\s*2 · 1 open$/);
    await page.getByPlaceholder(/550e8400/).fill(related.id);
    await page.getByRole("combobox", { name: "Relationship type" }).selectOption("relates_to");
    await page.getByRole("button", { name: "Add Dependency" }).click();
    await expect(tab).toHaveText(/^Dependencies\s*3 · 1 open$/);
    const depResponse = await context.request.get(`/api/v1/tasks/${current.id}/dependencies`, { headers });
    const edges = await depResponse.json();
    expect(edges.outgoing).toHaveLength(3);
    // Delete just the reference, leaving both prerequisite edges intact.
    const reference = page.getByText("Relates to", { exact: true }).locator("..");
    await reference.getByRole("button", { name: "Remove dependency" }).click();
    await expect(page.getByText("Remove link?", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(tab).toHaveText(/^Dependencies\s*3 · 1 open$/);
    await reference.getByRole("button", { name: "Remove dependency" }).click();
    await page.getByRole("button", { name: "Remove", exact: true }).click();
    await expect(tab).toHaveText(/^Dependencies\s*2 · 1 open$/);
    await blockerRow.click();
    await expect(page.getByRole("heading", { name: blocker.title, exact: true })).toBeVisible();
    console.log(`${width}px: eager GET, own-project status/assignee, done excluded, add/remove badge, navigation PASS`);
  }
  const empty = await post(`/api/v1/projects/${project.id}/tasks`, { title: "Empty dependencies" });
  await page.goto(`/w/${ws.slug}/p/${project.slug}/t/${empty.id}`);
  const emptyTab = page.getByRole("button", { name: /^Dependencies (Loading|Unavailable|\d+ ·)/ });
  await emptyTab.click();
  await expect(page.getByText("No dependencies yet.")).toBeVisible();
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await page.getByLabel("Task ID (UUID)").fill(related.id);
  await page.getByRole("combobox", { name: "Relationship type" }).selectOption("relates_to");
  await page.getByRole("button", { name: "Add Dependency" }).click();
  await expect(emptyTab).toHaveText(/^Dependencies\s*1 · 0 open$/);
  console.log("393px: empty tab + real API add PASS");
});
