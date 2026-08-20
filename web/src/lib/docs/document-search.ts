import { api } from "@/lib/api";
import type { LinkableDocument } from "@/lib/docs/doc-link";

/**
 * Searching documents by content, and choosing where to search.
 *
 * D8 gave the `[[` menu a list of the project's documents matched on TITLE, out
 * of the list it had already loaded. That is the right thing when the writer
 * knows the name. It is useless when they remember a phrase from inside a
 * document, which is the case this adds: the query goes to the server, where the
 * body has been indexed, and comes back with the fragment that matched.
 */

/** Where a search looks. */
export type SearchScope = "docs" | "relay";

/** A hit, from either scope, in the one shape the menu renders. */
export interface DocumentSearchHit extends LinkableDocument {
  /**
   * A fragment of the document with the matched words marked, or the document's
   * opening when the match lies past the window the server quoted from.
   */
  snippet: string;
  /**
   * Which of those two `snippet` is. Highlighting an unmatched opening as though
   * it were the reason the document matched is a small lie that makes the whole
   * result list untrustworthy, so the flag travels with the text.
   */
  snippetIsMatch: boolean;
  /** Set for a Team Relay hit; the docs scope links by id. */
  relayUrl?: string;
}

/**
 * The markers the API wraps matched words in.
 *
 * Private-use codepoints, chosen so nothing a writer can type is mistaken for
 * them — a document may well contain `<b>` or `**`, and treating the author's own
 * text as our highlight would mark the wrong words.
 *
 * These must stay equal to searchMarkStart / searchMarkEnd in
 * internal/repository/postgres/document_repo.go. Two constants, one meaning, and
 * no way for either side to import the other — so each side pins its own value
 * in a test, and a drift shows up as "nothing is ever highlighted" rather than as
 * a crash. Written as escapes rather than as the literal characters because a
 * private-use codepoint pasted into source is invisible and does not survive
 * every editor.
 */
export const MATCH_START = "\uE000";
export const MATCH_END = "\uE001";

/** A snippet split into plain and matched runs, for rendering without dangerouslySetInnerHTML. */
export interface SnippetPart {
  text: string;
  match: boolean;
}

export function splitSnippet(snippet: string): SnippetPart[] {
  const parts: SnippetPart[] = [];
  let rest = snippet;
  while (rest.length > 0) {
    const start = rest.indexOf(MATCH_START);
    if (start === -1) {
      parts.push({ text: rest, match: false });
      break;
    }
    if (start > 0) parts.push({ text: rest.slice(0, start), match: false });
    const end = rest.indexOf(MATCH_END, start);
    if (end === -1) {
      // An unbalanced marker is a truncated snippet, not a match: rendering the
      // remainder as highlighted would mark text the query never touched.
      parts.push({ text: rest.slice(start + MATCH_START.length), match: false });
      break;
    }
    parts.push({ text: rest.slice(start + MATCH_START.length, end), match: true });
    rest = rest.slice(end + MATCH_END.length);
  }
  return parts;
}

interface ApiHit {
  id: string;
  title: string;
  snippet: string;
  snippet_is_match: boolean;
}

/** Search this project's own documents, by title and content. */
export async function searchDocuments(
  projectId: string,
  query: string,
  limit = 8,
): Promise<DocumentSearchHit[]> {
  const res = await api<{ items: ApiHit[] | null }>(
    `/api/v1/projects/${projectId}/documents/search`,
    { params: { q: query, limit } },
  );
  return (res.items ?? []).map((hit) => ({
    id: hit.id,
    title: hit.title,
    snippet: hit.snippet,
    snippetIsMatch: hit.snippet_is_match,
  }));
}

interface RelayHit {
  title: string;
  path: string;
  relay_url: string;
}

/**
 * Search the project's Team Relay vault.
 *
 * The endpoint predates this unit — it is what RelayDocPicker has always used —
 * so the scope is wired to it rather than reimplemented. It returns no snippet,
 * and the path stands in for one: it is what tells two documents with the same
 * name apart, which is the job a snippet does in the other scope.
 */
export async function searchRelayDocuments(
  projectId: string,
  query: string,
  limit = 8,
): Promise<DocumentSearchHit[]> {
  const res = await api<{ docs: RelayHit[] | null }>(
    `/api/v1/projects/${projectId}/tr/search`,
    { params: { q: query, limit } },
  );
  return (res.docs ?? []).map((hit) => ({
    // Team Relay documents have no id in this system; the relay URL is their
    // identity, and it is also what gets inserted.
    id: hit.relay_url,
    title: hit.title,
    snippet: hit.path,
    snippetIsMatch: false,
    relayUrl: hit.relay_url,
  }));
}
