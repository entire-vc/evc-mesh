import { Bot, User } from "lucide-react";
import { cn } from "@/lib/cn";
import type { Mentionable } from "@/types";
import type { UseMentionPicker } from "@/hooks/use-mention-picker";

export type { Mentionable };

/**
 * The list that appears under `@`.
 *
 * Lifted out of comment-list.tsx, because the editor that owns the trigger is
 * now shared by comments and task descriptions — and the point of that unit is
 * that a writer gets the same affordances in both. Leaving the markup inline in
 * the comment box is how it came to exist in exactly one of the three places
 * you could write markdown in this app.
 *
 * Deliberately the same shape as DocLinkMenu beside it: a writer who has
 * learned one has learned the other.
 *
 * Presentational on purpose: it takes a list and an index, not a hook. Two
 * different mechanisms drive it — ProseMirror selection inside the rich text
 * editor, and caret offsets inside a plain textarea (MentionPickerMenu below) —
 * and neither should have to know what the other needs. What must NOT fork
 * again is the markup: the list, the icons and the highlight are what make the
 * menu recognisable.
 */
export function MentionMenu({
  suggestions,
  activeIndex,
  onPick,
  onHover,
  className,
}: {
  suggestions: readonly Mentionable[];
  activeIndex: number;
  onPick: (m: Mentionable) => void;
  onHover?: (index: number) => void;
  className?: string;
}) {
  if (suggestions.length === 0) return null;

  return (
    <div
      role="listbox"
      aria-label="Mention a person or agent"
      className={cn(
        "max-h-48 overflow-y-auto rounded-md border border-border bg-background shadow-md",
        className ?? "w-72",
      )}
    >
      {suggestions.map((m, i) => (
        <button
          key={m.id}
          type="button"
          role="option"
          aria-selected={i === activeIndex}
          className={cn(
            "flex w-full items-center gap-2 px-3 py-2 text-sm hover:bg-muted",
            i === activeIndex && "bg-muted",
          )}
          onMouseEnter={() => onHover?.(i)}
          // mousedown, not click: mousedown fires before the box loses focus,
          // so preventing its default keeps the caret (or the ProseMirror
          // selection) where the insertion is about to happen.
          onMouseDown={(e) => {
            e.preventDefault();
            onPick(m);
          }}
        >
          {m.kind === "agent" ? (
            <Bot className="h-3.5 w-3.5 shrink-0 text-violet-500" />
          ) : (
            <User className="h-3.5 w-3.5 shrink-0 text-sky-500" />
          )}
          <span className="font-mono text-xs text-muted-foreground">@{m.slug}</span>
          <span className="truncate">{m.display_name}</span>
          <span className="ml-auto text-[10px] capitalize text-muted-foreground">{m.kind}</span>
        </button>
      ))}
    </div>
  );
}

/**
 * The same menu, driven by useMentionPicker — the textarea-based surfaces.
 *
 * Document comments are still written in a plain textarea, so they keep the
 * caret-offset picker; task comments and descriptions moved to the rich text
 * editor and drive the list above directly. One component, two drivers, rather
 * than two components that have to be kept looking alike.
 *
 * The parent must be `position: relative` — this positions itself against it.
 */
export function MentionPickerMenu({ picker }: { picker: UseMentionPicker }) {
  if (picker.trigger === null || picker.suggestions.length === 0) return null;

  return (
    <div className="absolute bottom-full left-0 right-0 z-50 mb-1">
      <MentionMenu
        suggestions={picker.suggestions}
        activeIndex={picker.activeIndex}
        onPick={picker.pick}
        className="w-full"
      />
    </div>
  );
}
