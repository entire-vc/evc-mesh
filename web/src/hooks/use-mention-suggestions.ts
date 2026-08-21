import { useEffect, useState } from "react";
import { getMentionables } from "@/lib/api";
import type { Mentionable } from "@/types";

/**
 * What the `@` menu shows, for a given query.
 *
 * Lifted out of comment-list.tsx, where it was five pieces of local state and a
 * hand-rolled debounce ref. It moved because the editor that owns the `@`
 * trigger is now shared: before this, mentions could be typed with autocomplete
 * in a comment and only from memory in a task description — the same gap the
 * `[[` menu was built to avoid.
 *
 * `query` is null when no menu is open; nothing is fetched then.
 */

const MENTION_DEBOUNCE_MS = 150;

export interface UseMentionSuggestions {
  suggestions: Mentionable[];
}

export function useMentionSuggestions(
  workspaceId: string | undefined,
  query: string | null,
): UseMentionSuggestions {
  const [suggestions, setSuggestions] = useState<Mentionable[]>([]);

  useEffect(() => {
    if (query === null || !workspaceId) {
      setSuggestions([]);
      return;
    }
    let cancelled = false;
    const timer = setTimeout(() => {
      getMentionables(workspaceId, query)
        .then((items) => {
          if (!cancelled) setSuggestions(items ?? []);
        })
        .catch(() => {
          if (!cancelled) setSuggestions([]);
        });
    }, MENTION_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [workspaceId, query]);

  return { suggestions };
}
