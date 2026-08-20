import { cn } from "@/lib/cn";
import { formatDate, formatDateTime } from "@/lib/utils";
import type { ProjectDocument } from "@/types";

export interface DocMetaProps {
  doc: ProjectDocument;
  className?: string;
}

function actorLabel(name: string | null | undefined): string {
  const named = (name ?? "").trim();
  if (named) return named;
  // Never the raw uuid: it identifies nobody to the reader and is noise in a
  // line whose whole job is to be scannable. The API resolves both actor names
  // on every document read, so a blank one means the actor is gone — not that
  // the name has yet to arrive.
  return "Unknown";
}

/** Never throws on a timestamp the API surprises us with. */
function safeFormat(value: string | null | undefined, long: boolean): string {
  if (!value) return "";
  try {
    return long ? formatDateTime(value) : formatDate(value);
  } catch {
    return "";
  }
}

function Dot() {
  return (
    <span aria-hidden="true" className="px-1.5 opacity-50">
      ·
    </span>
  );
}

/**
 * The line under the title: who made this page, who touched it last, and when.
 *
 * `updated_by` is null for every row written before the column existed. There
 * is no honest editor to name for those, so the line degrades to creator +
 * date rather than guessing that the creator was also the last editor.
 */
export function DocMeta({ doc, className }: DocMetaProps) {
  const creator = actorLabel(doc.created_by_name);
  const hasEditor = Boolean(doc.updated_by || doc.updated_by_name);
  const editor = hasEditor ? actorLabel(doc.updated_by_name) : null;

  const when = safeFormat(doc.updated_at, false);
  const exact = safeFormat(doc.updated_at, true);

  return (
    <p
      className={cn(
        "flex flex-wrap items-center text-xs text-muted-foreground",
        className,
      )}
    >
      <span>
        Created by <span className="text-foreground/80">{creator}</span>
      </span>

      {editor && (
        <>
          <Dot />
          <span>
            Last updated by <span className="text-foreground/80">{editor}</span>
          </span>
        </>
      )}

      {when && (
        <>
          <Dot />
          {/* "Updated <date>" only when there is no editor named, so the two
              halves do not read as "last updated by X ... updated <date>". */}
          <time dateTime={doc.updated_at} title={exact || undefined}>
            {editor ? when : `Updated ${when}`}
          </time>
        </>
      )}
    </p>
  );
}
