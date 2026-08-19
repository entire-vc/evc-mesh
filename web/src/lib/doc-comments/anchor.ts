/**
 * Turning a reader's text selection into an anchor the API will store, and a
 * stored anchor back into a range on screen.
 *
 * ## The two coordinate spaces, and why the anchor straddles them
 *
 * The document exists twice: as the markdown the API stores, and as the prose
 * ProseMirror renders from it. `**bold**` is eight characters in one and four in
 * the other, so a position in one is not a position in the other.
 *
 * The stored anchor deliberately uses both:
 *
 * - `start` / `end` are byte offsets into the **markdown**, because that is what
 *   the API documents them to be and what any other client — or the server — has
 *   to be able to read them as.
 * - `exact` / `prefix` / `suffix` are taken from the **rendered** text, because
 *   their job is to find the quote again in what the reader is looking at, and a
 *   quote whose context strings live in a different space than the quote itself
 *   cannot disambiguate anything. In plain prose, which is nearly all of it, the
 *   two spaces are character-for-character identical anyway; the distinction only
 *   bites across inline markup, and there it is the rendered text that still
 *   matches the page.
 *
 * That split is the one deliberate choice in this file. Everything else follows
 * from it: `locateQuoteInSource` crosses from rendered text into markdown
 * (markup-tolerant, because the needle has none and the haystack does), and
 * `locateQuoteInRendered` stays inside rendered space (verbatim, because both
 * sides are the same kind of text).
 *
 * ## When the quote cannot be placed
 *
 * `locateQuoteInSource` returning null is a legitimate outcome, not a bug to
 * paper over. The API's third legal anchor state is "quote, offsets NULL" —
 * orphaned — and writing that down is strictly better than inventing offsets
 * that point at the wrong words. The caller warns and stores the quote.
 */

import type { DocumentCommentAnchor } from "@/types";
import { byteLength, utf16ToByteOffset } from "./offsets";

/** How much neighbouring text is kept either side of the quote. */
export const CONTEXT_LENGTH = 48;

/** Longest quote we will store. The API refuses anything over 2000 bytes. */
export const MAX_QUOTE_LENGTH = 500;

/** Above this, the markup-tolerant scan is skipped — see locateQuoteInSource. */
const TOLERANT_SCAN_MAX_SOURCE = 200_000;

/** A [start, end) span in UTF-16 indices. */
export interface CharSpan {
  start: number;
  end: number;
}

// ---------------------------------------------------------------------------
// Scoring
// ---------------------------------------------------------------------------

/** Length of the longest common suffix of `a` and `b`. */
function commonSuffixLength(a: string, b: string): number {
  let n = 0;
  while (n < a.length && n < b.length && a.charAt(a.length - 1 - n) === b.charAt(b.length - 1 - n)) {
    n++;
  }
  return n;
}

/** Length of the longest common prefix of `a` and `b`. */
function commonPrefixLength(a: string, b: string): number {
  let n = 0;
  while (n < a.length && n < b.length && a.charAt(n) === b.charAt(n)) n++;
  return n;
}

/**
 * How well a candidate occupying [start, end) in `haystack` matches the context
 * the anchor remembers. Higher is better; zero means "the quote is here but
 * nothing around it agrees", which is still a candidate when it is the only one.
 */
function contextScore(
  haystack: string,
  start: number,
  end: number,
  prefix: string,
  suffix: string,
): number {
  const before = haystack.slice(Math.max(0, start - prefix.length), start);
  const after = haystack.slice(end, end + suffix.length);
  return commonSuffixLength(before, prefix) + commonPrefixLength(after, suffix);
}

/**
 * Pick the best of several candidate spans: most agreeing context wins, and a
 * tie is broken by whichever sits nearest the position we expected.
 */
function bestCandidate(
  haystack: string,
  candidates: CharSpan[],
  prefix: string,
  suffix: string,
  hint: number | null,
): CharSpan | null {
  const first = candidates[0];
  if (!first) return null;
  if (candidates.length === 1) return first;

  let best = first;
  let bestScore = -1;
  let bestDistance = Number.POSITIVE_INFINITY;

  for (const candidate of candidates) {
    const score = contextScore(haystack, candidate.start, candidate.end, prefix, suffix);
    const distance = hint === null ? 0 : Math.abs(candidate.start - hint);
    if (score > bestScore || (score === bestScore && distance < bestDistance)) {
      best = candidate;
      bestScore = score;
      bestDistance = distance;
    }
  }
  return best;
}

// ---------------------------------------------------------------------------
// Rendered space -> rendered space (verbatim)
// ---------------------------------------------------------------------------

/** Every verbatim occurrence of `needle` in `haystack`. */
function allOccurrences(haystack: string, needle: string): CharSpan[] {
  const spans: CharSpan[] = [];
  if (!needle) return spans;
  let from = 0;
  for (;;) {
    const at = haystack.indexOf(needle, from);
    if (at === -1) break;
    spans.push({ start: at, end: at + needle.length });
    from = at + 1;
    // A pathological document (the same short quote thousands of times) must
    // not turn one render into a quadratic scan.
    if (spans.length > 500) break;
  }
  return spans;
}

/**
 * Find the anchor's quote in the text currently on screen.
 *
 * `hintFraction` is where the anchor's byte offset says it should be, expressed
 * as a fraction of the document, because the rendered text is a different length
 * than the markdown and the byte offset cannot be used directly. It only breaks
 * ties between equally-well-contextualised repeats of the same phrase.
 *
 * Returns null when the quote is not on screen at all — the document has been
 * edited since, and the thread is shown detached rather than highlighted
 * somewhere plausible-looking and wrong.
 */
export function locateQuoteInRendered(
  rendered: string,
  anchor: Pick<DocumentCommentAnchor, "exact" | "prefix" | "suffix">,
  hintFraction: number | null = null,
): CharSpan | null {
  const exact = anchor.exact;
  if (!exact) return null;

  const candidates = allOccurrences(rendered, exact);
  const hint =
    hintFraction === null ? null : Math.round(hintFraction * rendered.length);
  return bestCandidate(rendered, candidates, anchor.prefix, anchor.suffix, hint);
}

// ---------------------------------------------------------------------------
// Rendered space -> markdown space (markup-tolerant)
// ---------------------------------------------------------------------------

/**
 * Characters the markdown may carry that the rendered text does not. Skipping
 * them is what lets a selection of "the bold word" match `the **bold** word`.
 */
const MARKUP_CHARS = new Set(["*", "_", "`", "~", "\\", "[", "#", ">", "|"]);

/** A link's tail — `](https://example.com "title")` — skipped as one unit. */
const LINK_TAIL = /^\]\([^()\s]*(?:\s+"[^"]*")?\)/;

const WHITESPACE = /\s/;

/**
 * Longest run of markdown-only characters allowed between two characters of the
 * quote. Enough for `**`, a fenced backtick run or a short link tail; short
 * enough that the scan cannot wander across a paragraph and call it a match.
 */
const MAX_SKIP_RUN = 48;

/**
 * Try to match `needle` starting exactly at `from` in `source`, allowing the
 * source to contain markdown syntax the rendered text does not, and allowing
 * whitespace runs to differ (a markdown soft line break renders as one space).
 *
 * Returns the end index in `source`, or -1.
 */
function tolerantMatchAt(source: string, needle: string, from: number): number {
  let i = 0; // into needle
  let j = from; // into source

  while (i < needle.length) {
    if (j >= source.length) return -1;

    const nc = needle.charAt(i);
    const sc = source.charAt(j);

    if (nc === sc) {
      i++;
      j++;
      continue;
    }

    // Whitespace is compared as runs: any run matches any run.
    if (WHITESPACE.test(nc) && WHITESPACE.test(sc)) {
      while (i < needle.length && WHITESPACE.test(needle.charAt(i))) i++;
      while (j < source.length && WHITESPACE.test(source.charAt(j))) j++;
      continue;
    }

    // Skip a run of source-only markdown before giving up on this start.
    let skipped = 0;
    while (j < source.length && skipped < MAX_SKIP_RUN) {
      const tail = LINK_TAIL.exec(source.slice(j));
      if (tail) {
        j += tail[0].length;
        skipped += tail[0].length;
        continue;
      }
      if (!MARKUP_CHARS.has(source.charAt(j))) break;
      j++;
      skipped++;
    }
    if (skipped === 0) return -1;
  }

  return j;
}

/**
 * Locate a rendered-text quote inside the markdown it was rendered from.
 *
 * Verbatim first — that is the case for all plain prose, and it is exact. The
 * markup-tolerant scan is the fallback for a selection that crossed a bold run,
 * a link or a soft line break, where the rendered characters exist in the source
 * with syntax interleaved.
 *
 * `hint` is a rough UTF-16 index into `source`, used only to break ties.
 */
export function locateQuoteInSource(
  source: string,
  exact: string,
  prefix: string,
  suffix: string,
  hint: number | null = null,
): CharSpan | null {
  if (!exact) return null;

  const verbatim = allOccurrences(source, exact);
  if (verbatim.length > 0) {
    return bestCandidate(source, verbatim, prefix, suffix, hint);
  }

  if (source.length > TOLERANT_SCAN_MAX_SOURCE) return null;

  const first = exact.charAt(0);
  const candidates: CharSpan[] = [];
  for (let start = 0; start < source.length; start++) {
    // Only start where the first character could match — either literally, or
    // as the head of a whitespace run.
    const sc = source.charAt(start);
    if (sc !== first && !(WHITESPACE.test(first) && WHITESPACE.test(sc))) continue;
    const end = tolerantMatchAt(source, exact, start);
    if (end !== -1) {
      candidates.push({ start, end });
      if (candidates.length > 200) break;
    }
  }

  return bestCandidate(source, candidates, prefix, suffix, hint);
}

// ---------------------------------------------------------------------------
// Building the anchor
// ---------------------------------------------------------------------------

export interface BuiltAnchor {
  anchor: Omit<DocumentCommentAnchor, "orphaned">;
  /**
   * True when the quote could not be placed in the markdown, so the anchor is
   * being stored without offsets and the comment will come back orphaned. The
   * composer says so rather than letting it be a surprise later.
   */
  unplaceable: boolean;
}

/** Trim a selection's edges without dragging the indices out of step. */
function trimSpan(text: string, span: CharSpan): CharSpan {
  let { start, end } = span;
  while (start < end && WHITESPACE.test(text.charAt(start))) start++;
  while (end > start && WHITESPACE.test(text.charAt(end - 1))) end--;
  return { start, end };
}

/**
 * Build the anchor for a selection of the rendered document.
 *
 * @param source   the document's markdown, exactly as the API holds it
 * @param rendered the flattened text of what is on screen
 * @param span     the selection, in `rendered`'s UTF-16 indices
 */
export function buildAnchorFromSelection(
  source: string,
  rendered: string,
  span: CharSpan,
): BuiltAnchor | null {
  const trimmed = trimSpan(rendered, span);
  if (trimmed.end <= trimmed.start) return null;

  // A quote is an identifier, not a copy of the document. Truncating here rather
  // than letting the API refuse the write at 2000 bytes — which a Cyrillic
  // document would hit at half the characters an English one does.
  const end = Math.min(trimmed.end, trimmed.start + MAX_QUOTE_LENGTH);
  const exact = rendered.slice(trimmed.start, end);
  const prefix = rendered.slice(Math.max(0, trimmed.start - CONTEXT_LENGTH), trimmed.start);
  const suffix = rendered.slice(end, end + CONTEXT_LENGTH);

  // Where in the markdown we expect to land, as a rough proportion — the two
  // strings are different lengths, so this is a tie-breaker and nothing more.
  const hint =
    rendered.length > 0
      ? Math.round((trimmed.start / rendered.length) * source.length)
      : null;

  const inSource = locateQuoteInSource(source, exact, prefix, suffix, hint);

  if (!inSource) {
    return {
      anchor: { exact, prefix, suffix, start: null, end: null },
      unplaceable: true,
    };
  }

  return {
    anchor: {
      exact,
      prefix,
      suffix,
      start: utf16ToByteOffset(source, inSource.start),
      end: utf16ToByteOffset(source, inSource.end),
    },
    unplaceable: false,
  };
}

/**
 * Where the anchor claims to sit, as a fraction of the document, for
 * `locateQuoteInRendered`'s tie-break. Null when the anchor is orphaned.
 */
export function anchorHintFraction(
  source: string,
  anchor: Pick<DocumentCommentAnchor, "start">,
): number | null {
  if (anchor.start === null || anchor.start === undefined) return null;
  const total = byteLength(source);
  if (total <= 0) return null;
  return Math.max(0, Math.min(1, anchor.start / total));
}
