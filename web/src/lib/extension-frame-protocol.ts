/**
 * postMessage protocol between Mesh and the iframe screen of an external
 * extension (docs/extensions/ui-iframe.md). Pure functions, no DOM, so the rules
 * that keep a foreign page in its box are testable on their own.
 *
 * Mesh -> screen:  mesh.context, mesh.context.error, mesh.revoked
 * Screen -> Mesh:  mesh.ready, mesh.resize, mesh.navigate, mesh.toast, mesh.state,
 *                  mesh.app-ready, mesh.app-error
 *
 * A message is acted on only when BOTH hold: it came from the iframe's own
 * window and its origin is exactly the origin of the service the manifest
 * points at. Everything else is dropped without a sound.
 */
export const FRAME_PROTOCOL_VERSION = 1;

export const MIN_FRAME_HEIGHT = 120;
export const MAX_FRAME_HEIGHT = 4000;
const MAX_TOAST_LENGTH = 300;
const MAX_APP_ERROR_LENGTH = 300;
/** Longest section (sub-path plus hash) a screen may put into the Mesh address. */
export const MAX_SECTION_LENGTH = 512;

export type ToastLevel = "info" | "success" | "warning" | "error";

export type FrameCommand =
  | { type: "ready" }
  | { type: "resize"; height: number }
  | { type: "navigate"; path: string }
  | { type: "toast"; level: ToastLevel; message: string }
  | { type: "state"; section: string; replace: boolean }
  | { type: "app-ready" }
  | { type: "app-error"; message: string };

export interface FrameMessageSource {
  origin: string;
  source: unknown;
  data: unknown;
}

/** What the screen is told about where it was opened (the token is in `token`, never in the URL of the host). */
export interface FrameContext {
  token: string;
  expiresAt: string;
  workspaceId: string;
  workspaceSlug: string;
  projectId?: string;
  taskId?: string;
  locale: string;
  /** The section the address points at (`/memory/search#q`, see `normalizeSection`); "" for the screen's start. */
  section?: string;
}

export function contextMessage(ctx: FrameContext) {
  return {
    type: "mesh.context",
    version: FRAME_PROTOCOL_VERSION,
    token: ctx.token,
    expires_at: ctx.expiresAt,
    workspace_id: ctx.workspaceId,
    workspace_slug: ctx.workspaceSlug,
    project_id: ctx.projectId ?? null,
    task_id: ctx.taskId ?? null,
    locale: ctx.locale,
    path: ctx.section ?? "",
  };
}

/** Why a context refresh failed, as told to the screen. */
export type ContextErrorReason = "unauthorized" | "forbidden" | "not_found" | "conflict" | "unavailable" | "network";

/**
 * Map the HTTP status of a refused ui-context request to a reason. `0` (no
 * status: the request never got an answer) is `network`.
 */
export function contextErrorReason(status: number): ContextErrorReason {
  if (status === 401) return "unauthorized";
  if (status === 403) return "forbidden";
  if (status === 404) return "not_found";
  if (status === 409) return "conflict";
  if (status === 0) return "network";
  return "unavailable";
}

/**
 * A refusal that means the screen has lost its right to run here: the person
 * left the workspace (403), the extension or its screen is gone (404), or it
 * was switched off / its host is no longer allowed (409). A 401, a 5xx or a
 * network failure is transient and does not revoke.
 */
export function isRevocation(status: number): boolean {
  return status === 403 || status === 404 || status === 409;
}

export function contextErrorMessage(status: number) {
  return {
    type: "mesh.context.error",
    version: FRAME_PROTOCOL_VERSION,
    status: status || null,
    reason: contextErrorReason(status),
  };
}

export function revokedMessage(status: number) {
  return {
    type: "mesh.revoked",
    version: FRAME_PROTOCOL_VERSION,
    reason: contextErrorReason(status),
  };
}

/** Share of the token's lifetime after which Mesh renews it on its own. */
export const TOKEN_REFRESH_SHARE = 0.8;
/** Never renew more often than this, whatever the clocks say. */
export const MIN_REFRESH_DELAY_MS = 5_000;

/**
 * Delay before the proactive renewal of a token that expires at `expiresAt`,
 * counted from `now` (the moment the token arrived). A skewed or unparsable
 * expiry falls back to the floor rather than to a busy loop.
 */
export function refreshDelayMs(expiresAt: string, now: number): number {
  const exp = Date.parse(expiresAt);
  if (!Number.isFinite(exp)) return MIN_REFRESH_DELAY_MS;
  return Math.max(MIN_REFRESH_DELAY_MS, Math.floor((exp - now) * TOKEN_REFRESH_SHARE));
}

/**
 * Parse one window message. Returns null for anything that is not a well-formed
 * command from the expected frame: wrong sender window, wrong origin, unknown
 * type, bad payload.
 */
export function parseFrameMessage(
  ev: FrameMessageSource,
  expected: { origin: string; frameWindow: unknown },
): FrameCommand | null {
  if (!expected.frameWindow || ev.source !== expected.frameWindow) return null;
  if (ev.origin !== expected.origin) return null;
  const d = ev.data;
  if (!d || typeof d !== "object") return null;
  const m = d as Record<string, unknown>;
  switch (m.type) {
    case "mesh.ready":
      return { type: "ready" };
    case "mesh.resize": {
      const h = Number(m.height);
      if (!Number.isFinite(h)) return null;
      return { type: "resize", height: Math.min(MAX_FRAME_HEIGHT, Math.max(MIN_FRAME_HEIGHT, Math.round(h))) };
    }
    case "mesh.navigate": {
      const path = typeof m.path === "string" ? m.path : "";
      return isSafeAppPath(path) ? { type: "navigate", path } : null;
    }
    case "mesh.toast": {
      const message = typeof m.message === "string" ? m.message.trim().slice(0, MAX_TOAST_LENGTH) : "";
      if (!message) return null;
      const level = m.level === "success" || m.level === "warning" || m.level === "error" ? m.level : "info";
      return { type: "toast", level, message };
    }
    case "mesh.state": {
      const section = normalizeSection(m.path);
      return section === null ? null : { type: "state", section, replace: m.replace === true };
    }
    case "mesh.app-ready":
      return { type: "app-ready" };
    case "mesh.app-error": {
      const message = typeof m.message === "string" ? m.message.trim().slice(0, MAX_APP_ERROR_LENGTH) : "";
      return { type: "app-error", message };
    }
    default:
      return null;
  }
}

/**
 * A screen may send the user to a place inside Mesh, nowhere else: a single
 * leading slash, no scheme, no protocol-relative form, no backslash tricks, no
 * control characters.
 */
export function isSafeAppPath(path: string): boolean {
  if (!path.startsWith("/") || path.startsWith("//")) return false;
  // eslint-disable-next-line no-control-regex
  if (/[\\\u0000-\u001f]/.test(path)) return false;
  try {
    const u = new URL(path, "https://mesh.invalid");
    return u.origin === "https://mesh.invalid";
  } catch {
    return false;
  }
}

// Path characters a section may use (RFC 3986 pchar and "/"), percent-escapes included.
const SECTION_PATH = /^(\/([A-Za-z0-9\-._~!$&'()*+,;=:@]|%[0-9A-Fa-f]{2})*)*$/;
// The hash holds the same characters plus "/", and no "?" either: the section carries no query.
const SECTION_HASH = /^(#([A-Za-z0-9\-._~!$&'()*+,;=:@/]|%[0-9A-Fa-f]{2})*)?$/;

/**
 * A section of an extension screen: a sub-path under the screen's own path and
 * an optional `#hash`, e.g. `/memory/search#recent`. It travels both ways (the
 * Mesh address `/w/<ws>/x/<ext>/memory/search` and `mesh.state`), so it is held
 * to a narrow form: a leading `/`, no query, no `//`, no `.`/`..` segment, no
 * encoded separator (`%2F`, `%5C`), no `%` without two hex digits, no backslash
 * or control character. Returns the section without a trailing `/`
 * ("" for the start of the screen), or null when the value is not a section.
 */
export function normalizeSection(raw: unknown): string | null {
  if (typeof raw !== "string") return null;
  if (raw === "" || raw === "/") return "";
  if (raw.length > MAX_SECTION_LENGTH) return null;
  const at = raw.indexOf("#");
  let path = at < 0 ? raw : raw.slice(0, at);
  const hash = at < 0 ? "" : raw.slice(at);
  if (path !== "" && !path.startsWith("/")) return null;
  // Before the trailing slash is trimmed, so "//" is refused rather than read as "/".
  if (path.includes("//")) return null;
  if (path.length > 1 && path.endsWith("/")) path = path.slice(0, -1);
  if (path === "/") path = "";
  if (!SECTION_PATH.test(path)) return null;
  // An encoded separator (%2F, %5C) would hide a dot segment ("/..%2f..") from the check below.
  if (/%(2f|5c)/i.test(path)) return null;
  // A dot segment, also percent-encoded (`%2e%2E`), would climb out of the screen's path.
  if (path.split("/").some((seg) => /^(\.|%2e){1,2}$/i.test(seg))) return null;
  if (!SECTION_HASH.test(hash)) return null;
  // The hash is as opaque as the path: an encoded separator there is refused too.
  if (/%(2f|5c)/i.test(hash)) return null;
  return hash === "#" ? path : path + hash;
}

/**
 * The iframe src: the screen address, the section under it, plus the context
 * token (the screen verifies it against the JWKS). The section only extends the
 * path, so the origin is always the screen's own.
 */
export function frameSrc(frameUrl: string, token: string, section = ""): string {
  const u = new URL(frameUrl);
  const s = normalizeSection(section);
  if (s) {
    const at = s.indexOf("#");
    const path = at < 0 ? s : s.slice(0, at);
    if (path) u.pathname = u.pathname.replace(/\/+$/, "") + path;
    if (at >= 0) u.hash = s.slice(at);
  }
  u.searchParams.set("token", token);
  return u.toString();
}
