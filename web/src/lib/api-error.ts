/**
 * The sentence to put in front of the person who caused the error.
 *
 * ApiRequestError carries two things: `message`, which for a 400 is the
 * server's generic "Validation failed", and `details`, which is the per-field
 * map saying what was actually wrong. Showing only the message turns a
 * refusal the author could act on — "no workspace member or agent named
 * @daedelus is known in this workspace" — into two words that explain nothing,
 * which is barely better than the silent no-op it replaced.
 *
 * `details` is per-field validation output, generated deterministically by
 * the server's own validators — not free-form prose — so it's shown as-is.
 * A bare `.message` on an API error is different: it's `err.message ||
 * err.error || "Request failed"` from api.ts, i.e. whatever string the
 * backend happened to put in the response body, unreviewed by anyone who
 * owns product copy (§1r.A). That case falls back to the caller's own
 * approved string instead of the server's. Non-API errors (a thrown
 * client-side Error, a network TypeError) are unaffected — their `.message`
 * is our own text, not the server's, and keeps showing as before.
 *
 * Duck-typed rather than `instanceof ApiRequestError`: this is called from
 * components whose tests mock `@/lib/api` wholesale, where the real class is
 * not the one that was thrown.
 */
export function apiErrorMessage(err: unknown, fallback = "Failed to save"): string {
  const details = (err as { details?: unknown } | null)?.details;
  if (details && typeof details === "object") {
    const first = Object.values(details as Record<string, unknown>).find(
      (v) => typeof v === "string" && v.trim() !== "",
    );
    if (typeof first === "string") return first;
  }
  const isApiError =
    typeof (err as { code?: unknown } | null)?.code === "string" &&
    typeof (err as { status?: unknown } | null)?.status === "number";
  if (isApiError) return fallback;
  const message = (err as { message?: unknown } | null)?.message;
  if (typeof message === "string" && message.trim() !== "") return message;
  return fallback;
}
