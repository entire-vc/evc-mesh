/**
 * The href protocol allow-list for document bodies.
 *
 * This is the schema-level replacement for the regex that used to live in
 * markdown-renderer.tsx (`/^(https?:\/\/|\/|#|mailto:)/i`, applied to a string
 * that was about to be interpolated into an HTML template). The old renderer
 * needed it because it built HTML by concatenation; this one needs it because a
 * ProseMirror `link` mark stores an href attribute that eventually becomes a
 * real, clickable anchor.
 *
 * Milkdown 7.22 ships its own sanitizer at the toDOM sink, and it is a good one.
 * We do not rely on it:
 *
 *  - its allow-list is wider than ours (it permits `tel:` and `ftp:`), and the
 *    set of schemes Mesh is willing to make clickable is our decision, not a
 *    transitive dependency's;
 *  - it sanitizes on the way *out* to the DOM, so an unsafe href still lives in
 *    the document and still round-trips back into the saved markdown. We
 *    sanitize on the way *in* as well, so `javascript:` never enters the doc.
 *
 * Both are applied (see schema-overrides.ts): parse-time, so the document never
 * holds a dangerous href, and toDOM-time, so a doc built by any other route
 * still cannot produce a live one.
 */

/** Schemes that may become a real, navigable href. Mirrors the old renderer. */
const SAFE_SCHEMES = new Set(["http:", "https:", "mailto:"]);

/**
 * Characters a browser discards while resolving a URL's scheme. They are the
 * standard way to smuggle a blocked scheme past a naive prefix test — a literal
 * tab inside `java<TAB>script:` still navigates as `javascript:`. Covers C0/C1
 * controls and space, zero-width characters, the unicode line/paragraph
 * separators, and the BOM.
 */
function isSchemeIgnorableChar(code: number): boolean {
  return (
    code <= 0x20 ||
    (code >= 0x7f && code <= 0xa0) ||
    (code >= 0x200b && code <= 0x200d) ||
    code === 0x2028 ||
    code === 0x2029 ||
    code === 0xfeff
  );
}

function stripSchemeIgnorableChars(input: string): string {
  let out = "";
  for (const char of input) {
    if (!isSchemeIgnorableChar(char.codePointAt(0) ?? 0)) out += char;
  }
  return out;
}

/**
 * True when `href` may be rendered as a live anchor.
 *
 * Scheme-relative URLs (`//evil.example`) are refused deliberately: they are not
 * the relative links the old regex meant to allow by `^\/`, they are absolute
 * URLs to another origin wearing a relative-looking prefix.
 */
export function isSafeHref(href: unknown): boolean {
  if (typeof href !== "string") return false;

  const trimmed = href.trim();
  if (!trimmed) return false;

  const normalized = stripSchemeIgnorableChars(trimmed);
  if (normalized.startsWith("//")) return false;

  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(normalized)?.[1];
  // No scheme at all: a path, a fragment or a query. Same-origin by definition.
  if (!scheme) return true;

  return SAFE_SCHEMES.has(`${scheme.toLowerCase()}:`);
}

/**
 * `href` when it is safe, otherwise the empty string.
 *
 * Empty rather than dropping the anchor: the link text is the user's content and
 * must survive, and an `href=""` is inert. This matches what the old renderer
 * did in spirit (it degraded the link to `text (url)`), without needing to
 * rewrite the document.
 */
export function sanitizeHref(href: unknown): string {
  return isSafeHref(href) ? (href as string).trim() : "";
}
