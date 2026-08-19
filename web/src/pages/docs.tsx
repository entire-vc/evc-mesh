import { FilePlus2, NotebookText } from "lucide-react";

/**
 * Docs — two-column shell (tree + document area).
 *
 * Both columns are empty states on purpose: this unit ships navigation and the
 * frame, ahead of the document API. The tree is filled in by the next unit.
 */
export function DocsPage() {
  return (
    // h-full + overflow-hidden because the app shell's <main> is
    // `flex-1 overflow-y-auto`: without it the columns grow past the viewport
    // and the page scrolls as a whole instead of each column scrolling.
    <div className="flex h-full min-h-0 gap-3 overflow-hidden">
      {/* Document tree */}
      <div className="hidden w-64 shrink-0 flex-col rounded-lg border border-border md:flex">
        <div className="border-b border-border px-3 py-2">
          <h2 className="text-sm font-semibold">Documents</h2>
        </div>
        <div className="flex flex-1 flex-col items-center justify-center gap-1 overflow-y-auto p-4 text-center">
          <NotebookText className="h-6 w-6 text-muted-foreground" />
          <p className="text-xs text-muted-foreground">No documents yet</p>
        </div>
      </div>

      {/* Document area */}
      <div className="flex flex-1 flex-col overflow-hidden rounded-lg border border-border">
        <div className="flex flex-1 flex-col items-center justify-center gap-2 overflow-y-auto p-6 text-center">
          <FilePlus2 className="h-10 w-10 text-muted-foreground" />
          <h3 className="text-sm font-semibold">No document selected</h3>
          <p className="max-w-xs text-sm text-muted-foreground">
            Project documents live here. Pick one from the tree once there is
            something to pick.
          </p>
        </div>
      </div>
    </div>
  );
}
