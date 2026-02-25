import { useCallback, useEffect, useRef, useState } from "react";
import { Calendar } from "lucide-react";
import { cn } from "@/lib/cn";

interface DatePickerPopoverProps {
  value: string | null;
  onChange: (date: string | null) => void;
  placeholder?: string;
  className?: string;
  showClearButton?: boolean;
}

function parseDateParts(isoDate: string): [number, number, number] {
  const parts = isoDate.split("-").map(Number);
  return [parts[0] ?? 0, parts[1] ?? 1, parts[2] ?? 1];
}

function formatDisplayDate(isoDate: string): string {
  // isoDate is YYYY-MM-DD; parse as local date to avoid UTC offset shift.
  const [year, month, day] = parseDateParts(isoDate);
  const d = new Date(year, month - 1, day);
  return d.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}

function isPast(isoDate: string): boolean {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const [year, month, day] = parseDateParts(isoDate);
  const date = new Date(year, month - 1, day);
  return date < today;
}

export function DatePickerPopover({
  value,
  onChange,
  placeholder = "Set due date",
  className,
  showClearButton = true,
}: DatePickerPopoverProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  // Close on outside click.
  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (
        containerRef.current &&
        !containerRef.current.contains(event.target as Node)
      ) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  // Focus the date input when popover opens.
  useEffect(() => {
    if (open && inputRef.current) {
      inputRef.current.focus();
    }
  }, [open]);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
      }
    },
    [],
  );

  const handleDateChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const raw = e.target.value;
      onChange(raw || null);
      if (raw) {
        setOpen(false);
      }
    },
    [onChange],
  );

  const handleClear = useCallback(() => {
    onChange(null);
    setOpen(false);
  }, [onChange]);

  const past = value ? isPast(value) : false;

  return (
    <div
      ref={containerRef}
      className={cn("relative inline-block", className)}
      onKeyDown={handleKeyDown}
    >
      {/* Trigger */}
      <button
        type="button"
        onClick={() => setOpen((prev) => !prev)}
        className={cn(
          "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-sm transition-colors",
          "hover:bg-accent hover:text-accent-foreground",
          value
            ? past
              ? "text-red-500"
              : "text-foreground"
            : "text-muted-foreground",
        )}
      >
        <Calendar
          className={cn("h-3.5 w-3.5 shrink-0", past && value && "text-red-500")}
        />
        <span>{value ? formatDisplayDate(value) : placeholder}</span>
      </button>

      {/* Popover */}
      {open && (
        <div className="absolute left-0 top-full z-50 mt-1 min-w-[220px] rounded-lg border border-border bg-popover p-3 text-popover-foreground shadow-lg">
          <p className="mb-2 text-xs font-medium text-muted-foreground">
            Pick a date
          </p>
          <input
            ref={inputRef}
            type="date"
            value={value ?? ""}
            onChange={handleDateChange}
            className={cn(
              "w-full rounded-md border border-input bg-background px-2 py-1.5 text-sm",
              "transition-colors focus:outline-none focus:ring-2 focus:ring-ring",
            )}
          />
          {showClearButton && value && (
            <button
              type="button"
              onClick={handleClear}
              className={cn(
                "mt-2 w-full rounded-md px-2 py-1 text-xs text-muted-foreground",
                "transition-colors hover:bg-accent hover:text-accent-foreground",
              )}
            >
              Clear date
            </button>
          )}
        </div>
      )}
    </div>
  );
}
