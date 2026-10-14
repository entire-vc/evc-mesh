import i18n from "i18next";
import { miscT } from "@/lib/misc-t";

function firstDetailMessage(details: unknown): string | undefined {
  if (!details || typeof details !== "object") return undefined;
  const first = Object.values(details as Record<string, unknown>).find(
    (v) => typeof v === "string" && v.trim() !== "",
  );
  return typeof first === "string" ? first : undefined;
}

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
 * Translation by code (above) comes first: errors carry a stable `errorCode`
 * and the interface owns the text per language (locales: <lang>/apiErrors.json, a lazy namespace).
 *
 * Duck-typed rather than `instanceof ApiRequestError`: this is called from
 * components whose tests mock `@/lib/api` wholesale, where the real class is
 * not the one that was thrown.
 */
export function apiErrorMessage(
  err: unknown,
  fallback = miscT("errors.failedToSave", "Failed to save"),
): string {
  // A code the server catalog knows is shown in the user's language; this wins
  // over `details`, which are English field messages. A code the interface has
  // no entry for falls through to the rules below.
  const errorCode = (err as { errorCode?: unknown } | null)?.errorCode;
  if (typeof errorCode === "string") {
    const key = `apiErrors:${errorCode}`;
    if (i18n.hasLoadedNamespace("apiErrors")) {
      if (i18n.exists(key)) {
        const translated = i18n.t(key);
        // Field details (English, from the server) say which field is wrong, which
        // the translated sentence cannot: append the first one. Field keys stay hidden.
        const first = firstDetailMessage((err as { details?: unknown } | null)?.details);
        return first ? `${translated.replace(/\.$/, "")}: ${first}` : translated;
      }
    } else {
      // The catalog is its own lazy chunk (not in the login bundle): start the
      // fetch and fall through to the server's English message for this one.
      void i18n.loadNamespaces("apiErrors");
    }
  }
  // A coded error the interface has no text for: the server message is English by contract.
  if (typeof errorCode === "string" && errorCode !== "") {
    const msg = (err as { message?: unknown } | null)?.message;
    if (typeof msg === "string" && msg.trim() !== "") return msg;
  }
  const detail = firstDetailMessage((err as { details?: unknown } | null)?.details);
  if (detail) return detail;
  const isApiError =
    typeof (err as { code?: unknown } | null)?.code === "string" &&
    typeof (err as { status?: unknown } | null)?.status === "number";
  if (isApiError) return fallback;
  const message = (err as { message?: unknown } | null)?.message;
  if (typeof message === "string" && message.trim() !== "") return message;
  return fallback;
}
