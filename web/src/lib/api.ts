import type { ApiError, RefreshResponse } from "@/types";

const BASE_URL = import.meta.env.VITE_API_URL || "";

let accessToken: string | null = null;
let refreshToken: string | null = null;
let refreshPromise: Promise<string> | null = null;

export function setTokens(access: string, refresh: string) {
  accessToken = access;
  refreshToken = refresh;
  localStorage.setItem("access_token", access);
  localStorage.setItem("refresh_token", refresh);
}

export function loadTokens() {
  accessToken = localStorage.getItem("access_token");
  refreshToken = localStorage.getItem("refresh_token");
}

export function clearTokens() {
  accessToken = null;
  refreshToken = null;
  localStorage.removeItem("access_token");
  localStorage.removeItem("refresh_token");
}

// Best-effort clear of any non-httpOnly session cookies left by a previous
// Mesh/Casdoor instance. Cannot touch httpOnly cookies — those expire naturally.
export function clearSessionCookies() {
  const cookiesToClear = [
    "access_token",
    "refresh_token",
    "mesh_session",
    "casdoor_session",
    "session",
  ];
  for (const name of cookiesToClear) {
    document.cookie = `${name}=; max-age=0; path=/; SameSite=Lax`;
    document.cookie = `${name}=; max-age=0; path=/; domain=${location.hostname}; SameSite=Lax`;
  }
}

export function getAccessToken(): string | null {
  return accessToken;
}

async function refreshAccessToken(): Promise<string> {
  if (!refreshToken) {
    throw new ApiRequestError("No refresh token", "UNAUTHORIZED", 401);
  }

  const res = await fetch(`${BASE_URL}/api/v1/auth/refresh`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ refresh_token: refreshToken }),
  });

  if (!res.ok) {
    clearTokens();
    throw new ApiRequestError("Session expired", "UNAUTHORIZED", 401);
  }

  const data = (await res.json()) as RefreshResponse;
  setTokens(data.tokens.access_token, data.tokens.refresh_token);
  return data.tokens.access_token;
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
}

// api() always owns JSON-encoding the body. A caller that pre-stringifies (as
// notification.ts once did) would otherwise get double-encoded: this guards
// against that recurring, since a pre-stringified body silently becomes a
// JSON string of a JSON string that the server can't bind into a struct.
function serializeBody(body: unknown): string | undefined {
  if (body === undefined) return undefined;
  return typeof body === "string" ? body : JSON.stringify(body);
}

export async function api<T>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { method = "GET", body, params, noAuth = false } = options;

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

  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };

  if (!noAuth && accessToken) {
    headers["Authorization"] = `Bearer ${accessToken}`;
  }

  // noAuth requests (login, register) must not send session cookies — stale
  // cookies from a previous instance can cause the server to reject the request
  // with a non-JSON error body (e.g. 431 Too Large), which then surfaces as
  // "An unexpected error occurred" instead of a clean login form.
  let res = await fetch(url, {
    method,
    headers,
    body: serializeBody(body),
    credentials: noAuth ? "omit" : "same-origin",
  });

  // Auto-refresh on 401
  if (res.status === 401 && !noAuth && refreshToken) {
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
      clearTokens();
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
