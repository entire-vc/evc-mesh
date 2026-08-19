import { ChevronRight } from "lucide-react";
import { buildDocPath } from "@/components/doc-tree";
import { cn } from "@/lib/cn";
import type { ProjectDocument } from "@/types";

export interface DocBreadcrumbsProps {
  /** The project's documents — the same flat list the tree is built from. */
  documents: ProjectDocument[];
  /**
   * The open document. Supplies the last crumb, and stands in for the whole
   * trail when the list has not loaded yet (a deep link straight to /docs/:id
   * renders the document before the tree exists).
   */
  current: ProjectDocument;
  /** Label of the leading crumb, the one that goes back to the docs root. */
  rootLabel?: string;
  onNavigateRoot: () => void;
  onNavigate: (doc: ProjectDocument) => void;
  className?: string;
}

/**
 * Where this page sits: root / ancestor / ancestor / this page.
 *
 * Everything but the last crumb navigates. The last one is the page you are on,
 * so it is text with aria-current rather than a link to here.
 */
export function DocBreadcrumbs({
  documents,
  current,
  rootLabel = "Documents",
  onNavigateRoot,
  onNavigate,
  className,
}: DocBreadcrumbsProps) {
  const path = buildDocPath(documents, current.id);
  // The leaf's title comes from `current`, not from the list: after a rename
  // the open document is updated first, and reading the stale copy would show
  // the old title in the crumb next to the new one in the heading.
  const ancestors = path.length > 0 ? path.slice(0, -1) : [];

  return (
    <nav aria-label="Breadcrumb" className={cn("min-w-0", className)}>
      <ol className="flex flex-wrap items-center gap-x-1 gap-y-0.5 text-xs text-muted-foreground">
        <li className="flex items-center gap-x-1">
          <button
            type="button"
            onClick={onNavigateRoot}
            className="rounded-sm hover:text-foreground hover:underline"
          >
            {rootLabel}
          </button>
          <ChevronRight aria-hidden="true" className="h-3 w-3 shrink-0 opacity-60" />
        </li>

        {ancestors.map((ancestor) => (
          <li key={ancestor.id} className="flex min-w-0 items-center gap-x-1">
            <button
              type="button"
              onClick={() => onNavigate(ancestor)}
              className="max-w-[14rem] truncate rounded-sm hover:text-foreground hover:underline"
            >
              {ancestor.title}
            </button>
            <ChevronRight aria-hidden="true" className="h-3 w-3 shrink-0 opacity-60" />
          </li>
        ))}

        <li className="min-w-0">
          <span
            aria-current="page"
            className="block max-w-[22rem] truncate font-medium text-foreground"
          >
            {current.title}
          </span>
        </li>
      </ol>
    </nav>
  );
}
