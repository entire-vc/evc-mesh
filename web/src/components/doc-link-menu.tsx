import { FileText } from "lucide-react";
import { cn } from "@/lib/cn";
import type { LinkableDocument } from "@/lib/docs/doc-link";

/**
 * The list that appears under `[[`.
 *
 * Deliberately the same shape as the mention menu next to it: a writer who has
 * learned one has learned the other, and two inline menus in the same textarea
 * that behave differently is worse than either.
 */
export function DocLinkMenu({
  suggestions,
  activeIndex,
  onPick,
  onHover,
}: {
  suggestions: readonly LinkableDocument[];
  activeIndex: number;
  onPick: (doc: LinkableDocument) => void;
  onHover: (index: number) => void;
}) {
  if (suggestions.length === 0) {
    return (
      <div className="absolute z-20 mt-1 w-72 rounded-md border border-border bg-popover p-2 text-xs text-muted-foreground shadow-md">
        No documents match
      </div>
    );
  }

  return (
    <ul
      role="listbox"
      aria-label="Link a document"
      className="absolute z-20 mt-1 max-h-56 w-72 overflow-y-auto rounded-md border border-border bg-popover p-1 shadow-md"
    >
      {suggestions.map((doc, index) => (
        <li key={doc.id}>
          <button
            type="button"
            role="option"
            aria-selected={index === activeIndex}
            // mousedown, not click: the textarea loses focus on mousedown, and
            // with it the caret the insertion is measured from.
            onMouseDown={(e) => {
              e.preventDefault();
              onPick(doc);
            }}
            onMouseEnter={() => onHover(index)}
            className={cn(
              "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs",
              index === activeIndex ? "bg-accent text-foreground" : "text-muted-foreground",
            )}
          >
            <FileText className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">{doc.title}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}
