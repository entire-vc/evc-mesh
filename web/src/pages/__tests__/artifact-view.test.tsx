import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));
vi.mock("@/stores/auth", () => ({
  useAuthStore: vi.fn(() => ({ isAuthenticated: true })),
}));

import { api } from "@/lib/api";
import { ArtifactViewPage } from "@/pages/artifact-view";
import type { Artifact } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

function artifact(over: Partial<Artifact> = {}): Artifact {
  return {
    id: "a1",
    task_id: "t1",
    name: "report.json",
    artifact_type: "data",
    mime_type: "application/json",
    storage_key: "k",
    storage_url: "",
    size_bytes: 128,
    checksum_sha256: "",
    metadata: {},
    uploaded_by: "u1",
    uploaded_by_type: "user",
    created_at: "2026-10-05T00:00:00Z",
    ...over,
  };
}

/** Stand in for the S3 fetch. Returns whatever `body` says, with `ok` derived. */
function stubStorage(body: string, status = 200) {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    text: () => Promise.resolve(body),
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function stubMeta(a: Artifact) {
  mockedApi.mockImplementation((path: string) => {
    if (path === "/api/v1/artifacts/a1") return Promise.resolve(a);
    if (path === "/api/v1/artifacts/a1/download?disposition=inline")
      return Promise.resolve({ url: "https://mesh.entire.host/s3/x?X-Amz-Expires=3600" });
    return Promise.reject(new Error(`unexpected path: ${path}`));
  });
}

function show() {
  return render(
    <MemoryRouter initialEntries={["/a/a1"]}>
      <Routes>
        <Route path="/a/:artifactId" element={<ArtifactViewPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockedApi.mockReset();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("ArtifactViewPage — text kinds", () => {
  /**
   * AC2 of #eb6fde4e: `json отформатирован`. The assertion is on STRUCTURE:
   * the stored file is one line, the page must show it indented across
   * lines. A raw dump of the body would contain every character this test
   * could match on and still be exactly the defect.
   */
  it("pretty-prints JSON instead of dumping the stored line", async () => {
    stubMeta(artifact());
    stubStorage('{"model":"x","cost":1.5,"ok":true,"extra":null}');
    const { container } = show();

    await waitFor(() => {
      expect(container.querySelector("pre")).not.toBeNull();
    });
    const pre = container.querySelector("pre")!;
    // Indented multi-line: every top-level key starts its own line.
    expect(pre.textContent).toContain('\n  "model"');
    expect(pre.textContent).not.toBe('{"model":"x","cost":1.5,"ok":true,"extra":null}');
    // Syntax colouring is present — at least the key class fired.
    expect(pre.querySelector("span.text-sky-700, span[class*='text-sky']")).not.toBeNull();
  });

  it("falls back to plain text when the JSON does not parse", async () => {
    stubMeta(artifact());
    stubStorage('{"broken": ');
    const { container } = show();

    await waitFor(() => {
      expect(container.querySelector("pre")).not.toBeNull();
    });
    expect(container.querySelector("pre")?.textContent).toContain('{"broken": ');
  });

  /** AC2 of #eb6fde4e: `csv таблицей`, including quoted commas. */
  it("renders CSV as a table and honours quoted commas", async () => {
    stubMeta(artifact({ name: "costs.csv", mime_type: "text/csv" }));
    stubStorage('item,cost\n"laptop, used",100\nmouse,5');
    const { container } = show();

    await waitFor(() => {
      expect(container.querySelector("table")).not.toBeNull();
    });
    expect(container.querySelector("thead th")?.textContent).toBe("item");
    const cells = Array.from(container.querySelectorAll("tbody td"));
    expect(cells.map((c) => c.textContent)).toEqual(["laptop, used", "100", "mouse", "5"]);
  });

  /** AC1: md renders through the same pipeline the preview modal used. */
  it("renders markdown as structure, not source", async () => {
    stubMeta(artifact({ name: "audit.md", mime_type: "text/markdown" }));
    stubStorage("# Findings\n\n| col | val |\n| --- | --- |\n| a | 1 |\n");
    const { container } = show();

    await waitFor(() => {
      // The page's own h1 is the file name; the content must add ANOTHER one.
      expect(container.querySelectorAll("h1").length).toBeGreaterThan(1);
    });
    expect(
      Array.from(container.querySelectorAll("h1")).map((h) => h.textContent),
    ).toContain("Findings");
    expect(container.querySelector("table")).not.toBeNull();
    expect(container.textContent).not.toContain("| --- |");
  });

  it("keeps raw HTML inside the file inert", async () => {
    stubMeta(artifact({ name: "audit.md", mime_type: "text/markdown" }));
    stubStorage('# Doc\n\n<img src=x onerror="alert(1)">\n<script>alert(2)</script>\n');
    const { container } = show();

    // Wait for the CONTENT h1 (the second one), not the page header — the
    // script/img assertions are only meaningful once markdown has rendered.
    await waitFor(() =>
      expect(container.querySelectorAll("h1").length).toBeGreaterThan(1),
    );
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
  });

  it("announces truncation rather than silently showing a prefix", async () => {
    stubMeta(artifact({ name: "big.csv", mime_type: "text/csv", size_bytes: 250_000 }));
    stubStorage("x".repeat(250_000));
    show();

    await waitFor(() => {
      expect(screen.getByText(/more characters are\s+not shown/i)).toBeInTheDocument();
    });
  });

  it("explains an expired link and retries with a fresh URL request", async () => {
    stubMeta(artifact());
    stubStorage("", 403);
    show();

    await waitFor(() => expect(screen.getByText("Could not load preview")).toBeInTheDocument());
    const callsBefore = mockedApi.mock.calls.filter((c) => String(c[0]).includes("download")).length;
    screen.getByRole("button", { name: /try again/i }).click();
    await waitFor(() =>
      expect(
        mockedApi.mock.calls.filter((c) => String(c[0]).includes("download")).length,
      ).toBeGreaterThan(callsBefore),
    );
  });
});

describe("ArtifactViewPage — browser-native kinds", () => {
  it("embeds an image through the presigned inline URL", async () => {
    stubMeta(artifact({ name: "proof.png", mime_type: "image/png", artifact_type: "image" }));
    show();

    const img = await waitFor(() => {
      const el = document.querySelector("img");
      expect(el).not.toBeNull();
      return el as HTMLImageElement;
    });
    expect(img.getAttribute("src")).toBe("https://mesh.entire.host/s3/x?X-Amz-Expires=3600");
    expect(img.getAttribute("alt")).toBe("proof.png");
  });

  it("frames a PDF inline rather than offering a download-only page", async () => {
    stubMeta(artifact({ name: "doc.pdf", mime_type: "application/pdf" }));
    const { container } = show();

    await waitFor(() => expect(container.querySelector("iframe")).not.toBeNull());
    expect(container.querySelector("iframe")?.getAttribute("src")).toContain("X-Amz-Expires");
  });
});

describe("ArtifactViewPage — states", () => {
  it("says the artifact is unavailable when the API refuses it", async () => {
    mockedApi.mockRejectedValue(
      Object.assign(new Error("forbidden"), { code: "FORBIDDEN", status: 403 }),
    );
    show();
    expect(await screen.findByText("Artifact not found")).toBeInTheDocument();
  });

  it("offers Download on a file no renderer handles", async () => {
    stubMeta(artifact({ name: "archive.zip", mime_type: "application/zip" }));
    show();
    expect(await screen.findByText(/be shown in the browser/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /download/i })).toBeInTheDocument();
  });

  it("links back to the owning task", async () => {
    stubMeta(artifact());
    show();
    expect(await screen.findByRole("link", { name: /open task/i })).toHaveAttribute(
      "href",
      "/t/t1",
    );
  });
});

describe("ArtifactViewPage — unauthenticated deep link", () => {
  it("redirects to login preserving the /a/ path", async () => {
    // The module-level mock factory returns a vi.fn, so one call can flip it.
    const { useAuthStore } = await import("@/stores/auth");
    vi.mocked(useAuthStore).mockReturnValue({ isAuthenticated: false } as never);

    let loginSearch = "";
    function LoginProbe() {
      loginSearch = useLocation().search;
      return <div data-testid="login-page" />;
    }
    render(
      <MemoryRouter initialEntries={["/a/a1"]}>
        <Routes>
          <Route path="/a/:artifactId" element={<ArtifactViewPage />} />
          <Route path="/login" element={<LoginProbe />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByTestId("login-page")).toBeInTheDocument();
    expect(loginSearch).toBe("?redirect=%2Fa%2Fa1");
    vi.mocked(useAuthStore).mockReturnValue({ isAuthenticated: true } as never);
  });
});
