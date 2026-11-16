import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { loginWithRetry, pinUiLocale } from "./auth-helper";
import { rateLimitAware } from "./api-retry";

/**
 * E2E: Docs editor — images in a line of text, and Backspace/Delete at the edge
 * of headings and images.
 *
 * The real docs page on a real document in the sandbox workspace, typed into
 * with real key presses: what the browser does natively when the editor's
 * keymap declines a key (Backspace right after an image) is part of what is
 * checked, and a unit test in jsdom cannot see it. The keymap's own rules are
 * pinned by src/lib/milkdown/__tests__/block-boundary-keys.test.ts.
 *
 * Every test gets a fresh document, so no test depends on another's edits;
 * afterAll deletes them all. A change to this file runs it 20 times in
 * authed-e2e-docs-keys-stress (.gitlab-ci.yml).
 */

const RUN = `e2e-dk-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

test.describe.serial("Docs editor — images in text and Backspace/Delete at block edges", () => {
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
    // The editor reports its caret only when asked (caret-attribute.ts); caret() waits on it.
    await context.addInitScript(() => {
      (window as unknown as Record<string, unknown>).__meshCaretProbe = true;
    });
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
    // CI's account belongs to its sandbox workspace only, so the first one is
    // it. E2E_DOCS_WORKSPACE_SLUG lets a local run with a different test
    // account name its own sandbox instead.
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

    // An image that is served by the app under test itself, so its size is
    // known (192x192) and loading it depends on nothing outside the run. The
    // schema only renders http(s) sources.
    imageUrl = new URL("/icon-192.png", baseURL ?? "http://localhost:3007").toString();
  });

  test.afterAll(async () => {
    for (const id of created) {
      const res = await api.delete(`/api/v1/documents/${id}`, { headers: auth() });
      expect(res.ok(), `DELETE /api/v1/documents/${id} failed: ${res.status()}`).toBeTruthy();
    }
    await page?.context().close();
  });

  /** Creates a document and opens it; `edit` switches to the editor. */
  async function openDoc(body: string, edit: boolean): Promise<void> {
    const res = await api.post(`/api/v1/projects/${projectId}/documents`, {
      headers: auth(),
      data: { title: `E2E docs-editor-keys ${RUN} ${created.length + 1}`, body },
    });
    expect(res.ok(), `POST .../documents failed: ${res.status()} ${await res.text()}`).toBeTruthy();
    const { id } = (await res.json()) as { id: string };
    created.push(id);
    await page.goto(`${docsPath}/${id}`, { waitUntil: "networkidle" });
    await expect(page.locator(".mesh-doc-prose").first()).toBeVisible();
    if (edit) {
      await page.getByRole("button", { name: "Edit", exact: true }).first().click();
      await expect(page.locator('.mesh-doc-prose[contenteditable="true"]')).toBeVisible();
    }
  }

  const editor = () => page.locator('.mesh-doc-prose[contenteditable="true"]');

  /** Top-level blocks as `TAG:text`, an image as `[img]`. */
  async function blocks(): Promise<string[]> {
    return editor().evaluate((root) =>
      [...root.children].map((el) => {
        let text = "";
        const walk = (n: Node) => {
          if (n.nodeType === Node.TEXT_NODE) text += n.textContent;
          else if (n instanceof HTMLImageElement) {
            if (!n.classList.contains("ProseMirror-separator")) text += "[img]";
          } else n.childNodes.forEach(walk);
        };
        walk(el);
        return `${el.tagName}:${text}`;
      }),
    );
  }

  /** The image line: a paragraph with an image and no text. */
  const IMAGE_LINE = Symbol("image line");

  /**
   * Puts the caret into a block — the one whose text starts with `target`, or
   * the image line — at its start, at its end, or right after its first image.
   *
   * The caret is set in the DOM the way a click would leave it, and the editor
   * takes it over on its own `selectionchange`. A key pressed before that lands
   * where the editor's state still is — the start of the document.
   * So this waits until the editor reports the same place on its root
   * (`data-caret`, src/lib/milkdown/caret-attribute.ts), and places it again if
   * the editor moved it back meanwhile.
   */
  async function caret(
    target: string | typeof IMAGE_LINE,
    where: "start" | "end" | "after-image",
  ): Promise<void> {
    await expect(async () => {
      await editor().focus();
      const expected = await editor().evaluate(
        (root, [prefix, at]) => {
          const isImage = (n: Node): n is HTMLImageElement =>
            n instanceof HTMLImageElement && !n.classList.contains("ProseMirror-separator");
          const image = (el: Element) => el.querySelector("img:not(.ProseMirror-separator)");
          const blocks = [...root.children];
          const block = blocks.find((el) => {
            const t = el.textContent ?? "";
            return prefix === null ? t === "" && !!image(el) : t.startsWith(prefix);
          });
          if (!block) throw new Error(`no block for "${prefix ?? "image line"}"`);
          const range = document.createRange();
          if (at === "after-image") {
            range.setStartAfter(image(block)!);
          } else {
            range.selectNodeContents(block);
          }
          range.collapse(at !== "end");
          const sel = window.getSelection()!;
          sel.removeAllRanges();
          sel.addRange(range);

          // The same place in the editor's terms: a character of text or an
          // image is one step, so count them up to the caret.
          let offset = 0;
          const walk = (n: Node): boolean => {
            if (n === range.startContainer && n.nodeType === Node.TEXT_NODE) {
              offset += range.startOffset;
              return true;
            }
            if (n === range.startContainer) {
              n.childNodes.forEach((c, i) => {
                if (i < range.startOffset) offset += size(c);
              });
              return true;
            }
            if (n.nodeType === Node.TEXT_NODE) offset += n.textContent!.length;
            else if (isImage(n)) offset += 1;
            else for (const c of n.childNodes) if (walk(c)) return true;
            return false;
          };
          const size = (n: Node): number => {
            if (n.nodeType === Node.TEXT_NODE) return n.textContent!.length;
            if (isImage(n)) return 1;
            let total = 0;
            n.childNodes.forEach((c) => (total += size(c)));
            return total;
          };
          if (!walk(block)) throw new Error("the caret is not inside its block");
          return `${blocks.indexOf(block)}:${offset}`;
        },
        [target === IMAGE_LINE ? null : target, where] as const,
      );
      await expect(editor(), "the editor took the caret").toHaveAttribute("data-caret", expected, {
        timeout: 2_000,
      });
    }).toPass({ timeout: 15_000 });
  }

  const HEADED = (img: string) =>
    ["## Title", "Intro paragraph.", "## Heading two", `Text before ![](${img}) text after.`].join("\n\n");

  test("an image inside a line of text stays in that line, viewing and editing", async () => {
    await openDoc(`Text before ![](${imageUrl}) text after.\n\n![](${imageUrl})\n\nAfter image.\n`, false);
    for (const mode of ["view", "edit"] as const) {
      if (mode === "edit") {
        await page.getByRole("button", { name: "Edit", exact: true }).first().click();
        await expect(editor()).toBeVisible();
      }
      const prose = page.locator(".mesh-doc-prose").first();
      await expect(prose.locator("img:not(.ProseMirror-separator)").first()).toHaveJSProperty("complete", true);
      const m = await prose.evaluate((root) => {
        const p = root.children[0] as HTMLElement;
        const img = p.querySelector("img:not(.ProseMirror-separator)") as HTMLImageElement;
        const text = document.createRange();
        text.selectNodeContents(p.firstChild!);
        const solo = root.children[1] as HTMLElement;
        const soloImg = solo.querySelector("img:not(.ProseMirror-separator)") as HTMLImageElement;
        return {
          imgInP: img.parentElement === p,
          imgBottom: img.getBoundingClientRect().bottom,
          textBottom: text.getBoundingClientRect().bottom,
          soloP: solo.getBoundingClientRect().height,
          soloImg: soloImg.getBoundingClientRect().height,
        };
      });
      expect(m.imgInP, `${mode}: the image is inside the paragraph of its text`).toBe(true);
      // Same line: the image ends where the text line before it ends. A block
      // image ends a whole image-height below the text.
      expect(Math.abs(m.imgBottom - m.textBottom), `${mode}: image and text share a line`).toBeLessThan(8);
      // An image on its own line takes the image's height, without an empty
      // text line under it.
      expect(m.soloP - m.soloImg, `${mode}: no empty line under an image on its own line`).toBeLessThan(6);
    }
  });

  test("spacing and wrapping at 1440 and 393: one margin per gap, words whole, no sideways page scroll", async () => {
    const columns = Array.from({ length: 9 }, (_, i) => i + 1);
    const wideTable = [
      `| ${columns.map((n) => `Column ${n}`).join(" | ")} |`,
      `|${" --- |".repeat(columns.length)}`,
      `| ${columns.map((n) => `value number ${n}`).join(" | ")} |`,
    ].join("\n");
    const linkTable = `| Key | Value |\n| --- | --- |\n| link | https://example.com/${"a".repeat(110)} |`;
    await openDoc(
      [
        "# Heading one",
        "Intro paragraph.",
        "## Heading two",
        `![](${imageUrl})`,
        "### Heading three",
        "Text under the third heading.",
        wideTable,
        linkTable,
        `A long link https://example.com/${"b".repeat(140)} and more text.`,
      ].join("\n\n"),
      false,
    );
    const prose = page.locator(".mesh-doc-prose").first();
    await expect(prose.locator("img:not(.ProseMirror-separator)").first()).toHaveJSProperty("complete", true);

    for (const width of [1440, 393]) {
      await page.setViewportSize({ width, height: 900 });
      const m = await prose.evaluate((root) => {
        const kids = [...root.children] as HTMLElement[];
        const css = (el: Element) => getComputedStyle(el);
        const px = (v: string) => parseFloat(v) || 0;
        const headings = kids.filter((el) => /^H[1-6]$/.test(el.tagName));
        const tables = kids.filter((el) => el.tagName === "TABLE");
        // A word split across two lines has two client rects.
        const brokenWords: string[] = [];
        tables[0]!.querySelectorAll("th, td").forEach((cell) => {
          const walker = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
          for (let n = walker.nextNode(); n; n = walker.nextNode()) {
            const text = n.textContent ?? "";
            for (const match of text.matchAll(/\S+/g)) {
              const range = document.createRange();
              range.setStart(n, match.index!);
              range.setEnd(n, match.index! + match[0].length);
              if (range.getClientRects().length > 1) brokenWords.push(match[0]);
            }
          }
        });
        return {
          firstTop: css(kids[0]!).marginTop,
          bottoms: kids.filter((el) => el.tagName !== "HR").map((el) => px(css(el).marginBottom)),
          headingGaps: headings.map((h) => ({
            above: px(css(h).marginTop),
            below: px(css(h.nextElementSibling!).marginTop),
          })),
          brokenWords,
          wideScrolls: tables[0]!.scrollWidth > tables[0]!.clientWidth,
          linkTableFits: tables[1]!.scrollWidth <= tables[1]!.clientWidth,
          proseFits: root.scrollWidth <= root.clientWidth,
          pageFits: document.documentElement.scrollWidth <= window.innerWidth,
        };
      });
      expect(m.firstTop, `${width}: no space above the first block`).toBe("0px");
      expect(m.bottoms.every((b) => b === 0), `${width}: gaps come from margin-top only`).toBe(true);
      for (const gap of m.headingGaps.slice(1)) {
        expect(gap.below, `${width}: a heading sits closer to its text than to the block above`).toBeLessThan(gap.above);
      }
      expect(m.brokenWords, `${width}: words in table cells stay whole`).toEqual([]);
      expect(m.linkTableFits, `${width}: a long link in a cell wraps instead of widening the table`).toBe(true);
      expect(m.proseFits, `${width}: nothing sticks out of the text column`).toBe(true);
      expect(m.pageFits, `${width}: the page does not scroll sideways`).toBe(true);
      if (width === 393) {
        expect(m.wideScrolls, "393: a table too wide for the column scrolls inside its own box").toBe(true);
      }
    }
    await page.setViewportSize({ width: 1280, height: 720 });
  });

  test("Backspace at the start of a heading turns it into a paragraph, text kept", async () => {
    await openDoc(HEADED(imageUrl), true);
    await caret("Heading two", "start");
    await page.keyboard.press("Backspace");
    expect(await blocks()).toEqual([
      "H2:Title",
      "P:Intro paragraph.",
      "P:Heading two",
      "P:Text before [img] text after.",
    ]);
  });

  test("Delete at the start of a heading deletes a character, the level stays", async () => {
    await openDoc(HEADED(imageUrl), true);
    await caret("Heading two", "start");
    await page.keyboard.press("Delete");
    expect((await blocks())[2]).toBe("H2:eading two");
  });

  test("Delete at the end of a paragraph before a heading keeps both texts on their lines", async () => {
    await openDoc(HEADED(imageUrl), true);
    await caret("Intro paragraph.", "end");
    await page.keyboard.press("Delete");
    expect(await blocks()).toEqual([
      "H2:Title",
      "P:Intro paragraph.",
      "P:Heading two",
      "P:Text before [img] text after.",
    ]);
  });

  test("Backspace on an empty paragraph between two headings removes only it", async () => {
    await openDoc("## Before\n\n## After\n", true);
    await caret("Before", "end");
    await page.keyboard.press("Enter");
    expect(await blocks()).toEqual(["H2:Before", "P:", "H2:After"]);
    await page.keyboard.press("Backspace");
    expect(await blocks()).toEqual(["H2:Before", "H2:After"]);
  });

  const IMAGED = (img: string) =>
    ["## Title", `Text before ![](${img}) text after.`, `![](${img})`, "After image."].join("\n\n");

  test("Backspace at the start of the paragraph under an image removes the image whole", async () => {
    await openDoc(IMAGED(imageUrl), true);
    await caret("After image.", "start");
    await page.keyboard.press("Backspace");
    expect(await blocks()).toEqual([
      "H2:Title",
      "P:Text before [img] text after.",
      "P:After image.",
    ]);
  });

  test("Backspace right after an image on its own line removes it, neighbours untouched", async () => {
    await openDoc(IMAGED(imageUrl), true);
    await caret(IMAGE_LINE, "after-image");
    await page.keyboard.press("Backspace");
    const after = await blocks();
    expect(after.filter((b) => b.includes("[img]"))).toEqual(["P:Text before [img] text after."]);
    expect(after).toContain("H2:Title");
    expect(after).toContain("P:After image.");
  });
});
