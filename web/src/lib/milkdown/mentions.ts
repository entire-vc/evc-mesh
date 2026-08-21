export interface MentionEntry {
  kind: "agent" | "user";
}

/**
 * `@slug` in a task description or a comment, turned into a link to the profile.
 *
 * Applied to rendered DOM rather than to the markdown, and that is the whole
 * design. A mention is not a markdown construct: it has no syntax to parse, it
 * is a bare word in a sentence. The deleted renderers reached it by running a
 * regex over an HTML string mid-build, which is why they also had to hand-roll
 * placeholder juggling to keep `@name` inside backticks from being linked.
 *
 * Here the document has already been parsed, so "inside code" is a fact about
 * the tree instead of a guess about a string, and the replacement builds real
 * elements with `textContent` — the slug can never be markup, whatever it says.
 *
 * The document itself is untouched: nothing about this survives into the saved
 * markdown, so a mention round-trips as the plain text it always was.
 */

/**
 * Mirrors the deleted renderer exactly: lowercase, 3-42 characters, and not
 * adjacent to a word character on either side, so an e-mail address and a
 * `foo@bar` handle do not become mentions.
 */
const MENTION_RE =
  /(?<![a-zA-Z0-9_])@([a-z0-9][a-z0-9-]{1,40}[a-z0-9]?)(?![a-z0-9-])/g;

/** Elements whose text is never a mention, however it is spelled. */
const OPAQUE = new Set(["CODE", "PRE", "A"]);

const MENTION_CLASS =
  "mention-link inline-block whitespace-nowrap rounded px-1 py-0.5 text-blue-600 bg-blue-50 hover:underline dark:text-blue-400 dark:bg-blue-900/30";

function isOpaque(node: Node): boolean {
  for (let el = node.parentElement; el; el = el.parentElement) {
    if (OPAQUE.has(el.tagName)) return true;
  }
  return false;
}

/**
 * Link every known `@slug` inside `root`, in place.
 *
 * Unknown slugs are deliberately left as text: a mention that points at nobody
 * is a word someone typed, and rendering it as a dead link would claim a person
 * exists. Same rule the old renderer had.
 */
export function linkMentions(
  root: ParentNode,
  mentionables: Map<string, MentionEntry> | undefined,
  wsSlug: string | undefined,
): void {
  if (!mentionables?.size || !wsSlug) return;

  const doc = root.ownerDocument ?? document;
  const walker = doc.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const texts: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if (n instanceof Text && !isOpaque(n)) texts.push(n);
  }

  for (const text of texts) {
    const value = text.data;
    MENTION_RE.lastIndex = 0;
    let match = MENTION_RE.exec(value);
    if (!match) continue;

    const out = doc.createDocumentFragment();
    let cursor = 0;
    while (match) {
      const slug = match[1] ?? "";
      const entry = mentionables.get(slug);
      if (entry) {
        if (match.index > cursor) {
          out.appendChild(doc.createTextNode(value.slice(cursor, match.index)));
        }
        const a = doc.createElement("a");
        a.className = MENTION_CLASS;
        a.href = `/w/${wsSlug}/team/${entry.kind}/${slug}`;
        a.dataset.mentionSlug = slug;
        a.setAttribute("aria-label", `Open team profile for @${slug}`);
        // textContent, not innerHTML: the slug is content, and content is never
        // markup here.
        a.textContent = `@${slug}`;
        out.appendChild(a);
        cursor = match.index + match[0].length;
      }
      match = MENTION_RE.exec(value);
    }
    if (cursor === 0) continue;
    if (cursor < value.length) {
      out.appendChild(doc.createTextNode(value.slice(cursor)));
    }
    text.replaceWith(out);
  }
}
