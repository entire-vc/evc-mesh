import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Mesh access tokens live 15 minutes (internal/auth/service.go) in tab memory
// only, and are refreshed reactively — the app notices they lapsed by getting a
// 401 back. api() handles that: it refreshes and replays the request, which is
// why every other action on a task page keeps working across a token boundary.
//
// The task-attachment upload used a raw fetch of its own (it needs the browser
// to set the multipart Content-Type, which api() used to overwrite with
// application/json), and in doing so it lost the refresh-and-replay. That made
// attaching a file the one action on a task that a lapsed token kills outright.
// The same defect was fixed for document attachments in PR #620; these tests
// pin the equivalent recovery for tasks.
//
// Written against the network rather than against an internal helper, so they
// keep meaning if the plumbing moves.

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const artifact = {
  id: "art-1",
  task_id: "task-1",
  name: "pic.png",
  artifact_type: "image",
  mime_type: "image/png",
  size_bytes: 3,
  created_at: "2026-08-20T00:00:00Z",
};

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("uploadArtifact", () => {
  it("refreshes the session and replays the upload when the access token has lapsed", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/auth/refresh")) {
        return jsonResponse({ tokens: { access_token: "fresh-token" } });
      }
      // The first upload carries the stale token the tab still holds.
      if (fetchMock.mock.calls.filter((c) => String(c[0]).includes("/artifacts")).length === 1) {
        return jsonResponse({ code: 401, message: "Authentication required" }, 401);
      }
      return jsonResponse(artifact, 201);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { setAccessToken } = await import("@/lib/api");
    setAccessToken("stale-token");
    const { uploadArtifact } = await import("@/lib/task-artifacts");

    const result = await uploadArtifact("task-1", new File(["abc"], "pic.png", { type: "image/png" }));

    expect(result.id).toBe("art-1");

    const uploads = fetchMock.mock.calls.filter((c) => String(c[0]).includes("/artifacts"));
    expect(uploads).toHaveLength(2);
    // The replay must carry the refreshed token, not the one that just 401'd.
    const replayHeaders = (uploads[1]?.[1] as RequestInit).headers as Record<string, string>;
    expect(replayHeaders["Authorization"]).toBe("Bearer fresh-token");
  });

  it("still sends the file as multipart, letting the browser set the boundary", async () => {
    // The reason this upload did not simply call api(): a JSON Content-Type
    // here produces a body the server cannot bind. Guarding it so the move to
    // api() cannot quietly reintroduce that.
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      jsonResponse(artifact, 201),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { setAccessToken } = await import("@/lib/api");
    setAccessToken("tok");
    const { uploadArtifact } = await import("@/lib/task-artifacts");

    await uploadArtifact("task-1", new File(["abc"], "pic.png", { type: "image/png" }));

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.body).toBeInstanceOf(FormData);
    expect((init.body as FormData).get("file")).toBeInstanceOf(File);
    // artifact_type is task-specific and must survive the move to api().
    expect((init.body as FormData).get("artifact_type")).toBe("image");
    const headers = init.headers as Record<string, string>;
    const contentType = Object.keys(headers).find((k) => k.toLowerCase() === "content-type");
    expect(contentType).toBeUndefined();
  });

  it("reports the server's message when the upload is refused for good", async () => {
    // Negative control for the retry above: a real permission failure must
    // still surface, not be masked by the refresh path.
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
    const { uploadArtifact } = await import("@/lib/task-artifacts");

    await expect(
      uploadArtifact("task-1", new File(["abc"], "pic.png", { type: "image/png" })),
    ).rejects.toThrow(/insufficient permissions/);
  });
});
