import { loadRuntimeFlags } from "@/lib/runtime-flags";

/** Warm the chunks of the screens a signed-in user opens next (dashboard and
 * board). Route-splitting (perf·Б1) made them separate chunks the browser
 * hasn't fetched yet — without a warm-up the first click on a project pays
 * the chunk download (and Vite's 30 modulepreload links for the board's
 * dependencies) on the click itself.
 *
 * Two callers, one warm-up:
 *  - the login page, right after a successful login, so the chunks load while
 *    the login → dashboard navigation happens;
 *  - `AppLayout`, so a user who already has a session (page reload, bookmark,
 *    second tab — the common case) gets the same warm-up. Before, only the
 *    login submit did it, and a reload with a live session paid for the
 *    chunks on the first project click.
 *
 * Runs on idle (never competes with the transition or first paint) and is a
 * pure prefetch: the dynamic import() result is discarded, React still does
 * its own import() when the route renders, they just both hit the browser's
 * module cache so the second one is instant. Only ever runs once per page
 * load, however many callers reach it.
 *
 * Skipped on data-saver connections: there the download is the cost, and the
 * user has asked not to pay it for a screen they may never open.
 *
 * Gated by `prefetchNextRoute` (see runtime-flags.ts) so it can be killed
 * without a redeploy if it ever turns out to hurt instead of help. */
let scheduled = false;

function dataSaverOn(): boolean {
  const connection = (navigator as Navigator & { connection?: { saveData?: boolean } })
    .connection;
  return connection?.saveData === true;
}

/** Call from the signed-in shell (`AppLayout`) and right after a login. */
export function prefetchNextRouteOnIdle(): void {
  if (scheduled || dataSaverOn()) return;
  scheduled = true;
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
