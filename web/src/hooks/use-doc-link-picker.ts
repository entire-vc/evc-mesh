import { type KeyboardEvent, type RefObject, useCallback, useEffect, useRef, useState } from "react";
import {
  type DocLinkTrigger,
  type LinkableDocument,
  applyDocLinkInsertion,
  documentMarkdownLink,
  findDocLinkTrigger,
  matchDocuments,
} from "@/lib/docs/doc-link";
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
  suggestions: LinkableDocument[];
  activeIndex: number;
  setActiveIndex: (index: number) => void;
  /** Call from the textarea's onChange, AFTER the value has been set. */
  onValueChange: (value: string, caret: number) => void;
  /** Call from the textarea's onKeyDown, first. Returns true when it handled the key. */
  onKeyDown: (e: KeyboardEvent<HTMLTextAreaElement>) => boolean;
  pick: (doc: LinkableDocument) => void;
  close: () => void;
}

export function useDocLinkPicker(
  projectId: string | undefined,
  value: string,
  onChange: (next: string) => void,
  textareaRef: RefObject<HTMLTextAreaElement | null>,
): UseDocLinkPicker {
  const [trigger, setTrigger] = useState<DocLinkTrigger | null>(null);
  const [documents, setDocuments] = useState<LinkableDocument[]>([]);
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
        if (!cancelled) setDocuments(items);
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

  const suggestions = trigger ? matchDocuments(documents, trigger.query) : [];

  const onValueChange = useCallback(
    (next: string, caret: number) => {
      const found = findDocLinkTrigger(next, caret);
      setTrigger(found);
      if (!found) setActiveIndex(0);
    },
    [],
  );

  const pick = useCallback(
    (doc: LinkableDocument) => {
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

      const wsSlug = currentWorkspace?.slug;
      const project =
        projects.find((p) => p.id === projectId) ??
        (currentProject?.id === projectId ? currentProject : undefined);
      if (!wsSlug || !project) {
        close();
        return;
      }

      const link = documentMarkdownLink(doc.title, wsSlug, project.slug, doc.id);
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
    activeIndex,
    setActiveIndex,
    onValueChange,
    onKeyDown,
    pick,
    close,
  };
}
