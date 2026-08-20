import {
  type KeyboardEvent,
  type RefObject,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  type MentionTrigger,
  applyMentionInsertion,
  findMentionTrigger,
} from "@/lib/mentions/mention-trigger";
import { getMentionables } from "@/lib/api";
import type { Mentionable } from "@/types";

/**
 * Typing `@` in a comment box and getting a person or an agent out of it.
 *
 * One hook for every surface that takes a comment, which is the gap
 * use-doc-link-picker.ts names in its own header: the `@` menu was implemented
 * once, inline in the task comment list, and was therefore absent from document
 * comments. Adding it to document comments by copying those sixty lines would
 * have reopened the same gap for the next editor somebody adds.
 *
 * The caller owns its textarea and its markup; this owns when the menu is open,
 * what is in it, and what the text becomes.
 */
export interface UseMentionPicker {
  /** Non-null while the menu should be shown. */
  trigger: MentionTrigger | null;
  suggestions: Mentionable[];
  activeIndex: number;
  setActiveIndex: (index: number) => void;
  /** Call from the textarea's onChange, AFTER the value has been set. */
  onValueChange: (value: string, caret: number) => void;
  /** Call from the textarea's onKeyDown, first. Returns true when it handled the key. */
  onKeyDown: (e: KeyboardEvent<HTMLTextAreaElement>) => boolean;
  pick: (m: Mentionable) => void;
  close: () => void;
}

/** How long after the last keystroke the server is asked. */
export const MENTION_DEBOUNCE_MS = 150;

export function useMentionPicker(
  workspaceId: string | undefined,
  value: string,
  onChange: (next: string) => void,
  textareaRef: RefObject<HTMLTextAreaElement | null>,
): UseMentionPicker {
  const [trigger, setTrigger] = useState<MentionTrigger | null>(null);
  const [suggestions, setSuggestions] = useState<Mentionable[]>([]);
  const [activeIndex, setActiveIndex] = useState(0);

  // Read by the insertion, which must not close over the value of the render
  // the menu happened to open on.
  const valueRef = useRef(value);
  valueRef.current = value;
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  const close = useCallback(() => {
    setTrigger(null);
    setSuggestions([]);
    setActiveIndex(0);
  }, []);

  // Ask the server, debounced.
  //
  // The cleanup does both halves — clearTimeout for a keystroke that arrives
  // before the timer fires, and a `cancelled` flag for a response that arrives
  // after the menu moved on. The inline version this replaces had neither, so a
  // slow answer to `@da` could overwrite the suggestions for `@dan`, and the
  // pending timer outlived the component.
  const query = trigger?.query ?? "";
  const open = trigger !== null;
  useEffect(() => {
    if (!open || !workspaceId) {
      setSuggestions([]);
      return;
    }
    let cancelled = false;
    const timer = setTimeout(() => {
      getMentionables(workspaceId, query)
        .then((items) => {
          if (!cancelled) setSuggestions(items ?? []);
        })
        // A failed lookup empties the menu rather than freezing the previous
        // query's results, which would offer a name the current text does not
        // match.
        .catch(() => {
          if (!cancelled) setSuggestions([]);
        });
    }, MENTION_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [open, workspaceId, query]);

  const onValueChange = useCallback((next: string, caret: number) => {
    const found = findMentionTrigger(next, caret);
    setTrigger(found);
    if (!found) {
      setSuggestions([]);
      setActiveIndex(0);
    } else {
      setActiveIndex(0);
    }
  }, []);

  const pick = useCallback(
    (m: Mentionable) => {
      const textarea = textareaRef.current;
      const caret = textarea?.selectionStart ?? valueRef.current.length;
      // Re-derived from the live text rather than trusting the trigger the menu
      // opened with: between opening and choosing, the caret may have moved, and
      // a stale offset rewrites a part of the sentence the writer is not looking
      // at.
      const active = findMentionTrigger(valueRef.current, caret);
      if (!active) {
        close();
        return;
      }

      const out = applyMentionInsertion(valueRef.current, active, caret, m.slug);
      onChangeRef.current(out.value);
      close();

      requestAnimationFrame(() => {
        const el = textareaRef.current;
        if (!el) return;
        el.setSelectionRange(out.caret, out.caret);
        el.focus();
      });
    },
    [close, textareaRef],
  );

  const onKeyDown = useCallback(
    (e: KeyboardEvent<HTMLTextAreaElement>): boolean => {
      if (!trigger) return false;
      // Escape closes even with nothing to show — otherwise somebody who typed
      // an `@` for its own sake is stuck with a menu only a click dismisses.
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
        const m = suggestions[activeIndex];
        if (!m) return false;
        e.preventDefault();
        pick(m);
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
