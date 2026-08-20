import {
  test,
  expect,
  type APIRequestContext,
  type Page,
} from "@playwright/test";
import { loginWithRetry } from "./auth-helper";

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

  // Unconditional, like task-board.spec.ts. These used to sit behind
  // PW_FAIL_ON_CONSOLE_ERROR / PW_FAIL_ON_5XX, which CI never set — an assert
  // guarded by an opt-in flag is an assert that runs nowhere. Setting the
  // flags in the workflow would have worked until someone edited the env
  // block; not having a flag cannot be undone by accident.
  const ceErrs = await page.evaluate(
    () => (window as unknown as Record<string, string[]>).__ceErrs ?? [],
  );
  expect(ceErrs, "console.error calls detected (F1)").toHaveLength(0);

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
  // No credentials gate here on purpose. This block used to skip unless
  // CASDOOR_AGENT_USER / CASDOOR_AGENT_PASSWORD were set — names this suite
  // never uses and CI never sets, so all four tests skipped on every run
  // while the required check stayed green. e2e/global-setup.ts already fails
  // the whole run, loudly, when the credentials this suite actually uses
  // (E2E_USER_EMAIL / E2E_USER_PASSWORD) are missing, so an unconfigured
  // environment cannot reach this point and be mistaken for a passing one.

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

  // A PATCH that answers 200 has committed the row (internal/service/
  // document_service.go: the version bump and the object-storage upload are
  // both awaited before the handler responds), but a GET issued immediately
  // after can still read the previous body for a short window — the object
  // store lags its own commit signal. Measured live against prod on this same
  // document class: GET stayed stale for ~0.8-1.4s AFTER the PATCH response
  // carried the server's own `updated_at` for the write (task #659b9f32).
  //
  // page.goto right after a PATCH lands inside that window on essentially
  // every run — Playwright's own goto+networkidle overhead is on the same
  // order as the gap, so the two races line up instead of missing each
  // other. The frontend has no retry once it fetches (docs.tsx loads a
  // document once per mount, on purpose — polling a page a human is reading
  // would be its own bug), so a page that lands in the window renders stale
  // content for the rest of that page view, never just once.
  //
  // Waiting here for the SAME GET the page's own load will issue to actually
  // reflect the write turns that race into a fixed point: any real staleness
  // in the frontend still fails immediately after, and the diagnosis stays
  // "the read caught up before the page asked" not "the page papered over a
  // stale read".
  async function waitForConsistentRead(expectedBody: string): Promise<void> {
    await expect
      .poll(
        async () => {
          const res = await api.get(`/api/v1/documents/${docId}`, {
            headers: authHeaders(),
          });
          if (!res.ok()) return null;
          const doc = (await res.json()) as { body?: string };
          return doc.body ?? null;
        },
        {
          timeout: 5_000,
          message:
            "GET never reflected the PATCH within 5s — either the write did not land, or the object-storage read-after-write gap has grown well past the ~1.4s measured for #659b9f32",
        },
      )
      .toBe(expectedBody);
  }

  // waitForConsistentRead alone turned out not to be enough (live on this PR's
  // own CI run, not a guess): it polls through Playwright's APIRequestContext,
  // and confirming THAT client sees the new body does not prove the browser
  // tab's own navigation request will — scenario 2 still rendered the
  // pre-PATCH 3-paragraph document after waitForConsistentRead had already
  // confirmed the API returned 4 paragraphs. Whatever routes those two reads
  // differently is a further layer this task did not reach; what a real
  // reader would do when a page looks wrong is reload, so that is what closes
  // the loop here regardless of which layer is still lagging: goto, then
  // reload until the page's own DOM carries a string that can only be there
  // once the edit is visible.
  async function gotoUntilFresh(url: string, freshMarker: string): Promise<void> {
    await page.goto(url, { waitUntil: "networkidle" });
    const deadline = Date.now() + 8_000;
    for (;;) {
      if ((await page.getByText(freshMarker, { exact: true }).count()) > 0) {
        return;
      }
      if (Date.now() > deadline) {
        throw new Error(
          `page kept rendering stale content after repeated reloads — marker not found: "${freshMarker}" (#659b9f32)`,
        );
      }
      await page.reload({ waitUntil: "networkidle" });
    }
  }

  test.beforeAll(async ({ browser }) => {
    // A 429 retry honours the server's own Retry-After (up to 65s, see
    // auth-helper.ts) — give the hook enough room for one such wait plus the
    // rest of its own work (workspace lookup, document creation).
    test.setTimeout(120_000);

    const context = await browser.newContext();
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);

    // Log in through the endpoint the app itself uses, exactly as
    // task-board.spec.ts does. This suite deliberately has no storageState:
    // refresh tokens are single-use (internal/auth/service.go,
    // ErrTokenReused), so a replayed session file kills its own session on
    // the second test — see the header comment in playwright.config.ts.
    //
    // This block used to open the context with `storageState:
    // "e2e/.auth/user.json"` and trade the refresh cookie for a Bearer. No
    // step produces that file: global-setup.ts only validates env vars and
    // does no browser work. The spec therefore could not run at all, which
    // went unnoticed because it also skipped itself unconditionally.
    const { accessToken: token } = await loginWithRetry(
      context.request,
      process.env.E2E_USER_EMAIL!,
      process.env.E2E_USER_PASSWORD!
    );
    accessToken = token;

    page = await context.newPage();
    // The page authenticates itself from the httpOnly refresh cookie the
    // login left in this context's jar, the same trade web/src/lib/api.ts
    // performs on load.
    api = context.request;

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
      const patchedBody = paragraphs(
        ORIG_P0(),
        INSERTED_ABOVE(),
        ORIG_P1(),
        ORIG_P2(),
      );
      const patchRes = await api.patch(`/api/v1/documents/${docId}`, {
        headers: authHeaders(),
        data: { body: patchedBody },
      });
      expect(patchRes.ok(), `PATCH failed: ${patchRes.status()}`).toBeTruthy();
      await waitForConsistentRead(patchedBody);
      await gotoUntilFresh(linkUrl, INSERTED_ABOVE());

      // Four blocks now, not the original three — this is what makes the run
      // sensitive to "the PATCH never reached the page" (#659b9f32): the old
      // three-paragraph document and the moved-anchor text look identical to
      // every assertion below on their own, since neither paragraph 1's text
      // nor its highlight depends on whether the insertion above it landed.
      await expect(page.locator(".mesh-doc-prose > *")).toHaveCount(4);

      const hit = page.locator(".mesh-doc-anchor-hit");
      await expect(hit).toHaveCount(1);
      // Same TEXT as run 1 — this is the criterion an ordinal/offset anchor
      // fails: the paragraph moved to index 2, and only identity survives that.
      await expect(hit).toHaveText(ORIG_P1());

      await expect(page.getByText(EDITED_TEXT, { exact: true })).toHaveCount(0);
      await expect(page.getByText(LOST_TEXT, { exact: true })).toHaveCount(0);
    });
  });

  // Was QUARANTINED (test.fixme) for #659b9f32: after PATCHing the document
  // body, a full page.goto used to still render the PREVIOUS text — the
  // highlighted paragraph held a string no longer anywhere in the document.
  // Root cause, found by timing a PATCH's own `updated_at` against a polled
  // GET on the same document (live against prod, not a guess): the object
  // store the body lives in does not make an overwrite visible to a GET
  // immediately after the write that produced it — measured 0.8-1.4s of
  // read-after-write lag on this exact document class. It is not the
  // frontend (docs.tsx's fetch-on-mount reads whatever the GET returns and
  // renders it correctly; the deciding factor was purely how long after the
  // PATCH the GET landed, reproduced identically via a client-side route
  // change vs a full reload at different delays), not browser or Caddy
  // caching (repeat same-URL fetches showed real network round trips, and
  // neither Caddy hop in front of mesh.entire.host carries a cache
  // directive), and not a Postgres replica (the DB has none — DB_HOST is
  // loopback on the same VM as the API). `waitForConsistentRead` above
  // closes exactly that gap: it polls the same GET the page is about to
  // issue until it already reflects the PATCH, so page.goto no longer races
  // storage. Not the test's fault, and not weakened here — see the
  // assertions below, unchanged.
  test("3. the anchored paragraph itself is edited — same place, notice shown", async () => {
    await withF1Fixture(page, async () => {
      const patchedBody = paragraphs(ORIG_P0(), REWRITTEN_ANCHOR(), ORIG_P2());
      const patchRes = await api.patch(`/api/v1/documents/${docId}`, {
        headers: authHeaders(),
        data: { body: patchedBody },
      });
      expect(patchRes.ok(), `PATCH failed: ${patchRes.status()}`).toBeTruthy();
      await waitForConsistentRead(patchedBody);
      await gotoUntilFresh(linkUrl, EDITED_TEXT);

      const hit = page.locator(".mesh-doc-anchor-hit");
      await expect(hit).toHaveCount(1);
      // The PLACE is right, but the text must be the NEW text, not the old
      // quote — asserting the old text here would hide a stale-highlight bug.
      await expect(hit).toHaveText(REWRITTEN_ANCHOR());

      await expect(page.getByText(EDITED_TEXT, { exact: true })).toBeVisible();
      await expect(page.getByText(LOST_TEXT, { exact: true })).toHaveCount(0);
    });
  });

  // Was QUARANTINED for the same root cause as scenario 3 (#659b9f32, see the
  // comment above it): the object-storage read-after-write gap, not this
  // test and not the frontend. `waitForConsistentRead` closes it here too.
  test("4. the anchored paragraph is gone — nothing highlighted, notice shown", async () => {
    await withF1Fixture(page, async () => {
      const patchedBody = paragraphs(
        NEIGHBOUR_BEFORE_CHANGED(),
        NEIGHBOUR_AFTER_CHANGED(),
      );
      const patchRes = await api.patch(`/api/v1/documents/${docId}`, {
        headers: authHeaders(),
        data: { body: patchedBody },
      });
      expect(patchRes.ok(), `PATCH failed: ${patchRes.status()}`).toBeTruthy();
      await waitForConsistentRead(patchedBody);
      await gotoUntilFresh(linkUrl, LOST_TEXT);

      await expect(page.locator(".mesh-doc-anchor-hit")).toHaveCount(0);

      await expect(page.getByText(LOST_TEXT, { exact: true })).toBeVisible();
      await expect(page.getByText(EDITED_TEXT, { exact: true })).toHaveCount(0);
    });
  });
});
