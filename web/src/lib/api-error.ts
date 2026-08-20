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
  const message = (err as { message?: unknown } | null)?.message;
  if (typeof message === "string" && message.trim() !== "") return message;
  return fallback;
}
