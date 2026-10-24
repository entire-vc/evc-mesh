// Where theme runtime problems go. The instance has no client-log endpoint
// yet, so the sink is the console plus a DOM event and a bounded in-memory
// ring (`window.__meshThemeIssues`) that a future journal shipper, e2e or an
// admin reads. Never throws: logging must not be the second failure.

export type ThemeIssueCode =
  | "contract-mismatch"
  | "view-crashed"
  | "bundle-load-failed"
  | "bundle-invalid"
  | "theme-fetch-failed";

export interface ThemeIssue {
  code: ThemeIssueCode;
  theme: string;
  slot?: string;
  message: string;
  at: string;
}

const RING = 50;

export function reportThemeIssue(issue: Omit<ThemeIssue, "at">): void {
  try {
    const full: ThemeIssue = { ...issue, at: new Date().toISOString() };
    const w = window as unknown as { __meshThemeIssues?: ThemeIssue[] };
    const ring = (w.__meshThemeIssues ??= []);
    ring.push(full);
    if (ring.length > RING) ring.shift();
    console.warn(`[mesh-theme] ${full.code} theme=${full.theme}${full.slot ? ` slot=${full.slot}` : ""}: ${full.message}`);
    window.dispatchEvent(new CustomEvent("mesh:theme-issue", { detail: full }));
  } catch {
    /* logging is best-effort */
  }
}

/** Text of a thrown value for the log (never shown to the user, so no copy approval applies). */
export function errText(e: unknown): string {
  return e instanceof Error ? String(e.message) : String(e);
}
