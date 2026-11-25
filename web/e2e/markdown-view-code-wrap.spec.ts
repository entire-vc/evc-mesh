import { test, expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { loginWithRetry, pinUiLocale } from "./auth-helper";
import { rateLimitAware } from "./api-retry";

/**
 * E2E: a long fenced-code line wraps in a saved task description and a saved
 * comment, the way it already wraps in a document and in both editors.
 *
 * The read view of a description or a comment is MarkdownView, which renders
 * the document schema with no .ProseMirror root around it — so ProseMirror's
 * own `.ProseMirror pre { white-space: pre-wrap }` never reached it, and a
 * 600-character line was cut at 393px behind a sideways scroll inside the
 * block (QA measured client/scroll 303/4602 on the description, 311/4602 on a
 * comment). jsdom applies no stylesheet, so only a real browser on a real
 * saved task can show it; the CSS contract itself is pinned by
 * src/components/__tests__/markdown-view-content.test.tsx.
 *
 * The task is created through the API in the sandbox workspace and deleted in
 * afterAll.
 */

const SANDBOX_WS_SLUG = "e2e-ci-sandbox";
const SANDBOX_PROJECT_SLUG = "e2e-fixtures";
// Screenshots only on request (CODE_WRAP_SHOTS=<dir>); the assertions are the check.
const SHOTS = process.env.CODE_WRAP_SHOTS;
const RUN = `e2e-cw-${Date.now().toString(36)}`;

// The exact shape of the QA fixture: one line, a 16-character token × 40, no
// space anywhere — only `overflow-wrap: anywhere` can break it.
const DESC_CODE = "const_long_code_".repeat(40);
const COMMENT_CODE = "cmnt_long_code__".repeat(40);

test.describe.serial("Fenced code wraps in a saved description and comment", () => {
  let page: Page;
  let api: APIRequestContext;
  let accessToken = "";
  let taskId = "";
  const auth = () => ({ Authorization: `Bearer ${accessToken}` });

  test.beforeAll(async ({ browser }) => {
    test.setTimeout(120_000);
    const context = await browser.newContext();
    await pinUiLocale(context, "en");
    ({ accessToken } = await loginWithRetry(
      context.request,
      process.env.E2E_USER_EMAIL!,
      process.env.E2E_USER_PASSWORD!,
    ));
    api = rateLimitAware(context.request);
    page = await context.newPage();

    const wsRes = await api.get("/api/v1/workspaces", { headers: auth() });
    expect(wsRes.ok(), `GET /api/v1/workspaces failed: ${wsRes.status()}`).toBeTruthy();
    const workspace = ((await wsRes.json()) as { id: string; slug: string }[]).find(
      (w) => w.slug === SANDBOX_WS_SLUG,
    );
    expect(workspace, `the E2E credential must be a member of ${SANDBOX_WS_SLUG}`).toBeTruthy();
    const projRes = await api.get(`/api/v1/workspaces/${workspace!.id}/projects`, { headers: auth() });
    expect(projRes.ok(), `GET .../projects failed: ${projRes.status()}`).toBeTruthy();
    const project = ((await projRes.json()) as { items?: { id: string; slug: string }[] }).items?.find(
      (p) => p.slug === SANDBOX_PROJECT_SLUG,
    );
    expect(project, `project "${SANDBOX_PROJECT_SLUG}" must exist in the sandbox`).toBeTruthy();

    const createRes = await api.post(`/api/v1/projects/${project!.id}/tasks`, {
      headers: auth(),
      data: {
        title: `[e2e] code wrap ${RUN}`,
        priority: "low",
        description: `Fenced code below.\n\n\`\`\`ts\n${DESC_CODE}\n\`\`\`\n`,
      },
    });
    expect(createRes.status(), `POST .../tasks failed: ${await createRes.text()}`).toBeLessThan(300);
    const body = (await createRes.json()) as { id?: string; task?: { id?: string } };
    taskId = body.id ?? body.task?.id ?? "";
    expect(taskId, "created task id").toBeTruthy();

    const commentRes = await api.post(`/api/v1/tasks/${taskId}/comments`, {
      headers: auth(),
      data: { body: `Comment code:\n\n\`\`\`\n${COMMENT_CODE}\n\`\`\`` },
    });
    expect(commentRes.status(), `POST .../comments failed: ${await commentRes.text()}`).toBeLessThan(300);

    // The saved bodies are what the view reads: assert they round-tripped
    // whole, so a wrap measured below is a wrap of the full line.
    const saved = await api.get(`/api/v1/tasks/${taskId}`, { headers: auth() });
    expect(((await saved.json()) as { description?: string }).description).toContain(DESC_CODE);
  });

  test.afterAll(async () => {
    if (taskId) {
      const res = await api.delete(`/api/v1/tasks/${taskId}`, { headers: auth() });
      expect(res.ok(), `DELETE /api/v1/tasks/${taskId} failed: ${res.status()}`).toBeTruthy();
    }
    await page?.context().close();
  });

  const codeBlock = (text: string): Locator =>
    page.locator(".mesh-doc-prose pre:visible").filter({ hasText: text.slice(0, 48) }).first();

  /**
   * At 393 the task page is tabs (Details / Description / Comments / …) and
   * opens on Details; at 1440 the description is on the page and comments are
   * the right column's default tab. Waits for whichever of the block or its
   * tab appears first, so a slow first paint is not mistaken for "no tab".
   */
  async function reveal(text: string, tabName: string, what: string): Promise<Locator> {
    const block = codeBlock(text);
    const tab = page.getByRole("button", { name: tabName, exact: true }).filter({ visible: true }).first();
    await block.or(tab).first().waitFor({ timeout: 60_000 });
    if (!(await block.isVisible())) await tab.click();
    await expect(block, `the saved ${what}'s code block is on screen`).toBeVisible({ timeout: 30_000 });
    return block;
  }

  async function measure(block: Locator) {
    await block.scrollIntoViewIfNeeded();
    return block.evaluate((pre) => ({
      whiteSpace: getComputedStyle(pre).whiteSpace,
      client: pre.clientWidth,
      scroll: pre.scrollWidth,
      text: pre.textContent ?? "",
      pageFits: document.documentElement.scrollWidth <= window.innerWidth,
    }));
  }

  for (const width of [393, 1440]) {
    test(`${width}: description and comment code wrap inside the column, text intact`, async () => {
      await page.setViewportSize({ width, height: width === 393 ? 852 : 900 });
      await page.goto(`/t/${taskId}`, { waitUntil: "domcontentloaded" });

      const d = await measure(await reveal(DESC_CODE, "Description", "description"));
      if (SHOTS) {
        mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({ path: `${SHOTS}/description-${width}.png` });
      }

      const c = await measure(await reveal(COMMENT_CODE, "Comments", "comment"));
      if (SHOTS) await page.screenshot({ path: `${SHOTS}/comment-${width}.png` });

      for (const [name, m, code] of [
        ["description", d, DESC_CODE],
        ["comment", c, COMMENT_CODE],
      ] as const) {
        expect(m.whiteSpace, `${width} ${name}: <pre> wraps`).toBe("pre-wrap");
        expect(m.scroll, `${width} ${name}: no sideways scroll inside the block (client ${m.client})`).toBeLessThanOrEqual(m.client);
        // Wrapping is presentation only: the line is still one line of text.
        expect(m.text, `${width} ${name}: code text unchanged`).toContain(code);
        expect(m.pageFits, `${width} ${name}: the page does not scroll sideways`).toBe(true);
      }
    });
  }
});
