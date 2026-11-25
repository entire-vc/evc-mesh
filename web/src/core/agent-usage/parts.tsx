// Small pieces the statistics panels share: the segmented switch, the states (loading, error, empty), the row
// menu and the truncating name.
import type { ReactNode } from "react";
import { ChevronRight, MoreHorizontal } from "lucide-react";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/cn";
import type { ST } from "./format";

export function Segmented<T extends string>({
  value,
  options,
  onChange,
  label,
  className,
}: {
  value: T | null;
  options: { id: T; label: string }[];
  onChange: (v: T) => void;
  label: string;
  className?: string;
}) {
  return (
    <div role="radiogroup" aria-label={label} className={cn("inline-flex min-w-0 shrink-0 flex-wrap items-center gap-0.5 rounded-lg bg-muted p-0.5", className)}>
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          role="radio"
          aria-checked={value === o.id}
          onClick={() => onChange(o.id)}
          className={cn(
            "whitespace-nowrap rounded-md px-2.5 py-1 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            value === o.id ? "bg-background font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function LoadingBody({ st, rows = 5 }: { st: ST; rows?: number }) {
  return (
    <div aria-busy="true" aria-label={st("loading", "Loading")} className="space-y-4" data-testid="stats-loading">
      <Skeleton className="h-8 w-64 max-w-full rounded-md" />
      <Skeleton className="h-48 w-full rounded-lg" />
      <div className="space-y-2">
        {Array.from({ length: rows }).map((_, i) => (
          <Skeleton key={i} className="h-9 w-full rounded-md" />
        ))}
      </div>
    </div>
  );
}

export function ErrorBody({ st, error, retry }: { st: ST; error: Error; retry: () => void }) {
  return (
    <div role="alert" className="flex flex-col items-center justify-center gap-3 rounded-lg border border-border py-10 text-center text-sm text-muted-foreground">
      <p className="max-w-full break-words px-4">{error.message || st("error", "Could not load the statistics")}</p>
      <button type="button" onClick={retry} className="rounded-lg border border-border px-3 py-1.5 text-foreground hover:bg-accent">
        {st("retry", "Retry")}
      </button>
    </div>
  );
}

export function Name({ text, color, hatch, className }: { text: string; color?: string; hatch?: boolean; className?: string }) {
  return (
    <span className={cn("flex min-w-0 items-center gap-2", className)}>
      {hatch ? (
        <span aria-hidden="true" className="h-2.5 w-2.5 shrink-0 rounded-sm bg-[repeating-linear-gradient(135deg,var(--status-backlog)_0_2px,transparent_2px_4px)]" />
      ) : color ? (
        <span aria-hidden="true" className="h-2.5 w-2.5 shrink-0 rounded-sm" style={{ backgroundColor: color }} />
      ) : null}
      <span className="min-w-0 truncate" title={text}>
        {text}
      </span>
    </span>
  );
}

export interface RowAction {
  id: string;
  label: string;
  run: () => void;
}

/** The «⋯» of a table row. */
export function RowMenu({ actions, label }: { actions: RowAction[]; label: string }) {
  if (!actions.length) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger aria-label={label} className="rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground">
        <MoreHorizontal className="h-4 w-4" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {actions.map((a) => (
          <DropdownMenuItem key={a.id} onClick={a.run} className="text-xs">
            {a.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** A row as a card (narrow screens): the header is the menu trigger, the arrow says so. */
export function RowCard({ head, children, actions, label, testId }: { head: ReactNode; children?: ReactNode; actions: RowAction[]; label: string; testId?: string }) {
  const body = (
    <>
      <div className="flex min-w-0 items-center gap-2">{head}{actions.length > 0 && <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />}</div>
      {children}
    </>
  );
  if (!actions.length) {
    return (
      <li data-testid={testId} className="min-w-0 border-b border-border px-4 py-3 last:border-b-0">
        {body}
      </li>
    );
  }
  return (
    <li data-testid={testId} className="min-w-0 border-b border-border last:border-b-0 [&>div]:block">
      <DropdownMenu>
        <DropdownMenuTrigger aria-label={label} className="block w-full min-w-0 px-4 py-3 text-left hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring">
          {body}
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {actions.map((a) => (
            <DropdownMenuItem key={a.id} onClick={a.run} className="text-sm">
              {a.label}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  );
}

export function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <p className="truncate text-[11px] text-muted-foreground">{label}</p>
      <p className="truncate text-sm tabular-nums">{value}</p>
    </div>
  );
}

export function ShareBar({ share, color, className }: { share: number; color: string; className?: string }) {
  return (
    <div className={cn("h-1.5 min-w-0 overflow-hidden rounded-full bg-muted", className)}>
      <div className="h-full rounded-full" style={{ width: `${Math.max(0, Math.min(1, share)) * 100}%`, backgroundColor: color, minWidth: share > 0 ? 3 : 0 }} />
    </div>
  );
}
