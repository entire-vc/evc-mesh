import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";

// Regression test for the double-JSON-serialization bug (Mesh task 501608d1):
// api() always JSON.stringify()s `body` itself. A caller that pre-stringifies
// (as web/src/stores/notification.ts once did) got a JSON-encoded string of a
// JSON string, which the Go handler's c.Bind() cannot parse — the request came
// back 400 invalid request body, and the failure was swallowed silently by the
// caller's catch block.

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ ok: true })));
});
afterEach(() => vi.unstubAllGlobals());

describe("api() body encoding", () => {
  it("JSON-encodes a raw object body exactly once", async () => {
    await api("/api/v1/notifications/preferences", {
      method: "PUT",
      body: { workspace_id: "ws1", events: ["task.assigned"] },
    });

    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    const init = fetchMock.mock.calls[0]?.[1];
    expect(typeof init.body).toBe("string");
    // Decodes to the object directly — not to a string that itself needs a
    // second JSON.parse to reach the object.
    expect(JSON.parse(init.body)).toEqual({
      workspace_id: "ws1",
      events: ["task.assigned"],
    });
  });

  it("does not double-encode a body a caller mistakenly pre-stringified", async () => {
    const preStringified = JSON.stringify({ ids: ["a", "b"] });

    await api("/api/v1/notifications/mark-read", {
      method: "POST",
      body: preStringified,
    });

    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    const init = fetchMock.mock.calls[0]?.[1];
    // Sent as-is (single-encoded), not re-wrapped in another layer of quotes.
    expect(init.body).toBe(preStringified);
    expect(JSON.parse(init.body)).toEqual({ ids: ["a", "b"] });
  });
});
