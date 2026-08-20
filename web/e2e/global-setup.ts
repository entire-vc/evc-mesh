import type { FullConfig } from "@playwright/test";

/**
 * Global setup: fail loudly when the suite cannot possibly prove anything.
 *
 * "Not configured" must not be indistinguishable from "checked and fine" on a
 * REQUIRED status check. Until 2026-08-20 this file wrote an empty
 * storageState and the spec skipped itself, so the job exited 0 in one second
 * having asserted nothing — for every PR, since the check was made required.
 *
 * No browser work happens here on purpose: /api/v1/auth/login is rate-limited
 * to 5 requests/minute per IP (cmd/api/main.go — loginGroup), so the suite
 * spends exactly one login per run, inside the shared context in the spec.
 */
export default async function globalSetup(_config: FullConfig) {
  const missing = ["E2E_USER_EMAIL", "E2E_USER_PASSWORD"].filter(
    (name) => !process.env[name]
  );
  if (missing.length > 0) {
    throw new Error(
      `[global-setup] missing ${missing.join(" and ")}. The authed E2E suite ` +
        `cannot obtain a session, and a green check on skipped tests is worse ` +
        `than a red one. Set them as repository secrets (see the 'Authed E2E' ` +
        `job in .github/workflows/ci.yml).`
    );
  }
}
