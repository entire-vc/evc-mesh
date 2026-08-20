import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Images pasted into the description before a task exists are uploaded right
// after creation, against the same /api/v1/tasks/:id/artifacts endpoint that
// markdown-editor uses. This path had its own copy of the raw fetch, so it had
// the same 15-minute defect — and it swallowed harder than the others: the
// success branch was `if (res.ok)` with no else, and the catch was empty. A
// refused upload therefore left `![name](pending:name)` in the description and
// SAVED it, so the user ended up with a broken image link in their new task and
// no indication anything had gone wrong.

vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn() }),
}));

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const artifact = {
  id: "art-1",
  task_id: "task-1",
  name: "shot.png",
  artifact_type: "image",
  mime_type: "image/png",
  size_bytes: 3,
  created_at: "2026-08-20T00:00:00Z",
};

function pending(name = "shot.png") {
  return {
    file: new File(["abc"], name, { type: "image/png" }),
    placeholder: `![${name}](pending:${name})`,
  };
}

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("uploadPendingImages", () => {
  it("refreshes the session and replays when the access token has lapsed", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/auth/refresh")) {
        return jsonResponse({ tokens: { access_token: "fresh-token" } });
      }
      if (fetchMock.mock.calls.filter((c) => String(c[0]).includes("/artifacts")).length === 1) {
        return jsonResponse({ code: 401, message: "Authentication required" }, 401);
      }
      return jsonResponse(artifact, 201);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { setAccessToken } = await import("@/lib/api");
    setAccessToken("stale-token");
    const { uploadPendingImages } = await import("@/lib/pending-images");

    const p = pending();
    const out = await uploadPendingImages("task-1", [p], `before ${p.placeholder} after`);

    // The placeholder must have become a real link, which only happens if the
    // upload recovered from the 401.
    expect(out).toContain("/api/v1/artifacts/art-1/download");
    expect(out).not.toContain("pending:");
  });

  it("tells the user and drops the placeholder when an image is refused", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) =>
        String(input).includes("/auth/refresh")
          ? jsonResponse({ tokens: { access_token: "fresh" } })
          : jsonResponse({ code: 403, message: "insufficient permissions" }, 403),
      ),
    );

    const { setAccessToken } = await import("@/lib/api");
    setAccessToken("tok");
    const { uploadPendingImages } = await import("@/lib/pending-images");
    const { toast } = await import("@/components/ui/toast");

    const p = pending();
    const out = await uploadPendingImages("task-1", [p], `before ${p.placeholder} after`);

    expect(vi.mocked(toast.error)).toHaveBeenCalled();
    expect(String(vi.mocked(toast.error).mock.calls[0]?.[0])).toMatch(/shot\.png/);
    // A `pending:` URL is not a real link — saving it would persist a broken
    // image into the task description.
    expect(out).not.toContain("pending:");
  });
});
