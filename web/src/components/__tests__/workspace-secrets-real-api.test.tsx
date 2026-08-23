import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

// Regression test for task #73ee74fc: every api() call in workspace-secrets.tsx
// was missing the required `/api/v1` prefix (`/workspaces/:id/secrets` instead
// of `/api/v1/workspaces/:id/secrets`). That shipped to prod and passed 732/732
// frontend tests, two independent verifier SHIP verdicts, and a "grep the live
// bundle for approved strings" check — because workspace-secrets.test.tsx mocks
// `@/lib/api` entirely (`vi.mock("@/lib/api", ...)`), so no test in that file
// ever exercises the real path-prefixing logic in api.ts. A wrong path string
// and the code that reads it can drift together and still look green forever.
//
// This file does NOT mock `@/lib/api` — it renders the real component against
// the real `api()`/`fetch` call and only stubs `global.fetch`, the way
// web/src/lib/__tests__/api-body.test.ts already does for the same class of
// "does api() build the request I expect" question. That is the one layer the
// component-level mock cannot see through.
import { WorkspaceSecrets } from "@/components/workspace-secrets";
import type { Secret } from "@/types";

vi.mock("@/components/ui/toast", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const WS = "11111111-1111-1111-1111-111111111111";

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse([] as Secret[])));
});
afterEach(() => vi.unstubAllGlobals());

describe("WorkspaceSecrets — real api()/fetch path", () => {
  it("lists secrets from a URL starting with /api/v1, not the SPA fallback route", async () => {
    render(<WorkspaceSecrets workspaceId={WS} canManage />);
    await screen.findByTestId("secret-empty");

    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    const requestedUrl = String(fetchMock.mock.calls[0]?.[0]);

    // The exact failure this guards: a bare `/workspaces/:id/secrets` request
    // (no /api/v1) does not 404 — Caddy/the SPA router serves index.html for
    // it with a 200, and api.ts's res.json() then throws on the HTML body.
    // Asserting the request never left with that shape is the only way to
    // catch it before a live browser does.
    expect(requestedUrl).toContain(`/api/v1/workspaces/${WS}/secrets`);
    expect(requestedUrl).not.toMatch(/^\/workspaces\//);
  });
});
