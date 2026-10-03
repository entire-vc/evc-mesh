import { test, expect } from "@playwright/test";
import { writeFileSync } from "node:fs";

// This suite writes only to scripts/local-stack.sh's disposable database.
// Teardown the stand after the run; fixtures also support visual comparison.
test("agent tags survive real API writes and preserve assignment/mention identity at both widths", async ({ page, context }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const login = await context.request.post("/api/v1/auth/login", { data: {
    email: "local-stack@example.test", password: "LocalStack1", // pragma: allowlist secret
  } });
  expect(login.status()).toBe(200);
  const auth = { Authorization: `Bearer ${(await login.json()).tokens.access_token}` };
  const workspace = await context.request.post("/api/v1/workspaces", { headers: auth,
    data: { name: `Agent label check ${Date.now()}` },
  });
  expect(workspace.status()).toBe(201);
  const ws = await workspace.json();
  const createdProject = await context.request.post(`/api/v1/workspaces/${ws.id}/projects`, { headers: auth,
    data: { name: "Local Stack Demo" },
  });
  expect(createdProject.status()).toBe(201);
  const project = await createdProject.json();
  const createdDoc = await context.request.post(`/api/v1/projects/${project.id}/documents`, { headers: auth,
    data: { title: "Agent mentions", body: "# Agent mentions\n\nComment below to mention a teammate." },
  });
  expect(createdDoc.status()).toBe(201);
  const doc = await createdDoc.json();

  async function createAgent(name: string) {
    const response = await context.request.post(`/api/v1/workspaces/${ws.id}/agents`, { headers: auth,
      data: { name, agent_type: "codex", role: "long role that must not become a tag" } });
    expect(response.status()).toBe(201);
    const a = (await response.json()).agent;
    return { id: a.id as string, name: a.name as string, slug: a.slug as string };
  }
  const tagged = await createAgent("Bender");
  const empty = await createAgent("Test agent");
  for (const a of [tagged, empty]) {
    const response = await context.request.post(`/api/v1/projects/${project.id}/members/agents`, {
      headers: auth, data: { agent_id: a.id, role: "member" },
    });
    expect(response.status()).toBe(201);
  }
  const orgURL = `/w/${ws.slug}/org-chart`;
  await page.goto(orgURL);
  await page.getByTitle("Bender", { exact: true }).click();
  const tagInput = page.getByRole("textbox", { name: "Short tag" });
  await expect(tagInput).toHaveAttribute("maxlength", "24");
  await expect(tagInput).toHaveValue("");
  await tagInput.fill("keep-dev");
  const saved = page.waitForResponse((r) => r.url().endsWith(`/agents/${tagged.id}`) && r.request().method() === "PATCH");
  await tagInput.press("Enter");
  expect((await saved).status()).toBe(200);
  const profile = await context.request.get(`/api/v1/agents/${tagged.id}`, { headers: auth });
  expect(await profile.json()).toMatchObject({ ...tagged, short_tag: "keep-dev" });
  await page.keyboard.press("Escape");
  await expect(page.getByTitle("Bender · keep-dev", { exact: true })).toBeVisible();
  await expect(page.getByTitle("Test agent", { exact: true })).toHaveText("Test agent");

  const created = await context.request.post(`/api/v1/projects/${project.id}/tasks`, { headers: auth,
    data: { title: "Task with agent label", assignee_id: tagged.id, assignee_type: "agent" },
  });
  expect(created.status()).toBe(201);
  const task = await created.json();
  const boardURL = `/w/${ws.slug}/p/${project.slug}`;
  const detailURL = `${boardURL}/t/${task.id}`;

  for (const width of [1440, 393]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(orgURL + "/grid");
    const name = page.getByTitle("Bender · keep-dev", { exact: true });
    await expect(name).toBeVisible();
    await expect(name.locator("span")).toHaveClass(/text-muted-foreground/);
    await expect(name.locator("span")).toHaveCSS("font-weight", "400");
    await expect(page.getByTitle("Test agent", { exact: true })).toHaveText("Test agent");
    await name.click();
    const editorTitle = page.getByRole("heading", { level: 2 }).filter({ has: page.getByRole("textbox", { name: "Short tag" }) });
    await expect(editorTitle.locator("span[title]")).toHaveText("Bender");
    await expect(editorTitle).not.toContainText("keep-dev");
    await expect(tagInput).toHaveValue("keep-dev");
    const nameFits = await editorTitle.locator("span[title]").evaluate((el) => el.scrollWidth <= el.clientWidth);
    expect(nameFits).toBe(true);
    const inputBox = await tagInput.boundingBox();
    const closeBox = await editorTitle.locator("../..").locator("button.absolute").boundingBox();
    expect(inputBox).not.toBeNull();
    expect(closeBox).not.toBeNull();
    expect(inputBox!.x + inputBox!.width).toBeLessThanOrEqual(closeBox!.x - 8);
    console.log(`width=${width}: editor name only, untruncated; tag input/close gap=${Math.round(closeBox!.x - inputBox!.x - inputBox!.width)}px PASS`);
    await page.keyboard.press("Escape");
    await page.goto(boardURL);
    await expect(page.getByTitle("Bender · keep-dev", { exact: true })).toHaveText("BE");
    await expect(page.getByText("keep-dev", { exact: true })).toHaveCount(0);
    await page.getByText("Task with agent label", { exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/t/${task.id}$`));
    const selects = page.locator("select:visible").filter({ has: page.locator(`option[value="agent:${tagged.id}"]`) });
    await expect(selects).toHaveCount(2); // assignee and reviewer
    await expect(selects.first().locator(`option[value="agent:${tagged.id}"]`)).toHaveText("Bender · keep-dev");
    await expect(selects.first().locator(`option[value="agent:${empty.id}"]`)).not.toContainText(" · ");
    await selects.first().selectOption(`agent:${empty.id}`);
    await expect(selects.first()).toHaveValue(`agent:${empty.id}`);
    await selects.first().selectOption(`agent:${tagged.id}`);
    await expect(selects.first()).toHaveValue(`agent:${tagged.id}`);
    console.log(`width=${width}: org tagged/empty, board tooltip/initials, fullscreen detail, assignee/reviewer values PASS`);
  }
  await page.goto(`${boardURL}/docs/${doc.id}`);
  const composer = page.getByPlaceholder("Add a comment… @ to mention");
  await composer.fill("Hi @ben");
  const mention = page.getByRole("option").filter({ hasText: "Bender · keep-dev" });
  await expect(mention).toBeVisible();
  await composer.press("Enter");
  await expect(composer).toHaveValue(`Hi @${tagged.slug} `);
  expect(errors).toEqual([]);
  console.log("mention inserts slug only; saved name/slug unchanged; pageerrors=0 PASS");
  if (process.env.LOCAL_STACK_VISUAL_FIXTURES) writeFileSync(process.env.LOCAL_STACK_VISUAL_FIXTURES,
    JSON.stringify({ orgURL, boardURL, detailURL, docURL: `${boardURL}/docs/${doc.id}`, agentURL: `/w/${ws.slug}/team/agent/${tagged.slug}`, tagged, empty, taskId: task.id }, null, 2));
});
