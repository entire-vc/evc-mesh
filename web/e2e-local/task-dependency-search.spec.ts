import { test, expect } from "@playwright/test";
import { writeFileSync } from "node:fs";

test.use({ channel: process.env.LOCAL_STACK_BROWSER_CHANNEL });
test("search selection, every direction, confirmation and both endpoint GETs on real authenticated API", async ({ page, context }) => {
  test.setTimeout(240_000);
  const login = await context.request.post("/api/v1/auth/login", { data: {
    email: "local-stack@example.test", password: "LocalStack1", // pragma: allowlist secret
  } });
  expect(login.status()).toBe(200);
  const headers = { Authorization: `Bearer ${(await login.json()).tokens.access_token}` };
  async function post(path: string, data: unknown) {
    const response = await context.request.post(path, { headers, data });
    expect(response.status()).toBe(201); return response.json();
  }
  const ws = await post("/api/v1/workspaces", { name: `Dependency search ${Date.now()}` });
  const project = await post(`/api/v1/workspaces/${ws.id}/projects`, { name: "Dependency source" });
  const cross = await post(`/api/v1/workspaces/${ws.id}/projects`, { name: "Other project" });
  const a = await post(`/api/v1/projects/${project.id}/tasks`, { title: "Safe dependency search source" });
  const b = await post(`/api/v1/projects/${cross.id}/tasks`, { title: "Safe dependency search target with a long title that wraps on mobile" });
  const path = `/w/${ws.slug}/p/${project.slug}/t/${a.id}`;
  const slidePath = `/e2e-local/dependency-preview.html?task=${a.id}&workspace=${ws.id}&project=${project.slug}`;
  writeFileSync("../evidence/da428666/fixture.json", JSON.stringify({ ws, project, cross, a, b, path, slidePath }, null, 2));
  async function deps(id: string) {
    const response = await context.request.get(`/api/v1/tasks/${id}/dependencies`, { headers });
    expect(response.status()).toBe(200); return response.json();
  }
  for (const view of ["full", "slide"]) for (const width of [1440, 393]) for (const scheme of ["light", "dark"] as const) {
    await page.setViewportSize({ width, height: width === 393 ? 852 : 900 }); await page.emulateMedia({ colorScheme: scheme });
    await page.goto(view === "full" ? path : slidePath);
    const tab = page.getByRole("button", { name: /^Dependencies\s/ }).filter({ visible: true }).first();
    await expect(tab).toHaveText(/^Dependencies\s*0 · 0 open$/); await tab.click();
    await expect(page.getByText("No dependencies yet.")).toBeVisible();
    for (const [kind, query] of [["depends_on", "Safe dependency search target"], ["blocks", `#${b.id.slice(0, 8)}`], ["related", b.id]]) {
      await page.getByRole("button", { name: "Add", exact: true }).click();
      await page.getByRole("combobox", { name: "Find task" }).fill(query);
      const option = page.getByRole("option", { name: new RegExp(b.title) });
      await expect(option).toContainText("Other project");
      if (kind === "depends_on") {
        await page.getByRole("combobox", { name: "Find task" }).press("ArrowDown");
        await page.getByRole("combobox", { name: "Find task" }).press("Enter");
      } else await option.click();
      await page.getByRole("combobox", { name: "Relationship type" }).selectOption(kind);
      const changed = page.waitForResponse(r => r.url().endsWith("/dependencies") && r.request().method() === "POST");
      await page.getByRole("button", { name: "Add Dependency" }).click(); expect((await changed).status()).toBe(201);
      await expect(tab).toHaveText(kind === "depends_on" ? /^Dependencies\s*1 · 1 open$/ : /^Dependencies\s*1 · 0 open$/);
      const owner = kind === "blocks" ? b.id : a.id;
      const target = kind === "blocks" ? a.id : b.id;
      const type = kind === "related" ? "relates_to" : "blocks";
      const [left, right] = await Promise.all([deps(a.id), deps(b.id)]);
      const edge = [...left.outgoing, ...left.incoming][0];
      expect(edge).toMatchObject({ task_id: owner, depends_on_task_id: target, dependency_type: type });
      expect([...right.outgoing, ...right.incoming].map(e => e.id)).toEqual([edge.id]);
      // A row opens the second task within the same panel; its list is fresh.
      await page.getByRole("button", { name: new RegExp(`#${b.id.slice(0, 8)} ${b.title}`) }).click();
      const reverseTab = page.getByRole("button", { name: /^Dependencies\s/ }).filter({ visible: true }).first();
      await reverseTab.click(); await expect(reverseTab).toHaveText(kind === "blocks" ? /^Dependencies\s*1 · 1 open$/ : /^Dependencies\s*1 · 0 open$/);
      await page.getByRole("button", { name: "Remove dependency" }).click();
      await page.getByRole("button", { name: "Cancel", exact: true }).click();
      expect([...((await deps(a.id)).outgoing), ...((await deps(a.id)).incoming)].map(e => e.id)).toEqual([edge.id]);
      await page.getByRole("button", { name: "Remove dependency" }).click();
      const removed = page.waitForResponse(r => r.request().method() === "DELETE" && r.url().endsWith(`/tasks/${owner}/dependencies/${edge.id}`));
      await page.getByRole("button", { name: "Remove", exact: true }).click(); expect((await removed).status()).toBe(204);
      await expect(reverseTab).toHaveText(/^Dependencies\s*0 · 0 open$/);
      for (const id of [a.id, b.id]) expect(await deps(id)).toMatchObject({ outgoing: [], incoming: [] });
      await page.goto(view === "full" ? path : slidePath); await tab.click();
      console.log(`${view}/${width}/${scheme}/${kind}: title-or-ID click, exact IDs/type/direction, reverse UI, cancel+owner DELETE, both GET empty PASS`);
    }
  }
  console.log(`SAFE_PAIR=${a.id},${b.id}; no working edges changed; rollback DELETE owner/edge`);
});
