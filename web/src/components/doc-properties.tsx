import { useState } from "react";
import { ChevronRight } from "lucide-react";
import { cn } from "@/lib/cn";
import { formatDate, formatDateTime } from "@/lib/utils";
import type { FrontmatterEntry } from "@/lib/docs/frontmatter";

export interface DocPropertiesProps {
  entries: FrontmatterEntry[];
  /** The lines between the fences — shown verbatim when nothing parsed. */
  inner: string;
  className?: string;
}

const DATE_ONLY = /^\d{4}-\d{2}-\d{2}$/;
const DATE_TIME = /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}/;

/** A date shown as a date; anything else, or a date that fails to parse, as written. */
function DateOrText({ value }: { value: string }) {
  const isDate = DATE_ONLY.test(value);
  const isDateTime = !isDate && DATE_TIME.test(value);
  if (isDate || isDateTime) {
    try {
      const shown = isDate ? formatDate(value) : formatDateTime(value);
      return (
        <time dateTime={value} title={value}>
          {shown}
        </time>
      );
    } catch {
      // Fall through to the text as written.
    }
  }
  return <span className="break-words">{value}</span>;
}

function Chips({ items }: { items: string[] }) {
  return (
    <span className="flex flex-wrap gap-1">
      {items.map((item, i) => (
        <span
          key={`${item}-${i}`}
          data-testid="doc-property-chip"
          className="inline-flex items-center rounded-md bg-secondary px-1.5 py-0.5 text-[11px] font-medium text-secondary-foreground"
        >
          {item}
        </span>
      ))}
    </span>
  );
}

function tagsOf(entries: FrontmatterEntry[]): string[] {
  const tags = entries.find((e) => e.key.toLowerCase() === "tags")?.value;
  if (Array.isArray(tags)) return tags;
  return tags ? [tags] : [];
}

/**
 * The document's frontmatter as a collapsed "Properties" block, the way
 * Obsidian shows it: one line with the tags when closed, a key → value table
 * when opened.
 *
 * Display only. The markdown is not touched; see lib/docs/frontmatter.ts.
 *
 * `data-doc-text-skip` keeps the block out of the text the inline-comment layer
 * anchors against (lib/doc-comments/dom-text.ts): it is not prose, and opening
 * or closing it must not move every quote below it.
 */
export function DocProperties({ entries, inner, className }: DocPropertiesProps) {
  const [open, setOpen] = useState(false);
  const tags = tagsOf(entries);
  const count = entries.length || inner.split("\n").filter((l) => l.trim()).length;

  return (
    <div
      data-testid="doc-properties"
      data-doc-text-skip=""
      contentEditable={false}
      className={cn("mb-3 rounded-md border border-border text-xs", className)}
    >
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-left text-muted-foreground transition-colors hover:bg-secondary hover:text-secondary-foreground"
      >
        <ChevronRight
          aria-hidden="true"
          className={cn("h-3.5 w-3.5 shrink-0 transition-transform", open && "rotate-90")}
        />
        <span className="shrink-0 font-medium">Properties</span>
        <span className="shrink-0 opacity-70">{count}</span>
        {!open && tags.length > 0 && (
          <span className="ml-1 min-w-0 overflow-hidden">
            <Chips items={tags} />
          </span>
        )}
      </button>

      {open &&
        (entries.length ? (
          <dl
            data-testid="doc-properties-table"
            className="grid grid-cols-[minmax(5rem,auto)_1fr] gap-x-4 gap-y-1.5 border-t border-border px-3 py-2"
          >
            {entries.map((entry, i) => (
              <div key={`${entry.key}-${i}`} className="contents">
                <dt className="truncate text-muted-foreground" title={entry.key}>
                  {entry.key}
                </dt>
                <dd className="min-w-0 text-foreground">
                  {Array.isArray(entry.value) ? (
                    entry.key.toLowerCase() === "tags" ? (
                      <Chips items={entry.value} />
                    ) : (
                      <span className="break-words">{entry.value.join(", ")}</span>
                    )
                  ) : (
                    <DateOrText value={entry.value} />
                  )}
                </dd>
              </div>
            ))}
          </dl>
        ) : (
          <pre className="overflow-x-auto border-t border-border px-3 py-2 font-mono text-[11px] text-foreground">
            {inner}
          </pre>
        ))}
    </div>
  );
}
