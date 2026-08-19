/**
 * Links to a document from somewhere that is not a document — a task
 * description, a comment.
 *
 * ## Why this is an ordinary markdown link and not a scheme of its own
 *
 * The obvious precedent in this codebase is `relay://…`, which
 * MarkdownWithRelay splits out of the text before markdown is rendered at all.
 * That exists because a Team Relay document has no address in this app: there is
 * nothing to link to, so a pseudo-scheme is invented and a component is taught
 * to recognise it.
 *
 * Our own documents do have an address — `/w/:ws/p/:project/docs/:id` is a real
 * route. So the link is a real link, and everything that already renders
 * markdown renders it with no new code path, no second parser, and no component
 * that has to be remembered at every call site. A pseudo-scheme here would be a
 * fiction maintained for its own sake.
 */

/** The in-app path of a document. */
export function documentHref(
  workspaceSlug: string,
  projectSlug: string,
  documentId: string,
): string {
  return `/w/${workspaceSlug}/p/${projectSlug}/docs/${documentId}`;
}

/**
 * A label safe to put between `[` and `]`.
 *
 * Written for the hand-written renderer this app used to have, whose link label
 * was `[^\]]+` with no escape for a bracket: a title containing one produced NO
 * link at all, and silently — the writer saw their link vanish with no idea why.
 *
 * That renderer is gone and the dialect is now CommonMark, which CAN escape a
 * bracket, so the stripping is no longer forced. It is kept because the label is
 * inserted as a ProseMirror link mark and serialised by remark: what comes back
 * out would be `\[` escapes in the saved body, which is noise in a title for the
 * sake of two characters nobody misses. Widening it is a deliberate change with
 * a round-trip test behind it, not a cleanup.
 */
export function linkLabel(title: string): string {
  const stripped = title.replace(/[[\]]/g, "").trim();
  return stripped === "" ? "Untitled" : stripped;
}

/** The markdown a document link is inserted as. */
export function documentMarkdownLink(
  title: string,
  workspaceSlug: string,
  projectSlug: string,
  documentId: string,
): string {
  return `[${linkLabel(title)}](${documentHref(workspaceSlug, projectSlug, documentId)})`;
}

// ---------------------------------------------------------------------------
// The `[[` trigger
// ---------------------------------------------------------------------------

/**
 * The trigger. `[[` rather than `@`, which already means a person, and rather
 * than a toolbar-only affordance, because the writer's hands are on the
 * keyboard — the same reason the mention menu exists.
 *
 * It is also what Obsidian uses for exactly this, and this team ships an
 * Obsidian plugin, so it is the sequence the people writing these documents
 * already have in their fingers.
 */
export const DOC_LINK_TRIGGER = "[[";

/**
 * The query the writer has typed after `[[`, or null when the caret is not in a
 * trigger.
 *
 * The query stops at a newline or a closing bracket: a writer who typed `[[` and
 * then moved on to another line is not still picking a document, and leaving the
 * menu open across a paragraph break is how it ends up inserting a link where
 * the caret no longer is.
 */
export interface DocLinkTrigger {
  /** Index of the first `[` — where the replacement begins. */
  start: number;
  query: string;
}

export function findDocLinkTrigger(
  text: string,
  caret: number,
): DocLinkTrigger | null {
  const before = text.slice(0, caret);
  const start = before.lastIndexOf(DOC_LINK_TRIGGER);
  if (start === -1) return null;

  const query = before.slice(start + DOC_LINK_TRIGGER.length);
  if (/[\n\]]/.test(query)) return null;
  return { start, query };
}

// ---------------------------------------------------------------------------
// Matching
// ---------------------------------------------------------------------------

/** The least a document needs to be offered as a link target. */
export interface LinkableDocument {
  id: string;
  title: string;
}

/** How many suggestions the menu shows. More than this is a list, not a hint. */
export const DOC_LINK_SUGGESTION_LIMIT = 8;

/**
 * Documents whose title matches the query, best first.
 *
 * Case-insensitive substring, ranked by where the match starts: a title that
 * BEGINS with what was typed is what the writer meant far more often than one
 * that merely contains it, and with a limit of eight the difference decides what
 * is visible at all. Ties break alphabetically so the list does not reshuffle
 * between keystrokes for no visible reason.
 *
 * Deliberately client-side over the project's already-loaded document list.
 * Searching document CONTENT, and across scopes, is D9 — building half of it
 * here would be a second search with different answers to the same question.
 */
export function matchDocuments<T extends LinkableDocument>(
  documents: readonly T[],
  query: string,
  limit: number = DOC_LINK_SUGGESTION_LIMIT,
): T[] {
  const needle = query.trim().toLowerCase();
  const scored: { doc: T; at: number }[] = [];

  for (const doc of documents) {
    const at = doc.title.toLowerCase().indexOf(needle);
    // An empty query matches everything at position 0 — the menu opens on `[[`
    // with the project's documents, which is what makes it browsable as well as
    // searchable.
    if (at === -1) continue;
    scored.push({ doc, at });
  }

  // localeCompare, so the tie-break ignores case: "runbook archive" and
  // "Runbook for incidents" sort by their words, not by which one happens to be
  // capitalised. An alphabetical list that puts every capitalised title first
  // reads as unsorted.
  scored.sort((a, b) => {
    if (a.at !== b.at) return a.at - b.at;
    return a.doc.title.localeCompare(b.doc.title);
  });

  return scored.slice(0, limit).map((s) => s.doc);
}
