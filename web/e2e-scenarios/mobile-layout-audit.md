# Authenticated mobile layout audit

`node scripts/check-mobile-layout.mjs` runs a read-only browser audit against
an explicitly configured deployment or local preview. It captures board, task,
document list, document detail, analytics, sessions (including Cost Tracking)
and workspace settings at 393×852 and 1440×900, in light and dark themes.

Required environment: `AUDIT_BASE_URL`, `AUDIT_PROJECT_ID`,
`AUDIT_WORKSPACE_SLUG`, `AUDIT_TASK_ID`, `E2E_USER_EMAIL`, `E2E_USER_PASSWORD`.
The account must already have access to that project/workspace. Credentials
are consumed from the environment and never saved in the artifacts.

Optional environment: `AUDIT_DOCUMENT_ID` selects an existing document
(otherwise the first root document is used); `AUDIT_OUTPUT_DIR` controls the
artifact directory; `AUDIT_BROWSER_CHANNEL=chrome` uses installed Chrome.
Without that option, install Playwright Chromium first.
`AUDIT_ASSET_DIR` may point to a candidate Vite `dist` directory: browser
routing serves those files on `AUDIT_BASE_URL` while `/api/` requests use the
real deployment. This avoids a local reverse proxy and leaves production
unchanged. Such captures prove candidate rendering, not deployment; omit
this option for the final live check after deployment.

The command fails on missing credentials, missing document fixtures, wrong
viewport/theme, page/API errors, root/main horizontal overflow, or visible
mobile targets smaller than 44×44 CSS pixels. The task page's main controls
are measured and reported but exempted from the size assertion because task
create/edit forms are frozen. The shared task header is still asserted.
Internal board columns and task tabs may scroll; overflow of the containing
page is a failure. Targets hidden by opacity or outside a scroll container
are not counted as visible. Desktop target sizes are recorded for comparison;
the 44 px size assertion applies to the mobile audit.

The CSS viewport uses Playwright `viewport`, with touch enabled. It does not
use headless Chrome's `--window-size`, nor depend on the browser widening the
layout viewport when an overflowing page is run with `isMobile: true`.
Screenshots wait for the authenticated header, nonempty main content and
completion of API requests, then reset the main scroll position. Cost Tracking
and its Most expensive tasks section must be present; both scroll positions
are checked for overflow and mobile hit areas. Routes change within the same authenticated SPA to avoid
aborting a rotating refresh-cookie request during full-page navigation.
Review the screenshots visually as well as reading `metrics.json`.

The output contains 28 screen captures, eight extra Cost Tracking captures (top/bottom),
seven mobile crops, `metrics.json`, and `verdict.json`. Screenshots and titles
may contain workspace data: store them as private review artifacts. Cookies,
traces, storageState and API response bodies are never persisted. One browser
context and one login are shared for the whole audit, so a saved rotating
refresh cookie is never replayed into another context.

For a red control, run against a deployment containing the old layout: the
board's absolutely positioned screen-reader labels escape its scroll container,
the analytics project filter is clipped, and settings tabs overflow main.
After fixing, run the same command against the candidate preview. A negative
result exits 1; `AUDIT_COLLECT_ONLY=1` preserves that result in `verdict.json`
while returning 0 solely for collecting a baseline (never use it as a pass).
