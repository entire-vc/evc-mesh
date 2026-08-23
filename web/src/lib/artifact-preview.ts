/**
 * Deciding how an artifact should be opened.
 *
 * Three outcomes, and the split is about who can render the bytes safely:
 *
 *   "markdown" / "text"  — we render it ourselves, inside the app.
 *   "external"           — the browser renders it natively and well (pdf, images),
 *                          so a new tab is genuinely the better experience.
 *   "none"               — nothing renders it; only Download makes sense.
 *
 * Text used to fall into "external", which is the bug this exists to fix: a
 * `.md` handed to a browser is shown as raw source, because browsers do not
 * render markdown. It also handed `text/html` to a tab on OUR origin — the
 * artifact bucket is proxied at `https://mesh.entire.host/s3/...`, same origin
 * as the app — so an uploaded page ran its own script with the reader's
 * session. Routing text through our own escaped renderer removes that path.
 */

/** Bytes above which the viewer shows a prefix and says so, rather than rendering everything. */
export const PREVIEW_MAX_BYTES = 512 * 1024;

/**
 * Characters rendered before truncation kicks in. Separate from PREVIEW_MAX_BYTES
 * because what costs the browser is DOM nodes built from characters, not the
 * transfer. Largest real text artifact today is a 715 KB CSV; largest markdown
 * is 71 KB, so ordinary documents never reach this.
 */
export const PREVIEW_MAX_CHARS = 200_000;

export type PreviewKind = "markdown" | "text" | "external" | "none";

/**
 * The bare type, without parameters and case-folded.
 *
 * Necessary, not cosmetic: prod stores the same type both ways — 47 artifacts as
 * `text/markdown` and 97 as `text/markdown; charset=utf-8`. A matcher comparing
 * the raw string would silently miss two thirds of the markdown in the product.
 */
export function baseMimeType(mimeType: string | undefined | null): string {
  if (!mimeType) return "";
  return (mimeType.split(";")[0] ?? "").trim().toLowerCase();
}

const MARKDOWN_MIME_TYPES = new Set([
  "text/markdown",
  "text/x-markdown",
  "application/markdown",
]);

const MARKDOWN_EXTENSIONS = /\.(md|markdown|mdown|mkd)$/i;

/**
 * Types that are text but arrive under an `application/*` label.
 * `text/*` is covered by prefix, so this list only needs the exceptions.
 */
const TEXTUAL_APPLICATION_MIME_TYPES = new Set([
  "application/json",
  "application/xml",
  "application/x-yaml",
  "application/yaml",
  "application/x-sh",
  "application/javascript",
  "application/x-ndjson",
  "application/sql",
]);

/** Extensions we treat as text even when the stored mime says nothing useful. */
const TEXT_EXTENSIONS =
  /\.(txt|log|csv|tsv|json|ya?ml|toml|ini|conf|xml|sql|patch|diff|go|py|ts|tsx|js|jsx|sh|rb|rs|java|c|h|cpp|css|scss|env)$/i;

/** Types the browser renders natively and better than we would. */
const BROWSER_NATIVE_MIME_PREFIXES = ["image/", "video/", "audio/"];
const BROWSER_NATIVE_MIME_TYPES = new Set(["application/pdf"]);

/**
 * Types no browser renders — a tab would download or show a save dialog, so the
 * "Open" affordance would be promising something it cannot do.
 */
const NON_PREVIEWABLE_MIME_PREFIXES = [
  "application/zip",
  "application/x-7z-compressed",
  "application/x-rar-compressed",
  "application/x-tar",
  "application/gzip",
  "application/x-gzip",
  "application/vnd.openxmlformats-officedocument",
  "application/vnd.ms-excel",
  "application/vnd.ms-powerpoint",
  "application/msword",
];

export interface PreviewableArtifact {
  name: string;
  mime_type: string;
}

export function previewKindFor(artifact: PreviewableArtifact): PreviewKind {
  const mime = baseMimeType(artifact.mime_type);
  const name = artifact.name ?? "";

  // Name first for markdown: `application/octet-stream` is the single most
  // common stored type on prod (216 artifacts) and plenty of them are documents
  // uploaded by a client that did not bother to set a type.
  if (MARKDOWN_MIME_TYPES.has(mime) || MARKDOWN_EXTENSIONS.test(name)) {
    return "markdown";
  }

  if (BROWSER_NATIVE_MIME_TYPES.has(mime)) return "external";
  if (BROWSER_NATIVE_MIME_PREFIXES.some((p) => mime.startsWith(p))) return "external";

  if (mime.startsWith("text/") || TEXTUAL_APPLICATION_MIME_TYPES.has(mime)) {
    return "text";
  }

  if (NON_PREVIEWABLE_MIME_PREFIXES.some((p) => mime.startsWith(p))) return "none";

  // Typeless-but-named: trust the extension rather than refuse to show anything.
  if (TEXT_EXTENSIONS.test(name)) return "text";

  return "none";
}

export interface TruncationResult {
  text: string;
  truncated: boolean;
  /** Characters withheld. Zero when nothing was cut. */
  omittedChars: number;
}

/**
 * Cut for display, and report the cut.
 *
 * The report is the point. A viewer that silently shows the first part of a
 * document is worse than one that refuses, because the reader has no way to
 * know the tail existed.
 */
export function truncateForPreview(
  text: string,
  maxChars: number = PREVIEW_MAX_CHARS,
): TruncationResult {
  if (text.length <= maxChars) {
    return { text, truncated: false, omittedChars: 0 };
  }
  return {
    text: text.slice(0, maxChars),
    truncated: true,
    omittedChars: text.length - maxChars,
  };
}
