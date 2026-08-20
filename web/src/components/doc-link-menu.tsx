import { FileText, Loader2 } from "lucide-react";
import { cn } from "@/lib/cn";
import {
  type DocumentSearchHit,
  type SearchScope,
  splitSnippet,
} from "@/lib/docs/document-search";

/**
 * The list that appears under `[[`.
 *
 * Deliberately the same shape as the mention menu next to it: a writer who has
 * learned one has learned the other, and two inline menus in the same textarea
 * that behave differently is worse than either.
 *
 * The scope row is shown only when there is a choice to make. A single-option
 * switcher is a control that cannot do anything, and it teaches the reader that
 * the row is decoration.
 */

function Snippet({ hit }: { hit: DocumentSearchHit }) {
  if (!hit.snippet) return null;

  // Two different things wear the same slot, and the reader has to be able to
  // tell them apart: the fragment that MATCHED, or — when the match lies past
  // the window the server quoted from — the document's opening, which is merely
  // context. Rendering both identically presents the first sentence of every
  // long document as the reason it came up.
  //
  // The distinction is carried by an attribute and by weight rather than by a
  // word, so it needs no new user-facing copy and reads the same in every
  // language.
  if (!hit.snippetIsMatch) {
    return (
      <span
        data-snippet="preview"
        className="block truncate text-[10px] italic text-muted-foreground/70"
      >
        {hit.snippet}
      </span>
    );
  }

  return (
    <span data-snippet="match" className="block truncate text-[10px] text-muted-foreground">
      {splitSnippet(hit.snippet).map((part, i) =>
        part.match ? (
          <mark key={i} className="bg-primary/25 text-foreground">
            {part.text}
          </mark>
        ) : (
          <span key={i}>{part.text}</span>
        ),
      )}
    </span>
  );
}

export function DocLinkMenu({
  suggestions,
  activeIndex,
  onPick,
  onHover,
  scope,
  scopes,
  onScope,
  loading = false,
}: {
  suggestions: readonly DocumentSearchHit[];
  activeIndex: number;
  onPick: (doc: DocumentSearchHit) => void;
  onHover: (index: number) => void;
  scope: SearchScope;
  /** Which scopes this project actually has. Team Relay appears only when connected. */
  scopes: readonly { id: SearchScope; label: string }[];
  onScope: (scope: SearchScope) => void;
  loading?: boolean;
}) {
  const header =
    scopes.length > 1 ? (
      <div
        role="radiogroup"
        aria-label="Search in"
        className="mb-1 flex gap-1 border-b border-border px-1 pb-1"
      >
        {scopes.map((s) => (
          <button
            key={s.id}
            type="button"
            role="radio"
            aria-checked={s.id === scope}
            // mousedown, not click: the textarea loses focus on mousedown, and
            // with it the caret the insertion is measured from.
            onMouseDown={(e) => {
              e.preventDefault();
              onScope(s.id);
            }}
            className={cn(
              "rounded px-2 py-0.5 text-[11px]",
              s.id === scope
                ? "bg-accent text-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {s.label}
          </button>
        ))}
      </div>
    ) : null;

  if (suggestions.length === 0) {
    return (
      <div className="absolute z-20 mt-1 w-80 rounded-md border border-border bg-popover p-1 shadow-md">
        {header}
        <p className="px-2 py-1 text-xs text-muted-foreground">
          {loading ? (
            <span className="flex items-center gap-1.5">
              <Loader2 className="h-3 w-3 animate-spin" />
              Searching...
            </span>
          ) : (
            "No documents match"
          )}
        </p>
      </div>
    );
  }

  return (
    <div className="absolute z-20 max-h-64 w-80 overflow-y-auto rounded-md border border-border bg-popover p-1 shadow-md">
      {header}
      <ul role="listbox" aria-label="Link a document">
        {suggestions.map((doc, index) => (
          <li key={doc.id}>
            <button
              type="button"
              role="option"
              aria-selected={index === activeIndex}
              onMouseDown={(e) => {
                e.preventDefault();
                onPick(doc);
              }}
              onMouseEnter={() => onHover(index)}
              className={cn(
                "flex w-full items-start gap-2 rounded px-2 py-1.5 text-left text-xs",
                index === activeIndex ? "bg-accent text-foreground" : "text-muted-foreground",
              )}
            >
              <FileText className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span className="min-w-0 flex-1">
                <span className="block truncate">{doc.title}</span>
                <Snippet hit={doc} />
              </span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
