import { Fragment, type ReactNode } from "react";
import { Link } from "react-router";
import type { MentionEntry } from "@/components/markdown-view";

/**
 * Plain text with its `@mentions` picked out.
 *
 * Deliberately NOT the markdown renderer. A document comment is stored and
 * displayed as plain text with its line breaks kept, and switching it to
 * markdown to gain mention highlighting would silently reformat every comment
 * already written — asterisks becoming emphasis, a leading `#` becoming a
 * heading. This adds exactly the one thing that was missing.
 *
 * A slug nobody recognises stays plain text, and that is the read-side half of
 * the promise the server makes on write: an unresolvable mention is refused
 * outright, so anything that does reach this renderer and still fails to
 * highlight is visibly not a mention rather than a silent nothing. (Names that
 * stopped resolving after the fact — somebody who left — land here too, and
 * reading as plain text is the honest rendering of "this addresses nobody".)
 *
 * The pattern is the one markdown-renderer.tsx uses, character for character, so
 * the same text highlights the same way in a task comment and in a document one.
 */
const MENTION_PATTERN =
  /(?<![a-zA-Z0-9_])@([a-z0-9][a-z0-9-]{1,40}[a-z0-9]?)(?![a-z0-9-])/g;

export function MentionText({
  text,
  mentionables,
  wsSlug,
  className,
}: {
  text: string;
  mentionables?: Map<string, MentionEntry>;
  wsSlug?: string;
  className?: string;
}) {
  return <span className={className}>{splitMentions(text, mentionables, wsSlug)}</span>;
}

function splitMentions(
  text: string,
  mentionables?: Map<string, MentionEntry>,
  wsSlug?: string,
): ReactNode[] {
  if (!mentionables || !wsSlug) return [text];

  const out: ReactNode[] = [];
  let last = 0;
  // A fresh lastIndex per call: the regex is module-level and /g is stateful, so
  // reusing it across renders without resetting would skip matches in every
  // other comment.
  MENTION_PATTERN.lastIndex = 0;

  for (
    let match = MENTION_PATTERN.exec(text);
    match !== null;
    match = MENTION_PATTERN.exec(text)
  ) {
    const slug = match[1];
    const entry = slug ? mentionables.get(slug) : undefined;
    if (!entry || !slug) continue;

    if (match.index > last) out.push(text.slice(last, match.index));
    out.push(
      <Link
        key={`${match.index}-${slug}`}
        to={`/w/${wsSlug}/team/${entry.kind}/${slug}`}
        className="mention-link inline-block whitespace-nowrap rounded bg-blue-50 px-1 py-0.5 text-blue-600 hover:underline dark:bg-blue-900/30 dark:text-blue-400"
        aria-label={`Open team profile for @${slug}`}
      >
        @{slug}
      </Link>,
    );
    last = match.index + match[0].length;
  }

  if (last === 0) return [text];
  if (last < text.length) out.push(text.slice(last));
  return out.map((node, i) => <Fragment key={i}>{node}</Fragment>);
}
