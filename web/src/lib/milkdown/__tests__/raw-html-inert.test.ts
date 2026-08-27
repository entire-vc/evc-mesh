import { afterEach, describe, expect, it } from "vitest";
import { Editor, editorViewCtx } from "@milkdown/kit/core";
import { getMarkdown } from "@milkdown/kit/utils";
import { makeDocEditor } from "@/lib/milkdown/editor";

/**
 * Raw HTML in a document body must never become live markup.
 *
 * ## What was measured, and why this test is shaped this way
 *
 * The G1 spike asked whether the commonmark preset lets raw HTML through. It
 * does *parse* it: `<script>…</script>` in a body becomes an `html` node rather
 * than being dropped, and it is written back out verbatim, so the bytes survive
 * in S3. What it does not do is render it — the `html` node's toDOM emits a
 * `<span data-type="html">` whose *text* is the escaped source. Nothing
 * executes.
 *
 * So the safety we have is inherited from an upstream rendering decision, not
 * asserted by our configuration. That is precisely why it needs a test: the day
 * `html`'s toDOM starts emitting real markup — an upstream change, a preset
 * swap, someone reaching for a raw-HTML feature — every payload already sitting
 * in a stored document becomes live, and nothing else in this repo would notice.
 *
 * The assertions look at the rendered DOM rather than at the markdown, because
 * the markdown legitimately still contains the payload. "It is stored" and "it
 * is dangerous" are different claims, and only the second one is a bug.
 */

const openEditors: Editor[] = [];

async function open(markdown: string) {
  const root = document.createElement("div");
  document.body.appendChild(root);
  const editor = makeDocEditor({ root, defaultValue: markdown, editable: true });
  await editor.create();
  openEditors.push(editor);
  const dom = editor.action((ctx) => ctx.get(editorViewCtx).dom as HTMLElement);
  return { html: dom.innerHTML, serialized: editor.action(getMarkdown()) };
}

afterEach(async () => {
  await Promise.all(openEditors.splice(0).map((e) => e.destroy()));
  document.body.innerHTML = "";
});

const PAYLOADS: Record<string, string> = {
  "script tag": `<script>window.__pwned = true;</script>`,
  "img with onerror": `<img src=x onerror="window.__pwned = true">`,
  "iframe": `<iframe src="https://evil.example.com"></iframe>`,
  "inline html with a handler": `A paragraph with <b>bold</b> and <span onclick="window.__pwned=true">span</span>.`,
  "svg with onload": `<svg onload="window.__pwned = true"></svg>`,
};

/** The live-markup check, in one place so the control below uses the same one. */
function assertInert(html: string, label: string) {
  const probe = document.createElement("div");
  probe.innerHTML = html;
  expect(probe.querySelector("script"), `${label}: <script> reached the DOM`).toBeNull(); // nosemgrep: javascript.lang.security.audit.unknown-value-with-script-tag.unknown-value-with-script-tag -- this IS the test asserting the payload stays inert; PAYLOADS above are fixed test fixtures, never live user input
  expect(probe.querySelector("iframe"), `${label}: <iframe> reached the DOM`).toBeNull();
  expect(
    probe.querySelector("[onerror],[onload],[onclick]"),
    `${label}: an inline event handler reached the DOM`,
  ).toBeNull();
}

describe("raw HTML in a document body is inert", () => {
  // Without this the three assertions above could all be passing because the
  // selectors never match anything, and the suite would stay green over an
  // editor that renders payloads live.
  it("POSITIVE CONTROL: the check catches live markup when it is there", () => {
    expect(() =>
      assertInert(
        `<script>1</script><iframe src="x"></iframe><img src=x onerror="1">`,
        "control",
      ),
    ).toThrow();
  });

  for (const [name, payload] of Object.entries(PAYLOADS)) {
    it(`renders nothing live for: ${name}`, async () => {
      const { html } = await open(payload);
      assertInert(html, name);
    });
  }

  it("keeps the payload in the markdown — inert is not the same as removed", async () => {
    const { serialized } = await open(PAYLOADS["script tag"]!);

    // Documented deliberately: the bytes DO survive a round-trip. Any future
    // consumer of a document body that renders it as HTML (an export, a digest,
    // a second renderer) inherits this payload and must sanitize it itself.
    expect(serialized).toContain("<script>");
  });
});
