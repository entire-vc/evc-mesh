import { Skeleton } from "@/components/ui/skeleton";

/** First frame of the app route. web/index.html paints a static copy of this
 * markup before the bundle loads (static-shell-gate diffs the two), so it is
 * shared by AppLayout's auth-loading state and the top-level Suspense fallback:
 * change it here and in index.html together. */
export function AuthLoadingSkeleton() {
  return (
    <div className="flex h-screen items-center justify-center bg-background">
      <div className="space-y-4 text-center">
        <Skeleton className="mx-auto h-8 w-8 rounded-full" />
        <Skeleton className="h-4 w-32" />
      </div>
    </div>
  );
}
