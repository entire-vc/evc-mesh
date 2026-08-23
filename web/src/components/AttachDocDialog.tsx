import { useEffect, useRef, useState } from "react";
import { BookOpen, FileText, Loader2, Search, X } from "lucide-react";
import { apiErrorMessage } from "@/lib/api-error";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  type DocumentSearchHit,
  type SearchScope,
  searchDocuments,
  searchRelayDocuments,
  splitSnippet,
} from "@/lib/docs/document-search";

/**
 * One dialog, two scopes.
 *
 * Replaces RelayDocPicker (which only ever searched Team Relay) with a
 * version parametrised by SearchScope — the same type document-search.ts
 * already uses for the inline `[[` menu (D9). A search-and-pick dialog for
 * our own Docs was missing entirely; this is that dialog, reusing the exact
 * server calls the `[[` menu already relies on rather than inventing a third
 * way to search documents.
 */

const SCOPE_COPY: Record<
  SearchScope,
  { title: string; icon: typeof BookOpen; placeholder: string }
> = {
  docs: {
    title: "Link a document",
    icon: FileText,
    placeholder: "Search documents...",
  },
  relay: {
    title: "Attach Obsidian doc",
    icon: BookOpen,
    placeholder: "Искать Obsidian-доки...",
  },
};

/** How long after the last keystroke the server is asked. */
const SEARCH_DEBOUNCE_MS = 300;

interface AttachDocDialogProps {
  projId: string;
  scope: SearchScope;
  open: boolean;
  onClose: () => void;
  onSelect: (hit: DocumentSearchHit) => void;
}

export function AttachDocDialog({
  projId,
  scope,
  open,
  onClose,
  onSelect,
}: AttachDocDialogProps) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<DocumentSearchHit[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    if (open) {
      setQuery("");
      setResults([]);
      setError(null);
      // Focus after dialog renders into portal
      setTimeout(() => inputRef.current?.focus(), 50);
    }
  }, [open]);

  useEffect(() => {
    if (!open || !projId) return;
    // Docs search rejects an empty q outright (document_service.go: "an empty
    // query through a search endpoint is a caller bug ... answering it with
    // the project's whole document set is how a search box becomes a
    // full-table scan"), same reason the `[[` menu's useDocSuggestions never
    // fires on an empty query for this scope. Team Relay's search endpoint
    // has no such guard and genuinely supports browsing on an empty query —
    // that asymmetry is the server's, not invented here, so only "docs" waits.
    if (scope === "docs" && query.trim() === "") {
      setResults([]);
      setIsLoading(false);
      setError(null);
      return;
    }
    let cancelled = false;
    setIsLoading(true);
    setError(null);
    const timer = setTimeout(() => {
      const search = scope === "relay" ? searchRelayDocuments : searchDocuments;
      search(projId, query)
        .then((hits) => {
          if (!cancelled && mountedRef.current) setResults(hits);
        })
        .catch((err) => {
          if (!cancelled && mountedRef.current) {
            setError(apiErrorMessage(err, "Search failed"));
            setResults([]);
          }
        })
        .finally(() => {
          if (!cancelled && mountedRef.current) setIsLoading(false);
        });
    }, SEARCH_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [open, projId, query, scope]);

  function handleSelect(hit: DocumentSearchHit) {
    onSelect(hit);
    onClose();
  }

  const copy = SCOPE_COPY[scope];
  const Icon = copy.icon;

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent onClose={onClose} className="max-w-md p-4">
        <DialogHeader className="mb-3">
          <DialogTitle className="flex items-center gap-2 text-sm font-medium">
            <Icon className="h-4 w-4" />
            {copy.title}
          </DialogTitle>
        </DialogHeader>

        {/* Search input */}
        <div className="relative mb-3">
          <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground pointer-events-none" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={copy.placeholder}
            className="w-full rounded-md border border-border bg-background py-1.5 pl-8 pr-8 text-sm placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
          />
          {query && (
            <button
              type="button"
              onClick={() => setQuery("")}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>

        {/* Results */}
        <div className="max-h-64 overflow-y-auto">
          {isLoading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : error ? (
            <p className="px-2 py-3 text-xs text-destructive">{error}</p>
          ) : results.length === 0 ? (
            <p className="px-2 py-3 text-xs text-muted-foreground">
              {query
                ? "Ничего не найдено"
                : scope === "docs"
                  ? "Start typing to search"
                  : "Нет доступных документов"}
            </p>
          ) : (
            <ul className="space-y-0.5">
              {results.map((hit) => (
                <li key={hit.id}>
                  <button
                    type="button"
                    onClick={() => handleSelect(hit)}
                    className="w-full rounded-md px-2 py-2 text-left transition-colors hover:bg-muted"
                  >
                    <div className="truncate text-sm font-medium leading-snug">
                      {hit.title}
                    </div>
                    <div className="truncate text-xs text-muted-foreground">
                      {hit.snippetIsMatch
                        ? splitSnippet(hit.snippet).map((part, i) =>
                            part.match ? (
                              <mark key={i} className="bg-primary/25 text-foreground">
                                {part.text}
                              </mark>
                            ) : (
                              <span key={i}>{part.text}</span>
                            ),
                          )
                        : hit.snippet}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
