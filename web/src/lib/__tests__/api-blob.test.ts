import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// apiBlob is the export endpoint's own fetch path — the first binary response
// this app streams directly from its own backend rather than a presigned S3
// URL (see the function's own comment in api.ts for why api<T>() couldn't be
// reused). Same test shape as api-auth.test.ts: fresh module per test via
// vi.resetModules(), fetch stubbed globally.

beforeEach(() => {
  vi.resetModules();
});
afterEach(() => {
  vi.unstubAllGlobals();
});

function blobResponse(
  bytes: string,
  { status = 200, filename }: { status?: number; filename?: string } = {},
): Response {
  const headers: Record<string, string> = {};
  if (filename) {
    headers["Content-Disposition"] = `attachment; filename="${filename}"`;
  }
  return new Response(bytes, { status, headers });
}

function jsonErrorResponse(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("apiBlob", () => {
  it("sends the current access token as a Bearer header", async () => {
    const fetchMock = vi.fn().mockResolvedValue(blobResponse("pdf-bytes"));
    vi.stubGlobal("fetch", fetchMock);

    const { setAccessToken, apiBlob } = await import("@/lib/api");
    setAccessToken("my-token");

    await apiBlob("/api/v1/documents/d1/export", { format: "pdf", scope: "self" });

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer my-token");
    expect(init.credentials).toBe("same-origin");
  });

  it("encodes params onto the URL as a query string", async () => {
    const fetchMock = vi.fn().mockResolvedValue(blobResponse("bytes"));
    vi.stubGlobal("fetch", fetchMock);

    const { apiBlob } = await import("@/lib/api");
    await apiBlob("/api/v1/documents/d1/export", { format: "docx", scope: "tree" });

    const [url] = fetchMock.mock.calls[0] as [string];
    expect(url).toContain("/api/v1/documents/d1/export?");
    expect(url).toContain("format=docx");
    expect(url).toContain("scope=tree");
  });

  it("parses the filename from Content-Disposition, and returns the body as a Blob", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      blobResponse("fake-pdf-bytes", { filename: "guide-2026-09-02.pdf" }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { apiBlob } = await import("@/lib/api");
    const { blob, filename } = await apiBlob("/api/v1/documents/d1/export", {
      format: "pdf",
      scope: "self",
    });

    expect(filename).toBe("guide-2026-09-02.pdf");
    expect(await blob.text()).toBe("fake-pdf-bytes");
  });

  it("falls back to a generic filename when Content-Disposition is missing", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(blobResponse("bytes")));

    const { apiBlob } = await import("@/lib/api");
    const { filename } = await apiBlob("/api/v1/documents/d1/export", {
      format: "pdf",
      scope: "self",
    });

    expect(filename).toBe("download");
  });

  it("on a non-401 error, throws ApiRequestError parsed from the JSON error body", async () => {
    // A fresh Response per call — a mocked Response body can only be read
    // once, and reusing the same instance across two apiBlob calls would make
    // the second call's res.json() fail on an already-consumed stream, which
    // silently produces the generic SERVER_ERROR fallback instead of
    // exercising the real parse path this test means to check.
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() =>
        Promise.resolve(
          jsonErrorResponse(
            { error: "export tree exceeds the documents limit", code: "export_too_large" },
            400,
          ),
        ),
      ),
    );

    const { apiBlob, ApiRequestError } = await import("@/lib/api");

    try {
      await apiBlob("/api/v1/documents/d1/export", { format: "pdf", scope: "tree" });
      throw new Error("expected apiBlob to throw");
    } catch (err) {
      expect(err).toBeInstanceOf(ApiRequestError);
      expect((err as InstanceType<typeof ApiRequestError>).code).toBe("export_too_large");
    }
  });

  it("on 401, retries once with a freshly refreshed token and succeeds", async () => {
    vi.stubGlobal("navigator", {});
    const fetchMock = vi
      .fn()
      // First call: the export request itself, unauthenticated/expired token.
      .mockResolvedValueOnce(blobResponse("", { status: 401 }))
      // Second call: the refresh endpoint.
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ tokens: { access_token: "fresh-token", expires_in: 900 } }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      // Third call: the export request retried with the new token.
      .mockResolvedValueOnce(blobResponse("real-bytes", { filename: "guide.pdf" }));
    vi.stubGlobal("fetch", fetchMock);

    const { apiBlob } = await import("@/lib/api");
    const { blob, filename } = await apiBlob("/api/v1/documents/d1/export", {
      format: "pdf",
      scope: "self",
    });

    expect(filename).toBe("guide.pdf");
    expect(await blob.text()).toBe("real-bytes");
    expect(fetchMock).toHaveBeenCalledTimes(3);
    const retryInit = fetchMock.mock.calls[2]?.[1] as RequestInit;
    expect((retryInit.headers as Record<string, string>).Authorization).toBe("Bearer fresh-token");
  });
});
