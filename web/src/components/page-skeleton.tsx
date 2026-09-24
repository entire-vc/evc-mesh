/** Suspense fallback for lazy-loaded routes (perf·Б1). AppLayout's <Outlet/>
 * lives inside a `flex-1` region — its height is set by the surrounding
 * flexbox, not by this component's content, so this never causes a layout
 * shift when it's swapped for the real page. `h-full` just fills that
 * already-sized region instead of collapsing to its own content height. */
export function PageSkeleton() {
  return (
    <div className="h-full w-full animate-pulse space-y-4" aria-hidden="true">
      <div className="h-8 w-48 rounded-md bg-muted" />
      <div className="h-4 w-full max-w-2xl rounded bg-muted" />
      <div className="h-4 w-full max-w-xl rounded bg-muted" />
      <div className="mt-6 h-64 w-full rounded-lg bg-muted" />
    </div>
  );
}
