import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Mesh access tokens live 15 minutes (internal/auth/service.go) in tab memory
// only, and are refreshed reactively — the app notices they lapsed by getting a
// 401 back. api() handles that: it refreshes and replays the request, which is
// why every other action in the page keeps working across a token boundary.
//
// The document-attachment upload used a raw fetch of its own (it needs the
// browser to set the multipart Content-Type, which api() would have overwritten
// with application/json), and in doing so it lost the refresh-and-retry. That
// made an upload the one action in the app that a lapsed token kills outright —
// and the editor swallowed the resulting error, so the user saw nothing at all.
//
// These tests pin the recovery. They are written against the network, not
// against an internal helper, so they keep meaning if the plumbing moves.

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const attachment = {
  id: "att-1",
  document_id: "doc-1",
  name: "pic.png",
  mime_type: "image/png",
  size_bytes: 3,
  storage_key: "documents/p/d/attachments/att-1.png",
  uploaded_by: "u-1",
  uploaded_by_type: "user" as const,
  created_at: "2026-08-19T00:00:00Z",
};

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("uploadDocumentAttachment", () => {
  it("refreshes the session and replays the upload when the access token has lapsed", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/auth/refresh")) {
        return jsonResponse({ tokens: { access_token: "fresh-token" } });
      }
      // The first upload carries the stale token the tab still holds.
      if (fetchMock.mock.calls.filter((c) => String(c[0]).includes("/attachments")).length === 1) {
        return jsonResponse({ code: 401, message: "Authentication required" }, 401);
      }
      return jsonResponse(attachment, 201);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { setAccessToken } = await import("@/lib/api");
    setAccessToken("stale-token");
    const { uploadDocumentAttachment } = await import("@/lib/document-attachments");

    const file = new File(["abc"], "pic.png", { type: "image/png" });
    const result = await uploadDocumentAttachment("doc-1", file);

    expect(result.id).toBe("att-1");

    const uploads = fetchMock.mock.calls.filter((c) => String(c[0]).includes("/attachments"));
    expect(uploads).toHaveLength(2);
    // The replay must carry the refreshed token, not the one that just 401'd.
    const replayHeaders = (uploads[1]?.[1] as RequestInit).headers as Record<string, string>;
    expect(replayHeaders["Authorization"]).toBe("Bearer fresh-token");
  });

  it("still sends the file as multipart, letting the browser set the boundary", async () => {
    // The reason this upload does not simply call api(): a JSON Content-Type
    // here produces a body the server cannot bind. Guarding it so a later
    // refactor toward api() cannot quietly reintroduce that.
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      jsonResponse(attachment, 201),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { setAccessToken } = await import("@/lib/api");
    setAccessToken("tok");
    const { uploadDocumentAttachment } = await import("@/lib/document-attachments");

    await uploadDocumentAttachment("doc-1", new File(["abc"], "pic.png", { type: "image/png" }));

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.body).toBeInstanceOf(FormData);
    expect((init.body as FormData).get("file")).toBeInstanceOf(File);
    const headers = init.headers as Record<string, string>;
    const contentType = Object.keys(headers).find((k) => k.toLowerCase() === "content-type");
    expect(contentType).toBeUndefined();
  });

  it("reports the server's message when the upload is refused for good", async () => {
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
    const { uploadDocumentAttachment } = await import("@/lib/document-attachments");

    await expect(
      uploadDocumentAttachment("doc-1", new File(["abc"], "pic.png", { type: "image/png" })),
    ).rejects.toThrow(/insufficient permissions/);
  });
});
