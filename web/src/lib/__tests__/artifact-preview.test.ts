import { describe, expect, it } from "vitest";
import {
  PREVIEW_MAX_CHARS,
  baseMimeType,
  previewKindFor,
  truncateForPreview,
} from "@/lib/artifact-preview";

/**
 * The routing decision behind "why did this file open here and not there".
 *
 * The cases below are not invented: every mime string in the first block was
 * read off the prod artifacts table on 2026-08-21, with its row count. That
 * matters most for the charset variants — markdown is stored BOTH as
 * `text/markdown` (47 rows) and `text/markdown; charset=utf-8` (97 rows), so a
 * matcher that compares the raw string works on a third of the corpus and
 * silently sends the rest to a browser tab. That is the shape of the bug this
 * whole change exists to fix, one layer down.
 */
describe("previewKindFor — real mime strings from prod", () => {
  const cases: Array<[string, string, string]> = [
    // [mime, name, expected kind]
    ["text/markdown", "argus-audit-2026-07-21.md", "markdown"],
    ["text/markdown; charset=utf-8", "spark-PRD-FINAL-v1.0.md", "markdown"],
    ["text/plain", "notes.txt", "text"],
    ["text/plain; charset=utf-8", "run.log", "text"],
    ["text/csv", "dryrun_before_after.csv", "text"],
    ["text/csv; charset=utf-8", "rescore.csv", "text"],
    ["text/x-go; charset=utf-8", "service.go", "text"],
    ["text/x-patch; charset=utf-8", "fix.patch", "text"],
    ["text/x-python", "scan.py", "text"],
    ["application/json", "payload.json", "text"],
    ["image/png", "screenshot.png", "external"],
    ["image/jpeg", "photo.jpg", "external"],
    ["image/webp", "shot.webp", "external"],
    ["application/pdf", "report.pdf", "external"],
    ["application/zip", "bundle.zip", "none"],
    ["application/vnd.ms-excel", "sheet.xls", "none"],
  ];

  it.each(cases)("%s (%s) → %s", (mime, name, expected) => {
    expect(previewKindFor({ mime_type: mime, name })).toBe(expected);
  });

  /**
   * `text/html` is the case with teeth. Six such artifacts sit on prod, and the
   * bucket is proxied from the app's own origin, so handing one to a new tab
   * runs uploaded script against the reader's session. It must land in "text",
   * where our renderer escapes it — never in "external".
   */
  it("routes text/html into the in-app viewer, never to a browser tab", () => {
    expect(previewKindFor({ mime_type: "text/html", name: "prd.html" })).toBe("text");
    expect(
      previewKindFor({ mime_type: "text/html; charset=utf-8", name: "prd.html" }),
    ).toBe("text");
  });

  /**
   * `application/octet-stream` is the single most common stored type (216 rows)
   * and is what a client sends when it did not bother to look. Falling back to
   * the extension is the difference between a readable document and a dead
   * Download button for a large slice of the corpus.
   */
  it("falls back to the filename when the stored type says nothing", () => {
    expect(
      previewKindFor({ mime_type: "application/octet-stream", name: "spec.md" }),
    ).toBe("markdown");
    expect(
      previewKindFor({ mime_type: "application/octet-stream", name: "server.log" }),
    ).toBe("text");
    // ...and still refuses when neither type nor name gives us anything.
    expect(
      previewKindFor({ mime_type: "application/octet-stream", name: "blob" }),
    ).toBe("none");
  });

  /**
   * The table above cannot prove charset handling, and I only found that by
   * mutating it: with `baseMimeType` broken so it never strips `; charset=utf-8`,
   * every row still passed — because each row's NAME ends in a known extension
   * and the fallback rescued it. The rows exercise the extension path while
   * appearing to exercise the mime path.
   *
   * These cases give the name nothing to say. The mime is then the only thing
   * that can classify the artifact, so the parameter-stripping is load-bearing
   * and a regression in it is visible.
   */
  it("classifies by mime alone, with a name that carries no extension", () => {
    expect(
      previewKindFor({ mime_type: "text/markdown; charset=utf-8", name: "report" }),
    ).toBe("markdown");
    expect(
      previewKindFor({ mime_type: "text/plain; charset=utf-8", name: "output" }),
    ).toBe("text");
    expect(
      previewKindFor({ mime_type: "text/csv; charset=utf-8", name: "export" }),
    ).toBe("text");
    expect(
      previewKindFor({ mime_type: "text/x-python; charset=utf-8", name: "script" }),
    ).toBe("text");
  });

  it("is not confused by an uppercase type or extension", () => {
    expect(previewKindFor({ mime_type: "TEXT/MARKDOWN", name: "X.MD" })).toBe(
      "markdown",
    );
  });
});

describe("baseMimeType", () => {
  it("strips parameters and case", () => {
    expect(baseMimeType("text/markdown; charset=utf-8")).toBe("text/markdown");
    expect(baseMimeType("TEXT/Plain")).toBe("text/plain");
  });

  it("survives an absent or empty type", () => {
    expect(baseMimeType(undefined)).toBe("");
    expect(baseMimeType("")).toBe("");
    expect(baseMimeType(";charset=utf-8")).toBe("");
  });
});

describe("truncateForPreview", () => {
  it("leaves an ordinary document alone and says so", () => {
    const doc = "# Title\n\nbody";
    const out = truncateForPreview(doc);
    expect(out.text).toBe(doc);
    expect(out.truncated).toBe(false);
    expect(out.omittedChars).toBe(0);
  });

  /**
   * The count is asserted, not just the flag. A viewer that cuts a file and
   * cannot say how much it withheld is the silent-truncation failure wearing a
   * banner.
   */
  it("reports exactly how much it withheld", () => {
    const out = truncateForPreview("x".repeat(150), 100);
    expect(out.text).toHaveLength(100);
    expect(out.truncated).toBe(true);
    expect(out.omittedChars).toBe(50);
  });

  it("does not truncate at exactly the limit (off-by-one guard)", () => {
    const out = truncateForPreview("x".repeat(100), 100);
    expect(out.truncated).toBe(false);
  });

  /**
   * The largest real text artifact today is a 715 KB CSV. It must be cut, and
   * the largest real markdown (71 KB) must not be — the threshold has to sit
   * between them or it is either useless or in the way.
   */
  it("cuts the largest real CSV and leaves the largest real markdown whole", () => {
    expect(truncateForPreview("x".repeat(715_082)).truncated).toBe(true);
    expect(truncateForPreview("x".repeat(71_319)).truncated).toBe(false);
    expect(PREVIEW_MAX_CHARS).toBeGreaterThan(71_319);
    expect(PREVIEW_MAX_CHARS).toBeLessThan(715_082);
  });
});
