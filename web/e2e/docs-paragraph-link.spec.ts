import {
  test,
  expect,
  type APIRequestContext,
  type Page,
} from "@playwright/test";

/**
 * E2E scenario: docs-paragraph-link
 * Scenario doc: web/e2e-scenarios/docs-paragraph-link.md
 *
 * Four runs against ONE document + ONE copied link, because the point of the
 * unit is what happens to a fixed link as the document is edited around it:
 *   1. unchanged            -> exact text, no notice
 *   2. edited ABOVE anchor  -> same text still resolves (moved), no notice
 *   3. anchor itself edited -> same place, "edited" notice
 *   4. anchor removed       -> nothing highlighted, "removed" notice
 *
 * F1 harness (inline, same shape as task-board.spec.ts):
 *   - Captures console.error / pageerror events
 *   - Checks HTTP 5xx via performance.getEntriesByType('resource')
 */
async function withF1Fixture(
  page: Page,
  fn: () => Promise<void>,
): Promise<void> {
  const pageErrors: string[] = [];

  await page.addInitScript(() => {
    (window as unknown as Record<string, unknown>).__ceErrs = [];
    const orig = console.error.bind(console);
    console.error = (...args: unknown[]) => {
      ((window as unknown as Record<string, unknown[]>).__ceErrs).push(
        args.map(String).join(" "),
      );
      orig(...args);
    };
  });

  page.on("pageerror", (err) => pageErrors.push(err.message));

  await fn();

  if (process.env.PW_FAIL_ON_CONSOLE_ERROR === "1") {
    const ceErrs = await page.evaluate(
      () => (window as unknown as Record<string, string[]>).__ceErrs ?? [],
    );
    expect(ceErrs, "console.error calls detected (F1)").toHaveLength(0);
  }

  if (process.env.PW_FAIL_ON_5XX === "1") {
    const http5xx = await page.evaluate(() =>
      performance
        .getEntriesByType("resource")
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        .filter((r: any) => r.responseStatus >= 500)
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        .map((r: any) => `${r.responseStatus} ${r.name}`),
    );
    expect(http5xx, "HTTP 5xx responses detected (F1)").toHaveLength(0);
    expect(pageErrors, "Uncaught page errors detected (F1)").toHaveLength(0);
  }
}

// The exact banner strings from web/src/pages/docs.tsx's AnchorNotice — a
// change to either wording must fail this spec, not silently stop matching.
const EDITED_TEXT =
  "This paragraph has been edited since the link was created. You are at its place in the document, but the text is not what was linked.";
const LOST_TEXT =
  "The linked paragraph is no longer in this document. It was edited or removed after the link was created.";

// A run-unique marker so two concurrent CI runs never share recognisable text.
const RUN = `e2e-pl-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

// Original three paragraphs (index 0, 1, 2). Paragraph 1 (the "second
// paragraph") is the one the link is made from. Kept short and distinct so
// the anchor's exact/prefix/suffix windows (96 / 32 chars, lib/docs/anchor.ts)
// capture each paragraph in full rather than a truncated slice.
const ORIG_P0 = () => `Intro ${RUN} — before the anchor.`;
const ORIG_P1 = () => `Anchor ${RUN} — the paragraph this link points to.`;
const ORIG_P2 = () => `Closing ${RUN} — after the anchor.`;

// Run 2: a new paragraph inserted ABOVE the anchor. P1 itself is untouched.
const INSERTED_ABOVE = () =>
  `Inserted ${RUN} — a new paragraph added above the anchor.`;

// Run 3: the anchored paragraph is rewritten; its neighbours (P0, P2) are not
// touched, so the anchor's prefix/suffix context still holds.
const REWRITTEN_ANCHOR = () =>
  `Anchor ${RUN} — REWRITTEN, the old quote no longer matches.`;

// Run 4: the anchored paragraph is gone AND both neighbours are reworded, so
// neither the quote nor the surrounding context can place it any more. The
// endings/openings differ inside the anchor's 32-char context window on
// purpose — a change elsewhere in the paragraph would not be enough.
const NEIGHBOUR_BEFORE_CHANGED = () =>
  `Intro ${RUN} — ending replaced, old context clue gone.`;
const NEIGHBOUR_AFTER_CHANGED = () =>
  `Changed ${RUN} — new opening, old context clue gone.`;

function paragraphs(...blocks: string[]): string {
  return blocks.join("\n\n");
}

test.describe.serial("Docs — paragraph link survives edits (docs-paragraph-link)", () => {
  // Skip when Casdoor credentials are not configured (e.g. CI without secrets).
  // Skipped tests exit 0 — the job stays green in REPORT mode. Same idiom as
  // task-board.spec.ts's per-test skip, applied once for the whole group.
  test.skip(
    !process.env.CASDOOR_AGENT_USER || !process.env.CASDOOR_AGENT_PASSWORD,
    "CASDOOR_AGENT_USER / CASDOOR_AGENT_PASSWORD not set — authed E2E skipped",
  );

  let page: Page;
  let api: APIRequestContext;
  let accessToken = "";
  let projectId = "";
  let docsPath = "";
  let docId = "";
  let linkUrl = "";
  // Set once the document is actually created, so afterAll knows whether
  // there is anything to clean up (a beforeAll failure before creation must
  // not attempt to delete a document that was never made).
  let created = false;

  function authHeaders(): Record<string, string> {
    return { Authorization: `Bearer ${accessToken}` };
  }

  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({
      storageState: "e2e/.auth/user.json",
    });
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    page = await context.newPage();
    // Shares cookies (incl. the httpOnly refresh cookie) with `context` — the
    // app's own api() helper trades that cookie for a Bearer access token the
    // same way, via POST /api/v1/auth/refresh (internal/handler/auth_handler.go).
    api = context.request;

    const refreshRes = await api.post("/api/v1/auth/refresh");
    expect(
      refreshRes.ok(),
      `POST /api/v1/auth/refresh failed: ${refreshRes.status()} ${await refreshRes.text()}`,
    ).toBeTruthy();
    const refreshBody = (await refreshRes.json()) as {
      tokens: { access_token: string };
    };
    accessToken = refreshBody.tokens.access_token;

    const wsRes = await api.get("/api/v1/workspaces", { headers: authHeaders() });
    expect(wsRes.ok(), `GET /api/v1/workspaces failed: ${wsRes.status()}`).toBeTruthy();
    const workspaces = (await wsRes.json()) as { id: string; slug: string }[];
    expect(workspaces.length, "agent user has no workspace to run against").toBeGreaterThan(0);
    const workspace = workspaces[0]!;

    const projRes = await api.get(`/api/v1/workspaces/${workspace.id}/projects`, {
      headers: authHeaders(),
    });
    expect(projRes.ok(), `GET .../projects failed: ${projRes.status()}`).toBeTruthy();
    const projPage = (await projRes.json()) as {
      items: { id: string; slug: string; is_archived: boolean }[];
    };
    const project =
      projPage.items.find((p) => !p.is_archived) ?? projPage.items[0];
    expect(project, "workspace has no project to create a test document in").toBeTruthy();

    projectId = project!.id;
    docsPath = `/w/${workspace.slug}/p/${project!.slug}/docs`;

    const createRes = await api.post(`/api/v1/projects/${projectId}/documents`, {
      headers: authHeaders(),
      data: {
        title: `E2E docs-paragraph-link ${RUN}`,
        body: paragraphs(ORIG_P0(), ORIG_P1(), ORIG_P2()),
      },
    });
    expect(
      createRes.ok(),
      `POST .../documents failed: ${createRes.status()} ${await createRes.text()}`,
    ).toBeTruthy();
    const doc = (await createRes.json()) as { id: string };
    docId = doc.id;
    created = true;
  });

  test.afterAll(async () => {
    // Runs regardless of which test above passed or failed — nothing this
    // spec creates may survive it.
    if (created && docId) {
      const delRes = await api.delete(`/api/v1/documents/${docId}`, {
        headers: authHeaders(),
      });
      expect(
        delRes.ok(),
        `DELETE /api/v1/documents/${docId} failed: ${delRes.status()}`,
      ).toBeTruthy();

      // Deep-verify the cleanup itself — list the project's documents and
      // confirm this run's document is actually gone, rather than trusting
      // the DELETE response code alone.
      const listRes = await api.get(`/api/v1/projects/${projectId}/documents`, {
        headers: authHeaders(),
        params: { page: 1, page_size: 200 },
      });
      expect(listRes.ok(), `GET .../documents failed: ${listRes.status()}`).toBeTruthy();
      const listed = (await listRes.json()) as { items: { id: string }[] };
      expect(
        listed.items.some((d) => d.id === docId),
        "deleted document still present in the project's document list",
      ).toBe(false);
    }
    await page?.context().close();
  });

  test("1. copy and follow on an unchanged document", async () => {
    await withF1Fixture(page, async () => {
      await page.goto(`${docsPath}/${docId}`, { waitUntil: "networkidle" });

      const blocks = page.locator(".mesh-doc-prose > *");
      await expect(blocks).toHaveCount(3);

      // Right-click the SECOND paragraph (index 1) and copy its link.
      await blocks.nth(1).click({ button: "right" });
      await page
        .getByRole("menuitem", { name: "Copy link to this paragraph" })
        .click();

      // Poll rather than assume the async clipboard write finished by the
      // time the click's dispatch resolved.
      await expect
        .poll(() => page.evaluate(() => navigator.clipboard.readText()), {
          timeout: 5_000,
        })
        .toContain(`${docsPath}/${docId}#p=`);
      linkUrl = await page.evaluate(() => navigator.clipboard.readText());

      await page.goto(linkUrl, { waitUntil: "networkidle" });

      const hit = page.locator(".mesh-doc-anchor-hit");
      await expect(hit).toHaveCount(1);
      await expect(hit).toHaveText(ORIG_P1());

      await expect(page.getByText(EDITED_TEXT, { exact: true })).toHaveCount(0);
      await expect(page.getByText(LOST_TEXT, { exact: true })).toHaveCount(0);
    });
  });

  test("2. edited above the anchor — same text still resolves, no notice", async () => {
    await withF1Fixture(page, async () => {
      const patchRes = await api.patch(`/api/v1/documents/${docId}`, {
        headers: authHeaders(),
        data: { body: paragraphs(ORIG_P0(), INSERTED_ABOVE(), ORIG_P1(), ORIG_P2()) },
      });
      expect(patchRes.ok(), `PATCH failed: ${patchRes.status()}`).toBeTruthy();

      await page.goto(linkUrl, { waitUntil: "networkidle" });

      const hit = page.locator(".mesh-doc-anchor-hit");
      await expect(hit).toHaveCount(1);
      // Same TEXT as run 1 — this is the criterion an ordinal/offset anchor
      // fails: the paragraph moved to index 2, and only identity survives that.
      await expect(hit).toHaveText(ORIG_P1());

      await expect(page.getByText(EDITED_TEXT, { exact: true })).toHaveCount(0);
      await expect(page.getByText(LOST_TEXT, { exact: true })).toHaveCount(0);
    });
  });

  test("3. the anchored paragraph itself is edited — same place, notice shown", async () => {
    await withF1Fixture(page, async () => {
      const patchRes = await api.patch(`/api/v1/documents/${docId}`, {
        headers: authHeaders(),
        data: { body: paragraphs(ORIG_P0(), REWRITTEN_ANCHOR(), ORIG_P2()) },
      });
      expect(patchRes.ok(), `PATCH failed: ${patchRes.status()}`).toBeTruthy();

      await page.goto(linkUrl, { waitUntil: "networkidle" });

      const hit = page.locator(".mesh-doc-anchor-hit");
      await expect(hit).toHaveCount(1);
      // The PLACE is right, but the text must be the NEW text, not the old
      // quote — asserting the old text here would hide a stale-highlight bug.
      await expect(hit).toHaveText(REWRITTEN_ANCHOR());

      await expect(page.getByText(EDITED_TEXT, { exact: true })).toBeVisible();
      await expect(page.getByText(LOST_TEXT, { exact: true })).toHaveCount(0);
    });
  });

  test("4. the anchored paragraph is gone — nothing highlighted, notice shown", async () => {
    await withF1Fixture(page, async () => {
      const patchRes = await api.patch(`/api/v1/documents/${docId}`, {
        headers: authHeaders(),
        data: {
          body: paragraphs(NEIGHBOUR_BEFORE_CHANGED(), NEIGHBOUR_AFTER_CHANGED()),
        },
      });
      expect(patchRes.ok(), `PATCH failed: ${patchRes.status()}`).toBeTruthy();

      await page.goto(linkUrl, { waitUntil: "networkidle" });

      await expect(page.locator(".mesh-doc-anchor-hit")).toHaveCount(0);

      await expect(page.getByText(LOST_TEXT, { exact: true })).toBeVisible();
      await expect(page.getByText(EDITED_TEXT, { exact: true })).toHaveCount(0);
    });
  });
});
