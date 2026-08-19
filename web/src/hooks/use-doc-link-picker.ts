import { type KeyboardEvent, type RefObject, useCallback, useEffect, useRef, useState } from "react";
import {
  type DocLinkTrigger,
  applyDocLinkInsertion,
  documentMarkdownLink,
  linkLabel,
  findDocLinkTrigger,
  matchDocuments,
} from "@/lib/docs/doc-link";
import {
  type DocumentSearchHit,
  type SearchScope,
  searchDocuments,
  searchRelayDocuments,
} from "@/lib/docs/document-search";
import { fetchLinkableDocuments } from "@/lib/docs/linkable-documents";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";

/**
 * Typing `[[` in a task description or a comment, and getting a link to a
 * document out of it.
 *
 * One hook for all three surfaces — the comment box, the description editor and
 * the markdown editor — because the alternative is what this codebase already
 * has for mentions: an inline menu implemented once, in comments, and absent
 * from the other two. That is the gap this unit exists to close, and closing it
 * by writing the same 60 lines three more times would reopen it the next time
 * someone adds a fourth editor.
 *
 * The caller owns its textarea and its markup; this owns when the menu is open,
 * what is in it, and what the text becomes.
 */

export interface UseDocLinkPicker {
  /** Non-null while the menu should be shown. */
  trigger: DocLinkTrigger | null;
  suggestions: DocumentSearchHit[];
  scope: SearchScope;
  setScope: (scope: SearchScope) => void;
  loading: boolean;
  activeIndex: number;
  setActiveIndex: (index: number) => void;
  /** Call from the textarea's onChange, AFTER the value has been set. */
  onValueChange: (value: string, caret: number) => void;
  /** Call from the textarea's onKeyDown, first. Returns true when it handled the key. */
  onKeyDown: (e: KeyboardEvent<HTMLTextAreaElement>) => boolean;
  pick: (doc: DocumentSearchHit) => void;
  close: () => void;
}

/** How long after the last keystroke the server is asked. */
const SEARCH_DEBOUNCE_MS = 200;

export function useDocLinkPicker(
  projectId: string | undefined,
  value: string,
  onChange: (next: string) => void,
  textareaRef: RefObject<HTMLTextAreaElement | null>,
): UseDocLinkPicker {
  const [trigger, setTrigger] = useState<DocLinkTrigger | null>(null);
  const [documents, setDocuments] = useState<DocumentSearchHit[]>([]);
  const [hits, setHits] = useState<DocumentSearchHit[] | null>(null);
  const [scope, setScope] = useState<SearchScope>("docs");
  const [loading, setLoading] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);

  const projects = useProjectStore((s) => s.projects);
  const currentProject = useProjectStore((s) => s.currentProject);
  const currentWorkspace = useWorkspaceStore((s) => s.currentWorkspace);

  // Read by the insertion, which must not close over the value of the render the
  // menu happened to open on.
  const valueRef = useRef(value);
  valueRef.current = value;
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  const close = useCallback(() => {
    setTrigger(null);
    setActiveIndex(0);
  }, []);

  // The project's documents, loaded the first time a menu opens and cached for
  // 30s after. Nothing is fetched until someone types `[[`.
  useEffect(() => {
    if (!trigger || !projectId) return;
    let cancelled = false;
    fetchLinkableDocuments(projectId)
      .then((items) => {
        if (!cancelled) {
          setDocuments(
            items.map((d) => ({ ...d, snippet: "", snippetIsMatch: false })),
          );
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
  }, [trigger, projectId]);

  // Two sources, and which one answers is the whole point of this unit.
  //
  // The already-loaded title list answers instantly and is what makes the menu
  // browsable the moment `[[` is typed — a picker that shows nothing until a
  // request returns feels broken. The server answers the question titles cannot:
  // where a phrase appears INSIDE a document.
  //
  // So the local list is shown first and replaced by hits when they arrive. The
  // Team Relay scope has no local list at all; there the menu is empty until the
  // search returns, which is honest — we genuinely know nothing about that vault
  // until we ask.
  const suggestions = !trigger
    ? []
    : hits ?? (scope === "docs" ? matchDocuments(documents, trigger.query) : []);

  // Ask the server. Debounced, and only once there is something to ask about:
  // an empty query is refused by the API, and firing it on every `[[` would put
  // a rejected request behind a keystroke.
  const query = trigger?.query ?? "";
  useEffect(() => {
    if (!trigger || !projectId || query.trim() === "") {
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
        // emptying the menu: degraded is better than blank, and blank would read
        // as "there is no such document".
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
  }, [trigger, projectId, query, scope]);

  const onValueChange = useCallback(
    (next: string, caret: number) => {
      const found = findDocLinkTrigger(next, caret);
      setTrigger(found);
      if (!found) {
        setActiveIndex(0);
        setHits(null);
      }
    },
    [],
  );

  const pick = useCallback(
    (doc: DocumentSearchHit) => {
      const textarea = textareaRef.current;
      const caret = textarea?.selectionStart ?? valueRef.current.length;
      const active = findDocLinkTrigger(valueRef.current, caret);
      // Re-derived from the live text rather than trusting the trigger the menu
      // opened with: between opening and choosing, the caret may have moved, and
      // a stale offset rewrites a part of the sentence the writer is not looking
      // at.
      if (!active) {
        close();
        return;
      }

      // A Team Relay document has no route in this app — that is exactly why the
      // relay:// pseudo-scheme exists — so it is inserted as the URL the existing
      // renderer already recognises, not as a link to a page we do not serve.
      let link: string;
      if (doc.relayUrl) {
        link = `[${linkLabel(doc.title)}](${doc.relayUrl})`;
      } else {
        const wsSlug = currentWorkspace?.slug;
        const project =
          projects.find((p) => p.id === projectId) ??
          (currentProject?.id === projectId ? currentProject : undefined);
        if (!wsSlug || !project) {
          close();
          return;
        }
        link = documentMarkdownLink(doc.title, wsSlug, project.slug, doc.id);
      }
      const out = applyDocLinkInsertion(valueRef.current, active, caret, link);
      onChangeRef.current(out.value);
      close();

      requestAnimationFrame(() => {
        const el = textareaRef.current;
        if (!el) return;
        el.setSelectionRange(out.caret, out.caret);
        el.focus();
      });
    },
    [close, currentProject, currentWorkspace, projectId, projects, textareaRef],
  );

  const onKeyDown = useCallback(
    (e: KeyboardEvent<HTMLTextAreaElement>): boolean => {
      if (!trigger) return false;
      // Escape closes even with nothing to show — otherwise a writer who typed
      // `[[` for its own sake is stuck with a menu that only a click dismisses.
      if (e.key === "Escape") {
        e.preventDefault();
        close();
        return true;
      }
      if (suggestions.length === 0) return false;

      if (e.key === "ArrowDown") {
        e.preventDefault();
        setActiveIndex((i) => Math.min(i + 1, suggestions.length - 1));
        return true;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setActiveIndex((i) => Math.max(i - 1, 0));
        return true;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        const doc = suggestions[activeIndex];
        if (!doc) return false;
        e.preventDefault();
        pick(doc);
        return true;
      }
      return false;
    },
    [activeIndex, close, pick, suggestions, trigger],
  );

  return {
    trigger,
    suggestions,
    scope,
    setScope,
    loading,
    activeIndex,
    setActiveIndex,
    onValueChange,
    onKeyDown,
    pick,
    close,
  };
}
