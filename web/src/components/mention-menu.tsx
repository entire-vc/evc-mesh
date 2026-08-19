import { Bot, User } from "lucide-react";
import { cn } from "@/lib/cn";
import type { UseMentionPicker } from "@/hooks/use-mention-picker";

/**
 * The `@` dropdown, rendered above the box it belongs to.
 *
 * A component rather than markup repeated per surface, for the same reason
 * useMentionPicker is a hook: the list, the icons and the highlight are what
 * make the menu recognisable, and a second copy is a second thing to keep in
 * step.
 *
 * The parent must be `position: relative` — this positions itself against it.
 *
 * onMouseDown, not onClick: mousedown fires before the textarea loses focus, so
 * preventing its default keeps the caret where the insertion is about to happen.
 */
export function MentionMenu({ picker }: { picker: UseMentionPicker }) {
  if (picker.trigger === null || picker.suggestions.length === 0) return null;

  return (
    <div
      role="listbox"
      aria-label="Mention a person or agent"
      className="absolute bottom-full left-0 right-0 z-50 mb-1 max-h-48 overflow-y-auto rounded-md border border-border bg-background shadow-md"
    >
      {picker.suggestions.map((m, i) => (
        <button
          key={m.id}
          type="button"
          role="option"
          aria-selected={i === picker.activeIndex}
          className={cn(
            "flex w-full items-center gap-2 px-3 py-2 text-sm hover:bg-muted",
            i === picker.activeIndex && "bg-muted",
          )}
          onMouseDown={(e) => {
            e.preventDefault();
            picker.pick(m);
          }}
        >
          {m.kind === "agent" ? (
            <Bot className="h-3.5 w-3.5 shrink-0 text-violet-500" />
          ) : (
            <User className="h-3.5 w-3.5 shrink-0 text-sky-500" />
          )}
          <span className="font-mono text-xs text-muted-foreground">@{m.slug}</span>
          <span className="truncate">{m.display_name}</span>
          <span className="ml-auto text-[10px] capitalize text-muted-foreground">
            {m.kind}
          </span>
        </button>
      ))}
    </div>
  );
}
