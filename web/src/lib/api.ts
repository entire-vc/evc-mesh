import type { ApiError, RefreshResponse } from "@/types";

const BASE_URL = import.meta.env.VITE_API_URL || "";

// The access token lives ONLY here — a module-scoped variable, gone the
// instant the tab reloads or closes. It is never written to localStorage,
// sessionStorage, or a cookie: an XSS payload or a malicious extension can
// still read process memory, but it can no longer walk out with a token that
// survives the page it stole it from.
let accessToken: string | null = null;
let refreshPromise: Promise<string> | null = null;

// One-time migration: earlier versions of this app kept both tokens in
// localStorage. Strip them unconditionally on every load so an upgraded
// browser doesn't keep sitting on exactly the value this rewrite exists to
// stop exposing — this runs regardless of whether the user is logged in.
try {
  localStorage.removeItem("access_token");
  localStorage.removeItem("refresh_token");
} catch {
  // localStorage can throw in locked-down contexts (private browsing in some
  // older engines, disabled storage). Nothing to clean up if it's unusable.
}

export function setAccessToken(access: string) {
  accessToken = access;
}

export function clearAccessToken() {
  accessToken = null;
}

// Best-effort clear of any non-httpOnly session cookies left by a previous
// Mesh/Casdoor instance. Cannot touch httpOnly cookies — those expire naturally.
// (The current refresh_token cookie IS httpOnly and is not, and cannot be,
// touched here — only the server can clear it, via POST /auth/logout.)
export function clearSessionCookies() {
  const cookiesToClear = ["mesh_session", "casdoor_session", "session"];
  for (const name of cookiesToClear) {
    document.cookie = `${name}=; max-age=0; path=/; SameSite=Lax`;
    document.cookie = `${name}=; max-age=0; path=/; domain=${location.hostname}; SameSite=Lax`;
  }
}

export function getAccessToken(): string | null {
  return accessToken;
}

// The refresh endpoint reads the token from an httpOnly cookie the browser
// attaches automatically — nothing to send here but the request itself.
// credentials: "include" is what makes the browser both attach that cookie
// and store the rotated one the response sets.
async function refreshOnce(): Promise<string> {
  const res = await fetch(`${BASE_URL}/api/v1/auth/refresh`, {
    method: "POST",
    credentials: "include",
  });

  if (!res.ok) {
    accessToken = null;
    throw new ApiRequestError("Session expired", "UNAUTHORIZED", 401);
  }

  const data = (await res.json()) as RefreshResponse;
  accessToken = data.tokens.access_token;
  return accessToken;
}

// Refresh tokens rotate one-shot: the server revokes the presented token the
// instant it issues a new one, and reuse of a revoked token kills every
// session for the user. With the access token now living only in tab memory,
// every tab hits this on load — open two tabs at once and both would race to
// present the SAME cookie-borne token before either's response updates it,
// tripping that theft detector on ordinary use, not an attack.
//
// navigator.locks serializes the actual network call across every tab of
// this origin (it is a browser-wide lock manager, not a per-tab one): only
// one tab is ever mid-refresh at a time, so by the time a waiting tab's
// fetch goes out, the cookie the browser attaches is already the winner's
// rotated one, not the stale value another tab is mid-rotating. Browsers
// without the Web Locks API (very old Safari/Firefox) fall back to an
// unserialized call — rarer race, not a security hole, since the theft
// detector still just costs the pair of tabs a re-login rather than silently
// misbehaving.
async function refreshAccessToken(): Promise<string> {
  if (typeof navigator !== "undefined" && navigator.locks) {
    return navigator.locks.request("mesh-auth-refresh", refreshOnce);
  }
  return refreshOnce();
}

// Called once at app startup (see useAuthStore.initialize) to silently trade
// the httpOnly refresh cookie for an access token, before the app knows
// whether the visitor is logged in at all. A missing/expired/absent cookie
// throws the same UNAUTHORIZED as any other failed refresh — the caller
// treats that as "not logged in", not as an error to surface.
export async function bootstrapSession(): Promise<string> {
  return refreshAccessToken();
}

export class ApiRequestError extends Error {
  code: string;
  status: number;
  details?: Record<string, string>;

  constructor(
    message: string,
    code: string,
    status: number,
    details?: Record<string, string>,
  ) {
    super(message);
    this.name = "ApiRequestError";
    this.code = code;
    this.status = status;
    this.details = details;
  }
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  params?: Record<string, string | number | undefined>;
  noAuth?: boolean;
  // Send/receive cookies on an otherwise-noAuth request — needed by the
  // endpoints that set or must present the httpOnly refresh cookie (login,
  // register, invite-accept) while still not sending a Bearer token.
  withCredentials?: boolean;
}

// api() always owns JSON-encoding the body. A caller that pre-stringifies (as
// notification.ts once did) would otherwise get double-encoded: this guards
// against that recurring, since a pre-stringified body silently becomes a
// JSON string of a JSON string that the server can't bind into a struct.
//
// FormData is the one exception, and it is the reason file uploads used to be
// written as a raw fetch alongside this helper instead of through it: a
// multipart body must reach fetch untouched, and its Content-Type must be left
// for the browser to generate — it carries the boundary, which only the browser
// knows. Handling that here rather than in each uploader is what lets an upload
// inherit the 401 refresh-and-replay below. A raw fetch cannot, which made an
// upload the one action in the app that a lapsed 15-minute access token killed
// outright while everything else silently recovered.
function serializeBody(body: unknown): BodyInit | undefined {
  if (body === undefined) return undefined;
  if (body instanceof FormData) return body;
  return typeof body === "string" ? body : JSON.stringify(body);
}

export async function api<T>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { method = "GET", body, params, noAuth = false, withCredentials = false } = options;

  let url = `${BASE_URL}${path}`;
  if (params) {
    const searchParams = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined) {
        searchParams.set(key, String(value));
      }
    }
    const qs = searchParams.toString();
    if (qs) url += `?${qs}`;
  }

  const headers: Record<string, string> = {};
  if (!(body instanceof FormData)) {
    headers["Content-Type"] = "application/json";
  }

  if (!noAuth && accessToken) {
    headers["Authorization"] = `Bearer ${accessToken}`;
  }

  // Plain noAuth requests (e.g. GET /auth/config, GET /invites/:token) must
  // not send session cookies — stale cookies from a previous instance can
  // cause the server to reject the request with a non-JSON error body (e.g.
  // 431 Too Large), which then surfaces as "An unexpected error occurred"
  // instead of a clean login form. withCredentials opts a specific noAuth
  // request back in when it needs to set/read the refresh cookie.
  const credentials: RequestCredentials = withCredentials
    ? "include"
    : noAuth
      ? "omit"
      : "same-origin";

  let res = await fetch(url, {
    method,
    headers,
    body: serializeBody(body),
    credentials,
  });

  // Auto-refresh on 401
  if (res.status === 401 && !noAuth) {
    if (!refreshPromise) {
      refreshPromise = refreshAccessToken().finally(() => {
        refreshPromise = null;
      });
    }

    try {
      const newToken = await refreshPromise;
      headers["Authorization"] = `Bearer ${newToken}`;
      res = await fetch(url, {
        method,
        headers,
        body: serializeBody(body),
        credentials: "same-origin",
      });
    } catch {
      accessToken = null;
      window.location.href = "/login";
      throw new ApiRequestError("Session expired", "UNAUTHORIZED", 401);
    }
  }

  if (res.status === 204) {
    return undefined as T;
  }

  // Guard against non-JSON error bodies (e.g. Caddy/Go 431, plain-text 500).
  // Without this, res.json() throws a SyntaxError which is not an
  // ApiRequestError and surfaces as "An unexpected error occurred".
  let data: unknown;
  try {
    data = await res.json();
  } catch {
    throw new ApiRequestError(
      res.status === 431
        ? "Your browser is sending a large saved session that was rejected. Please clear this site's cookies and try again."
        : `Server error (${res.status})`,
      "SERVER_ERROR",
      res.status,
    );
  }

  if (!res.ok) {
    const err = data as ApiError;
    throw new ApiRequestError(
      err.message || err.error || "Request failed",
      String(err.code || "UNKNOWN"),
      res.status,
      err.validation || err.details,
    );
  }

  return data as T;
}

export async function getMentionables(
  workspaceId: string,
  query: string,
): Promise<import("@/types").Mentionable[]> {
  return api<import("@/types").Mentionable[]>(
    `/api/v1/workspaces/${workspaceId}/mentionables`,
    { params: { q: query, limit: 20 } },
  );
}

export interface TaskCostSummary {
  total_cost: number;
  tokens_in: number;
  tokens_out: number;
  session_count: number;
  rework_count: number;
  quality_flag: "golden" | "rework" | "multi-turn" | "unknown";
}

export async function getTaskCostSummary(
  taskId: string,
): Promise<TaskCostSummary> {
  return api<TaskCostSummary>(`/api/v1/tasks/${taskId}/cost-summary`);
}
