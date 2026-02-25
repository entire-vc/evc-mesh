import { useCallback, useEffect, useRef, useState } from "react";
import { Calendar, X } from "lucide-react";
import { cn } from "@/lib/cn";

interface DatePickerPopoverProps {
  /** ISO date string (YYYY-MM-DD) or datetime-local string (YYYY-MM-DDTHH:mm) */
  value: string | null;
  onChange: (date: string | null) => void;
  placeholder?: string;
  className?: string;
  showClearButton?: boolean;
  /** Use datetime-local input instead of date */
  includeTime?: boolean;
}

function parseDateParts(isoDate: string): [number, number, number] {
  const dateOnly = isoDate.split("T")[0] ?? isoDate;
  const parts = dateOnly.split("-").map(Number);
  return [parts[0] ?? 0, parts[1] ?? 1, parts[2] ?? 1];
}

function parseTimeParts(isoDate: string): [number, number] {
  const timePart = isoDate.split("T")[1];
  if (!timePart) return [0, 0];
  const parts = timePart.split(":").map(Number);
  return [parts[0] ?? 0, parts[1] ?? 0];
}

function formatDisplayDate(isoDate: string, includeTime?: boolean): string {
  const [year, month, day] = parseDateParts(isoDate);
  const d = new Date(year, month - 1, day);
  const dateStr = d.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
  if (includeTime) {
    const [hours, minutes] = parseTimeParts(isoDate);
    if (hours || minutes) {
      const h = String(hours).padStart(2, "0");
      const m = String(minutes).padStart(2, "0");
      return `${dateStr} ${h}:${m}`;
    }
  }
  return dateStr;
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
  includeTime = false,
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
      inputRef.current.showPicker?.();
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
      // Only auto-close for date-only; datetime users may still pick time
      if (raw && !includeTime) {
        setOpen(false);
      }
    },
    [onChange, includeTime],
  );

  const handleClear = useCallback(() => {
    onChange(null);
    setOpen(false);
  }, [onChange]);

  const past = value ? isPast(value) : false;
  const inputType = includeTime ? "datetime-local" : "date";

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
        <span>{value ? formatDisplayDate(value, includeTime) : placeholder}</span>
        {showClearButton && value && (
          <span
            role="button"
            className="ml-0.5 rounded-full p-0.5 hover:bg-muted"
            onClick={(e) => {
              e.stopPropagation();
              handleClear();
            }}
          >
            <X className="h-3 w-3" />
          </span>
        )}
      </button>

      {/* Popover */}
      {open && (
        <div className="absolute left-0 top-full z-50 mt-1 min-w-[240px] rounded-lg border border-border bg-popover p-3 text-popover-foreground shadow-lg">
          <p className="mb-2 text-xs font-medium text-muted-foreground">
            {includeTime ? "Pick date & time" : "Pick a date"}
          </p>
          <input
            ref={inputRef}
            type={inputType}
            value={value ?? ""}
            onChange={handleDateChange}
            className={cn(
              "w-full rounded-md border border-input bg-background px-2 py-1.5 text-sm",
              "transition-colors focus:outline-none focus:ring-2 focus:ring-ring",
            )}
          />
          <div className="mt-2 flex items-center gap-2">
            {includeTime && value && (
              <button
                type="button"
                onClick={() => setOpen(false)}
                className={cn(
                  "flex-1 rounded-md bg-primary px-2 py-1 text-xs text-primary-foreground",
                  "transition-colors hover:bg-primary/90",
                )}
              >
                Done
              </button>
            )}
            {showClearButton && value && (
              <button
                type="button"
                onClick={handleClear}
                className={cn(
                  "flex-1 rounded-md px-2 py-1 text-xs text-muted-foreground",
                  "transition-colors hover:bg-accent hover:text-accent-foreground",
                )}
              >
                Clear
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
