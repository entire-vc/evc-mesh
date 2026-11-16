// Pieces shared by the mesh-default widget views: the four states of a body and short time labels.
import type { ElementType, MouseEvent, ReactNode } from "react";
import i18n from "@/i18n";
import { Skeleton } from "@/components/ui/skeleton";
import type { Async } from "../../types";

export function formatShort(isoStr: string): string {
  const diff = Date.now() - new Date(isoStr).getTime();
  if (diff < 60_000) return i18n.t("notifications:dashboard.short.now");
  if (diff < 3_600_000) return i18n.t("notifications:dashboard.short.minutes", { count: Math.floor(diff / 60_000) });
  if (diff < 86_400_000) return i18n.t("notifications:dashboard.short.hours", { count: Math.floor(diff / 3_600_000) });
  return i18n.t("notifications:dashboard.short.days", { count: Math.floor(diff / 86_400_000) });
}

export function WidgetSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2" aria-busy="true">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-9 w-full rounded-md" />
      ))}
    </div>
  );
}

export function WidgetEmpty({ icon: Icon, message }: { icon: ElementType; message: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-8 text-center text-muted-foreground">
      <Icon className="mb-2 h-6 w-6 opacity-30" />
      <p className="text-xs">{message}</p>
    </div>
  );
}

export function WidgetError({ error, retry }: { error: Error; retry: () => void }) {
  return (
    <div role="alert" className="flex flex-col items-center justify-center gap-2 py-8 text-center text-xs text-muted-foreground">
      <p>{error.message}</p>
      <button type="button" className="underline hover:text-foreground" onClick={retry}>
        {i18n.t("common:retry", { defaultValue: "Retry" })}
      </button>
    </div>
  );
}

/** Card + body; loading / error / empty / list are four different things. */
export function Body<D>({
  result,
  rows,
  isEmpty,
  empty,
  children,
}: {
  result: Async<D>;
  rows: number;
  isEmpty: (v: D) => boolean;
  empty: ReactNode;
  children: (v: D) => ReactNode;
}) {
  if (result.state === "loading") return <WidgetSkeleton rows={rows} />;
  if (result.state === "error") return <WidgetError error={result.error} retry={result.retry} />;
  return isEmpty(result.value) ? <>{empty}</> : <>{children(result.value)}</>;
}

export const plainClick = (e: MouseEvent) => !(e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey);

