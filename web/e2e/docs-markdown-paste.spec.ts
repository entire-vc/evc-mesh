import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { loginWithRetry, pinUiLocale } from "./auth-helper";
import { rateLimitAware } from "./api-retry";

/**
 * E2E: Docs editor — pasting a whole Markdown file from the clipboard, and
 * replacing a large page with a short one.
 *
 * The text goes through the real system clipboard (navigator.clipboard, then
 * Ctrl/Cmd+V), so what is checked is the browser's own paste event reaching
 * the editor's markdown-paste plugin, not a synthetic event a unit test builds.
 * The plugin's rules are pinned by src/lib/milkdown/__tests__/markdown-paste.test.ts.
 *
 * What lands is read back three ways: the editor's blocks, the stored body
 * through the API, and the read-only view after a reload.
 *
 * Every test gets a fresh document; afterAll deletes them all.
 */

const RUN = `e2e-mdp-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

test.describe.serial("Docs editor — Markdown paste and an intended cut", () => {
  let page: Page;
  let api: APIRequestContext;
  let accessToken = "";
  let projectId = "";
  let docsPath = "";
  let imageUrl = "";
  const created: string[] = [];

  const auth = () => ({ Authorization: `Bearer ${accessToken}` });

  test.beforeAll(async ({ browser, baseURL }) => {
    test.setTimeout(120_000);
    const context = await browser.newContext();
    await pinUiLocale(context, "en");
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    ({ accessToken } = await loginWithRetry(
      context.request,
      process.env.E2E_USER_EMAIL!,
      process.env.E2E_USER_PASSWORD!,
    ));
    page = await context.newPage();
    api = rateLimitAware(context.request);

    const wsRes = await api.get("/api/v1/workspaces", { headers: auth() });
    expect(wsRes.ok(), `GET /api/v1/workspaces failed: ${wsRes.status()}`).toBeTruthy();
    const workspaces = (await wsRes.json()) as { id: string; slug: string }[];
    // Same choice as docs-editor-keys.spec.ts: CI's account sees its sandbox
    // workspace only; E2E_DOCS_WORKSPACE_SLUG names another for a local run.
    const want = process.env.E2E_DOCS_WORKSPACE_SLUG;
    const workspace = want ? workspaces.find((w) => w.slug === want) : workspaces[0];
    expect(workspace, "no workspace to run against").toBeTruthy();

    const projRes = await api.get(`/api/v1/workspaces/${workspace!.id}/projects`, {
      headers: auth(),
    });
    expect(projRes.ok(), `GET .../projects failed: ${projRes.status()}`).toBeTruthy();
    const projects = (await projRes.json()) as {
      items: { id: string; slug: string; is_archived: boolean }[];
    };
    const project = projects.items.find((p) => !p.is_archived) ?? projects.items[0];
    expect(project, "workspace has no project to create a test document in").toBeTruthy();
    projectId = project!.id;
    docsPath = `/w/${workspace!.slug}/p/${project!.slug}/docs`;

    // Served by the app under test itself, so loading it depends on nothing
    // outside the run.
    imageUrl = new URL("/icon-192.png", baseURL ?? "http://localhost:3007").toString();
  });

  test.afterAll(async () => {
    for (const id of created) {
      const res = await api.delete(`/api/v1/documents/${id}`, { headers: auth() });
      expect(res.ok(), `DELETE /api/v1/documents/${id} failed: ${res.status()}`).toBeTruthy();
    }
    await page?.context().close();
  });

  /** Creates a document, opens it and switches to the editor. Returns its id. */
  async function openForEditing(body: string): Promise<string> {
    const res = await api.post(`/api/v1/projects/${projectId}/documents`, {
      headers: auth(),
      data: { title: `E2E docs-markdown-paste ${RUN} ${created.length + 1}`, body },
    });
    expect(res.ok(), `POST .../documents failed: ${res.status()} ${await res.text()}`).toBeTruthy();
    const { id } = (await res.json()) as { id: string };
    created.push(id);
    await page.goto(`${docsPath}/${id}`, { waitUntil: "networkidle" });
    await expect(page.locator(".mesh-doc-prose").first()).toBeVisible();
    await page.getByRole("button", { name: "Edit", exact: true }).first().click();
    await expect(editor()).toBeVisible();
    return id;
  }

  const editor = () => page.locator('.mesh-doc-prose[contenteditable="true"]');
  const view = () => page.locator(".mesh-doc-prose").first();

  /** Select everything in the editor and paste `text` from the system clipboard. */
  async function replaceAllByPaste(text: string): Promise<void> {
    await page.evaluate((t) => navigator.clipboard.writeText(t), text);
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(text);
    await editor().click();
    await page.keyboard.press("ControlOrMeta+A");
    await page.keyboard.press("ControlOrMeta+V");
  }

  async function storedBody(id: string): Promise<{ body: string; version: number }> {
    const res = await api.get(`/api/v1/documents/${id}`, { headers: auth() });
    expect(res.ok(), `GET /api/v1/documents/${id} failed: ${res.status()}`).toBeTruthy();
    const doc = (await res.json()) as { body?: string; version: number };
    return { body: doc.body ?? "", version: doc.version };
  }

  const saved = () => page.getByText("Saved", { exact: true });

  test("a full plaintext .md pasted from the clipboard becomes structure, is saved, and reads the same after reload", async () => {
    const md = [
      "# Pasted heading one",
      "Intro with a [docs link](https://example.com/docs) inline.",
      "## Lists",
      "- bullet one\n- bullet two",
      "1. first item\n2. second item",
      "> quoted line",
      "```ts\nconst answer = 42;\n```",
      "| Col A | Col B |\n| --- | --- |\n| a1 | b1 |",
      `Inline image ![logo](${imageUrl}) in text.`,
    ].join("\n\n");

    const id = await openForEditing("Seed paragraph.\n");
    await replaceAllByPaste(md);

    // Structure in the editor, not literal `#`, `-`, `|` text.
    const structure = async (root: ReturnType<typeof view>) => {
      await expect(root.locator("h1")).toHaveText("Pasted heading one");
      await expect(root.locator("h2")).toHaveText("Lists");
      await expect(root.locator("ul > li")).toHaveText(["bullet one", "bullet two"]);
      await expect(root.locator("ol > li")).toHaveText(["first item", "second item"]);
      await expect(root.locator("blockquote")).toHaveText("quoted line");
      await expect(root.locator("pre")).toContainText("const answer = 42;");
      await expect(root.locator("table th")).toHaveText(["Col A", "Col B"]);
      await expect(root.locator("table td")).toHaveText(["a1", "b1"]);
      await expect(root.locator('a[href="https://example.com/docs"]')).toHaveText("docs link");
      // The image stays inside its line of text.
      await expect(
        root.locator("p", { hasText: "Inline image" }).locator(`img[src="${imageUrl}"]`),
      ).toHaveCount(1);
      await expect(root).not.toContainText("# Pasted");
      await expect(root).not.toContainText("| Col A");
    };
    await structure(editor());

    await expect(saved()).toBeVisible({ timeout: 15_000 });

    const { body } = await storedBody(id);
    expect(body).toMatch(/^# Pasted heading one$/m);
    expect(body).toMatch(/^## Lists$/m);
    expect(body).toMatch(/^[-*] bullet one$/m);
    expect(body).toMatch(/^1\. first item$/m);
    expect(body).toMatch(/^> quoted line$/m);
    expect(body).toContain("```ts\nconst answer = 42;\n```");
    expect(body).toMatch(/^\|\s*Col A\s*\|\s*Col B\s*\|$/m);
    expect(body).toContain("[docs link](https://example.com/docs)");
    expect(body).toContain(`![logo](${imageUrl})`);
    expect(body).not.toContain("Seed paragraph.");

    await page.reload({ waitUntil: "networkidle" });
    await expect(page.locator('.mesh-doc-prose[contenteditable="true"]')).toHaveCount(0);
    await structure(view());
    // The reload wrote nothing back: the server still holds the pasted body.
    expect((await storedBody(id)).body, "stored body after reload").toBe(body);
  });

  const BIG = (() => {
    const para = (n: number) =>
      `Paragraph ${n} of a long page. ${"It carries enough text to make the page large. ".repeat(4)}`;
    return [
      "# Large page",
      para(1),
      "## Section alpha",
      para(2),
      para(3),
      "### Detail alpha",
      para(4),
      "## Section beta",
      para(5),
      para(6),
      "### Detail beta",
      para(7),
      "## Section gamma",
      para(8),
      para(9),
    ].join("\n\n");
  })();
  const SHORT = "# Positive save\n\nFirst stable edit.";

  test("a writer outside the editor still cannot cut a large page without allow_shrink", async () => {
    const res = await api.post(`/api/v1/projects/${projectId}/documents`, {
      headers: auth(),
      data: { title: `E2E docs-markdown-paste ${RUN} guard`, body: BIG },
    });
    expect(res.ok()).toBeTruthy();
    const { id, version } = (await res.json()) as { id: string; version: number };
    created.push(id);

    const refused = await api.patch(`/api/v1/documents/${id}`, {
      headers: auth(),
      data: { body: SHORT, base_version: version },
    });
    expect(refused.status(), "the shrink guard still stops an unconfirmed API cut").toBe(409);
    expect(((await refused.json()) as { message: string }).message).toContain("allow_shrink");
    expect((await storedBody(id)).body.length, "nothing was written").toBeGreaterThan(2000);
  });

  test("replacing a large page with a short pasted one is saved after one confirmation, and stays after reload", async () => {
    expect(BIG.length, "the fixture is large enough for the shrink guard").toBeGreaterThan(2000);
    const id = await openForEditing(BIG);
    const before = await storedBody(id);

    await replaceAllByPaste(SHORT);
    await expect(editor().locator("h1")).toHaveText("Positive save");
    await expect(editor()).not.toContainText("Section alpha");

    // The autosave is refused by the shrink guard: said as what it is, with the
    // one action that helps instead of a Retry that is refused again.
    await expect(page.getByText("Not saved: this cuts most of the page")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole("button", { name: "Retry" })).toHaveCount(0);
    expect((await storedBody(id)).body, "nothing written before the confirmation").toBe(before.body);

    await page.getByRole("button", { name: "Save anyway" }).click();
    await expect(saved()).toBeVisible({ timeout: 15_000 });

    const after = await storedBody(id);
    expect(after.version).toBe(before.version + 1);
    expect(after.body.trim()).toBe(SHORT);

    // Done, then a reload: the short page is what the server serves and shows.
    await page.getByRole("button", { name: "Done", exact: true }).click();
    await page.reload({ waitUntil: "networkidle" });
    await expect(view().locator("h1")).toHaveText("Positive save");
    await expect(view()).toContainText("First stable edit.");
    await expect(view()).not.toContainText("Section alpha");
    await expect(page.getByText(/Not saved/)).toHaveCount(0);
    const reloaded = await storedBody(id);
    expect(reloaded.body.trim(), "stored body after reload").toBe(SHORT);
    expect(reloaded.version, "the reload wrote nothing back").toBe(after.version);
  });
});
