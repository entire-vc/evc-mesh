import { test, type APIRequestContext, type APIResponse } from "@playwright/test";

/**
 * Every authed-e2e job signs in as the same CI account, and the API limits each actor to a per-minute budget
 * (MESH_RATE_LIMIT_API_RPM, internal/middleware/ratelimit.go). When jobs from several pipelines overlap they can spend
 * that budget together, and a setup or cleanup call gets HTTP 429 even though nothing is wrong with the change under
 * test. The server says on every 429 how long to wait (Retry-After: the seconds until its one-minute window resets),
 * so these helpers wait exactly that long and send the same request again, a bounded number of times. Any other
 * status, and the last 429, is returned unchanged: the spec's own assertion then fails with the real status.
 */

/** Attempts per request: the first send plus two waits for a fresh window. */
export const RATE_LIMIT_MAX_ATTEMPTS = 3;

/** Seconds to wait for a 429 with this Retry-After value: clamped to 1..65, 60 when missing or unreadable. */
export function retryAfterSeconds(header: string | undefined): number {
  const parsed = header ? parseInt(header, 10) : NaN;
  return Number.isFinite(parsed) ? Math.min(Math.max(parsed, 1), 65) : 60;
}

/**
 * Gives the running test or hook the time it is about to spend waiting, so honouring Retry-After never turns into a
 * timeout. Outside a test (global setup) there is no timeout to extend.
 */
function extendCurrentTimeout(ms: number): void {
  try {
    const info = test.info();
    if (info.timeout > 0) info.setTimeout(info.timeout + ms);
  } catch {
    // not inside a test or hook
  }
}

export async function sendWithRateLimitRetry(
  send: () => Promise<APIResponse>,
  label: string,
  maxAttempts = RATE_LIMIT_MAX_ATTEMPTS,
  sleep: (ms: number) => Promise<void> = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
): Promise<APIResponse> {
  for (let attempt = 1; ; attempt++) {
    const res = await send();
    if (res.status() !== 429 || attempt >= maxAttempts) return res;
    const secs = retryAfterSeconds(res.headers()["retry-after"]);
    console.log(`[rate-limit] ${label}: HTTP 429, waiting ${secs}s (Retry-After) before attempt ${attempt + 1}/${maxAttempts}`);
    await res.dispose();
    extendCurrentTimeout(secs * 1000 + 5_000);
    await sleep(secs * 1000);
  }
}

const SENDING_METHODS = new Set(["get", "post", "put", "patch", "delete", "head", "fetch"]);

/**
 * The same request context, with get/post/put/patch/delete/head/fetch retried on 429 as described above.
 * Use it for every API call a spec makes with the shared CI account: `api = rateLimitAware(context.request)`.
 */
export function rateLimitAware(request: APIRequestContext, maxAttempts = RATE_LIMIT_MAX_ATTEMPTS): APIRequestContext {
  return new Proxy(request, {
    get(target, prop) {
      const value: unknown = Reflect.get(target, prop, target);
      if (typeof value !== "function") return value;
      const method = value as (...args: unknown[]) => Promise<APIResponse>;
      if (typeof prop !== "string" || !SENDING_METHODS.has(prop)) return method.bind(target);
      return (urlOrRequest: unknown, options?: unknown) => {
        const url = typeof urlOrRequest === "string" ? urlOrRequest : "<request>";
        return sendWithRateLimitRetry(() => method.call(target, urlOrRequest, options), `${prop.toUpperCase()} ${url}`, maxAttempts);
      };
    },
  });
}
