import { useEffect, useState } from "react";
import {
  DOC_LINK_SUGGESTION_LIMIT,
  matchDocuments,
} from "@/lib/docs/doc-link";
import {
  type DocumentSearchHit,
  type SearchScope,
  searchDocuments,
  searchRelayDocuments,
} from "@/lib/docs/document-search";
import { fetchLinkableDocuments } from "@/lib/docs/linkable-documents";

/**
 * What the `[[` menu shows, for a given query.
 *
 * This is the half of the old useDocLinkPicker that had nothing to do with a
 * textarea: loading the project's documents, asking the server, and merging the
 * two lists. The other half — reading `selectionStart`, splicing a string,
 * restoring the caret — could not survive the move to a rich-text editor and
 * did not need to: where the trigger IS and what to do with the choice are now
 * the editor's business (lib/milkdown/suggestion.ts).
 *
 * Splitting it this way is what let the menu keep working identically while the
 * surface underneath it changed completely.
 *
 * `query` is null when no menu is open — nothing is fetched then.
 */

/** How long after the last keystroke the server is asked. */
const SEARCH_DEBOUNCE_MS = 200;

/**
 * Server hits first, then title matches not already in the list.
 *
 * Capped at the same number the local matcher uses, so the menu stays a hint
 * rather than becoming a page of results.
 */
function mergeSuggestions(
  hits: readonly DocumentSearchHit[],
  local: readonly DocumentSearchHit[],
): DocumentSearchHit[] {
  const seen = new Set(hits.map((h) => h.id));
  const merged = [...hits];
  for (const doc of local) {
    if (seen.has(doc.id)) continue;
    seen.add(doc.id);
    merged.push(doc);
  }
  return merged.slice(0, DOC_LINK_SUGGESTION_LIMIT);
}

export interface UseDocSuggestions {
  suggestions: DocumentSearchHit[];
  scope: SearchScope;
  setScope: (scope: SearchScope) => void;
  loading: boolean;
}

export function useDocSuggestions(
  projectId: string | undefined,
  query: string | null,
): UseDocSuggestions {
  const [documents, setDocuments] = useState<DocumentSearchHit[]>([]);
  const [hits, setHits] = useState<DocumentSearchHit[] | null>(null);
  const [scope, setScope] = useState<SearchScope>("docs");
  const [loading, setLoading] = useState(false);

  const open = query !== null;

  // The project's documents, loaded the first time a menu opens and cached
  // after. Nothing is fetched until someone types `[[`.
  useEffect(() => {
    if (!open || !projectId) return;
    let cancelled = false;
    fetchLinkableDocuments(projectId)
      .then((items) => {
        if (!cancelled) {
          setDocuments(items.map((d) => ({ ...d, snippet: "", snippetIsMatch: false })));
        }
      })
      // A failed fetch leaves the menu empty, which reads as "no documents
      // match". That is the honest state: we do not know of any.
      .catch(() => {
        if (!cancelled) setDocuments([]);
      });
    return () => {
      cancelled = true;
    };
  }, [open, projectId]);

  // Two sources, MERGED — not one replacing the other.
  //
  // The local title list matches substrings, so `[[run` finds "Deploy runbook"
  // the moment it is typed. The server matches whole tokens (plainto_tsquery
  // over a 'simple' index), so `run` does NOT find "runbook" there — it answers
  // a different question: where a phrase appears INSIDE a document.
  //
  // An earlier version showed `hits ?? local`, which looked right and was not:
  // as soon as the server answered with zero results the title matches
  // disappeared, so typing the first few letters of a document's name — the
  // most common thing anyone does here — stopped finding it.
  const localMatches = open && scope === "docs" ? matchDocuments(documents, query) : [];
  const suggestions = !open ? [] : mergeSuggestions(hits ?? [], localMatches);

  // Ask the server. Debounced, and only once there is something to ask about:
  // an empty query is refused by the API, and firing it on every `[[` would put
  // a rejected request behind a keystroke.
  useEffect(() => {
    if (!open || !projectId || (query ?? "").trim() === "") {
      setHits(null);
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    const timer = setTimeout(() => {
      const search = scope === "relay" ? searchRelayDocuments : searchDocuments;
      search(projectId, query)
        .then((found) => {
          if (!cancelled) setHits(found);
        })
        // A failed search leaves the local title matches in place rather than
        // emptying the menu: degraded is better than blank, and blank would
        // read as "there is no such document".
        .catch(() => {
          if (!cancelled) setHits(null);
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    }, SEARCH_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [open, projectId, query, scope]);

  return { suggestions, scope, setScope, loading };
}
