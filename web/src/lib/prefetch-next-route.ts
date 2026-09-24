import { loadRuntimeFlags } from "@/lib/runtime-flags";

/** Call once right after a successful login. Route-splitting (perf·Б1) means
 * the dashboard the user lands on next is now a separate chunk the browser
 * hasn't fetched yet — without this, splitting the bundle would make the
 * post-login transition slower than it was before, which defeats the point.
 *
 * Runs on idle (never competes with the login → navigate transition itself)
 * and is a pure prefetch: the dynamic import() result is discarded, React
 * still does its own import() when the route actually renders, they just
 * both hit the browser's module cache so the second one is instant.
 *
 * Gated by `prefetchNextRoute` (see runtime-flags.ts) so it can be killed
 * without a redeploy if it ever turns out to hurt instead of help. */
export function prefetchNextRouteAfterLogin(): void {
  loadRuntimeFlags().then((flags) => {
    if (!flags.prefetchNextRoute) return;
    const prefetch = () => {
      // Dashboard is where "/" redirects post-login (AppLayout); board is
      // the single most-used screen once there. Both or neither could be
      // wrong for a given workspace, but both are cheap to warm.
      import("@/pages/dashboard").catch(() => {});
      import("@/pages/board").catch(() => {});
    };
    if ("requestIdleCallback" in window) {
      window.requestIdleCallback(prefetch, { timeout: 2000 });
    } else {
      setTimeout(prefetch, 300);
    }
  });
}
