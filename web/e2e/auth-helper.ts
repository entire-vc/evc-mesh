import type { APIRequestContext } from "@playwright/test";

export interface LoginResult {
  accessToken: string;
}

/**
 * Logs in through the real /api/v1/auth/login endpoint, retrying ONE extra
 * time on HTTP 429 and honouring the server's own Retry-After header rather
 * than a fixed guess.
 *
 * Why this exists (`#603f340a`, 2026-08-20): /auth/login is capped by
 * AuthRPM (5/min per key — internal/middleware/ratelimit.go). This suite
 * only ever makes ONE legitimate login call per spec file, comfortably under
 * budget on its own — but the same endpoint is shared with everything else
 * that authenticates against prod (real users, the agent fleet, other CI
 * runs), and a single point-in-time assertion cannot tell "these credentials
 * are wrong" apart from "the shared budget was momentarily spent by someone
 * else". A naive fixed-delay retry would guess wrong in either direction
 * (too short: hits the SAME window again; too long: wastes CI minutes) —
 * the server already tells us exactly how long via Retry-After, so honour
 * that instead of guessing.
 *
 * This is defense-in-depth, not the primary fix: the actual root cause
 * (`#603f340a`) was that /auth/login and /auth/refresh silently shared one
 * Redis counter, so routine fleet refresh traffic alone could exhaust
 * login's 5 RPM budget — fixed at the source by namespacing rate-limit keys
 * per route (RateLimitConfig.Name, internal/middleware/ratelimit*.go). A
 * second, separately-tracked infra-level cause (all external traffic
 * resolving to one apparent IP at the reverse-proxy hop) is NOT fixed by
 * this PR — see the task thread. Retrying here does not hide a real
 * credentials/account problem: a 401/403/5xx is NOT retried, it fails
 * immediately with the real status.
 */
export async function loginWithRetry(
  request: APIRequestContext,
  email: string,
  password: string,
  opts: { maxAttempts?: number } = {}
): Promise<LoginResult> {
  const maxAttempts = opts.maxAttempts ?? 2;
  let lastStatus = -1;
  let lastBody = "";

  for (let attempt = 1; attempt <= maxAttempts; attempt++) {
    const res = await request.post("/api/v1/auth/login", {
      data: { email, password },
    });
    lastStatus = res.status();

    if (lastStatus === 200) {
      const body = (await res.json()) as {
        tokens?: { access_token?: string };
      };
      if (!body.tokens?.access_token) {
        throw new Error(
          `login as ${email} returned HTTP 200 but no access_token in the ` +
            `body — the response shape changed, this is not a credentials or ` +
            `rate-limit problem.`
        );
      }
      return { accessToken: body.tokens.access_token };
    }

    if (lastStatus === 429) {
      if (attempt === maxAttempts) {
        break; // exhausted retries — fall through to the 429-specific error below
      }
      // The server always sets Retry-After on 429 (tooManyRequestsJSON,
      // internal/middleware/ratelimit.go) — trust it over a guess. Cap
      // defensively so a malformed/huge header can't stall the job.
      const header = res.headers()["retry-after"];
      const parsed = header ? parseInt(header, 10) : NaN;
      const retryAfterSecs = Number.isFinite(parsed)
        ? Math.min(Math.max(parsed, 1), 65)
        : 60;
      await new Promise((resolve) => setTimeout(resolve, retryAfterSecs * 1000));
      continue;
    }

    // Not a rate limit — waiting will not fix a 401/403/5xx. Fail now with
    // the real status instead of retrying into the same wrong answer.
    lastBody = await res.text().catch(() => "<unreadable body>");
    break;
  }

  if (lastStatus === 429) {
    throw new Error(
      `login as ${email} was rate-limited (HTTP 429) after ${maxAttempts} ` +
        `attempt(s), honouring the server's Retry-After each time — this is ` +
        `AuthRPM contention on a shared bucket (internal/middleware/ratelimit.go), ` +
        `NOT wrong credentials or a disabled account. See task #603f340a.`
    );
  }
  throw new Error(
    `login as ${email} must succeed — E2E credentials are wrong or the user ` +
      `is disabled (HTTP ${lastStatus}: ${lastBody})`
  );
}
