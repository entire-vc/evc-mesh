import { useEffect, useRef, useState } from "react";
import { BookOpen, Loader2, Search, X } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useRelayDocSearch } from "@/hooks/useRelayDocSearch";

interface RelayDocPickerProps {
  projId: string;
  open: boolean;
  onClose: () => void;
  onSelect: (relayUrl: string) => void;
}

export function RelayDocPicker({
  projId,
  open,
  onClose,
  onSelect,
}: RelayDocPickerProps) {
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const { results, isLoading, error } = useRelayDocSearch(query, projId);

  useEffect(() => {
    if (open) {
      setQuery("");
      // Focus after dialog renders into portal
      setTimeout(() => inputRef.current?.focus(), 50);
    }
  }, [open]);

  function handleSelect(relayUrl: string) {
    onSelect(relayUrl);
    onClose();
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent onClose={onClose} className="max-w-md p-4">
        <DialogHeader className="mb-3">
          <DialogTitle className="flex items-center gap-2 text-sm font-medium">
            <BookOpen className="h-4 w-4" />
            Attach Obsidian doc
          </DialogTitle>
        </DialogHeader>

        {/* Search input */}
        <div className="relative mb-3">
          <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground pointer-events-none" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Искать Obsidian-доки..."
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
              {query ? "Ничего не найдено" : "Нет доступных документов"}
            </p>
          ) : (
            <ul className="space-y-0.5">
              {results.map((doc) => (
                <li key={doc.relay_url}>
                  <button
                    type="button"
                    onClick={() => handleSelect(doc.relay_url)}
                    className="w-full rounded-md px-2 py-2 text-left transition-colors hover:bg-muted"
                  >
                    <div className="truncate text-sm font-medium leading-snug">
                      {doc.title}
                    </div>
                    <div className="truncate text-xs text-muted-foreground">
                      {doc.path}
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
