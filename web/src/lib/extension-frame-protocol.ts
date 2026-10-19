/**
 * postMessage protocol between Mesh and the iframe screen of an external
 * extension (docs/extensions/ui-iframe.md). Pure functions, no DOM, so the rules
 * that keep a foreign page in its box are testable on their own.
 *
 * Mesh -> screen:  mesh.context
 * Screen -> Mesh:  mesh.ready, mesh.resize, mesh.navigate, mesh.toast
 *
 * A message is acted on only when BOTH hold: it came from the iframe's own
 * window and its origin is exactly the origin of the service the manifest
 * points at. Everything else is dropped without a sound.
 */
export const FRAME_PROTOCOL_VERSION = 1;

export const MIN_FRAME_HEIGHT = 120;
export const MAX_FRAME_HEIGHT = 4000;
const MAX_TOAST_LENGTH = 300;

export type ToastLevel = "info" | "success" | "warning" | "error";

export type FrameCommand =
  | { type: "ready" }
  | { type: "resize"; height: number }
  | { type: "navigate"; path: string }
  | { type: "toast"; level: ToastLevel; message: string };

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
  };
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

/** The iframe src: the screen address plus the context token (the screen verifies it against the JWKS). */
export function frameSrc(frameUrl: string, token: string): string {
  const u = new URL(frameUrl);
  u.searchParams.set("token", token);
  return u.toString();
}
