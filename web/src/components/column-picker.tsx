import { useCallback, useEffect, useRef, useState } from "react";
import { Columns3 } from "lucide-react";
import { cn } from "@/lib/cn";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface ColumnDef {
  key: string;
  label: string;
  visible: boolean;
  required?: boolean;
}

export interface ColumnPickerProps {
  columns: ColumnDef[];
  onChange: (columns: { key: string; visible: boolean }[]) => void;
  className?: string;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ColumnPicker({ columns, onChange, className }: ColumnPickerProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Close on outside click
  useEffect(() => {
    if (!open) return;

    const handler = (e: MouseEvent) => {
      if (
        containerRef.current &&
        !containerRef.current.contains(e.target as Node)
      ) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  // Close on Escape
  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [open]);

  const handleToggle = useCallback(
    (key: string, checked: boolean) => {
      const updated = columns.map((col) =>
        col.key === key ? { key: col.key, visible: checked } : { key: col.key, visible: col.visible },
      );
      onChange(updated);
    },
    [columns, onChange],
  );

  const handleReset = useCallback(() => {
    // Reset shows: name, status, priority, assignee, due_date
    const defaults = new Set(["name", "status", "priority", "assignee", "due_date"]);
    const updated = columns.map((col) => ({
      key: col.key,
      visible: col.required === true || defaults.has(col.key),
    }));
    onChange(updated);
  }, [columns, onChange]);

  const visibleCount = columns.filter((c) => c.visible).length;

  return (
    <div ref={containerRef} className={cn("relative", className)}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className={cn(
          "flex items-center gap-1.5 rounded-md border border-border bg-background px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:border-border/80 hover:bg-muted hover:text-foreground",
          open && "border-primary/50 bg-muted text-foreground",
        )}
        aria-haspopup="listbox"
        aria-expanded={open}
      >
        <Columns3 className="h-3.5 w-3.5" />
        Columns
        {visibleCount > 0 && (
          <span className="ml-0.5 rounded-full bg-primary/15 px-1.5 py-px text-[10px] font-semibold text-primary">
            {visibleCount}
          </span>
        )}
      </button>

      {open && (
        <div
          role="listbox"
          aria-label="Toggle columns"
          className="absolute right-0 top-full z-50 mt-1.5 min-w-[180px] rounded-lg border border-border bg-popover shadow-lg"
        >
          <div className="px-1 py-1">
            {columns.map((col) => {
              const isDisabled = col.required === true;
              return (
                <label
                  key={col.key}
                  className={cn(
                    "flex cursor-pointer items-center gap-2.5 rounded px-2 py-1.5 text-xs transition-colors",
                    isDisabled
                      ? "cursor-not-allowed opacity-60"
                      : "hover:bg-muted",
                  )}
                >
                  <input
                    type="checkbox"
                    checked={col.visible}
                    disabled={isDisabled}
                    onChange={(e) => handleToggle(col.key, e.target.checked)}
                    className="h-3.5 w-3.5 cursor-pointer rounded border-input accent-primary disabled:cursor-not-allowed"
                  />
                  <span className="select-none text-foreground">{col.label}</span>
                  {isDisabled && (
                    <span className="ml-auto text-[10px] text-muted-foreground">
                      required
                    </span>
                  )}
                </label>
              );
            })}
          </div>

          <div className="border-t border-border px-2 py-1.5">
            <button
              type="button"
              onClick={handleReset}
              className="text-[11px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            >
              Reset to default
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
