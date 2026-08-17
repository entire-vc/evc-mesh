import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
vi.mock("@/components/ui/toast", () => ({
  toast: vi.fn(),
}));

import { api } from "@/lib/api";
import { toast } from "@/components/ui/toast";
import {
  artifactDownloadPath,
  isArtifactDownloadPath,
  renderArtifactAwareImage,
  renderArtifactAwareLink,
  resolveArtifactImages,
  handleArtifactLinkClick,
} from "@/lib/artifact-links";

describe("artifactDownloadPath / isArtifactDownloadPath", () => {
  it("builds a stable, inline-disposition path from an artifact id", () => {
    expect(artifactDownloadPath("artifact-1")).toBe(
      "/api/v1/artifacts/artifact-1/download?disposition=inline",
    );
  });

  it("recognizes its own generated path", () => {
    expect(isArtifactDownloadPath(artifactDownloadPath("6b1f9c2a-1111-4a2b-8c3d-000000000001"))).toBe(
      true,
    );
  });

  it("rejects unrelated URLs", () => {
    expect(isArtifactDownloadPath("https://evil.example.com/x")).toBe(false);
    expect(isArtifactDownloadPath("/api/v1/tasks/1/artifacts")).toBe(false);
    expect(isArtifactDownloadPath("javascript:alert(1)")).toBe(false);
  });
});

describe("renderArtifactAwareImage", () => {
  it("renders internal artifact links without a src, deferred to data-artifact-src", () => {
    const url = artifactDownloadPath("artifact-1");
    const html = renderArtifactAwareImage("alt text", url);
    expect(html).toContain(`data-artifact-src="${url}"`);
    expect(html).not.toMatch(/(?<!data-artifact-)src="/);
    expect(html).toContain('alt="alt text"');
  });

  it("renders external http(s) images with a direct src", () => {
    const html = renderArtifactAwareImage("cat", "https://example.com/cat.png");
    expect(html).toContain('src="https://example.com/cat.png"');
  });

  it("drops unsafe schemes (javascript:, data:) to plain alt text", () => {
    expect(renderArtifactAwareImage("x", "javascript:alert(1)")).toBe("x");
    expect(renderArtifactAwareImage("x", "data:text/html,<script>1</script>")).toBe("x");
  });
});

describe("renderArtifactAwareLink", () => {
  it("routes internal artifact links through data-artifact-download, not href", () => {
    const url = artifactDownloadPath("artifact-1");
    const html = renderArtifactAwareLink("my-file.pdf", url);
    expect(html).toContain(`data-artifact-download="${url}"`);
    expect(html).toContain('href="#"');
  });

  it("keeps external http(s) links as a normal href", () => {
    const html = renderArtifactAwareLink("docs", "https://example.com/docs");
    expect(html).toContain('href="https://example.com/docs"');
  });

  it("falls back unsafe schemes to '#'", () => {
    const html = renderArtifactAwareLink("x", "javascript:alert(1)");
    expect(html).toContain('href="#"');
    expect(html).not.toContain("javascript:");
  });
});

describe("resolveArtifactImages", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
  });

  it("fetches a fresh presigned URL for each unresolved artifact image and sets src", async () => {
    const path = artifactDownloadPath("artifact-1");
    vi.mocked(api).mockResolvedValue({ url: "https://s3.example.com/fresh" });

    const container = document.createElement("div");
    container.innerHTML = `<img data-artifact-src="${path}" alt="shot" />`;
    document.body.appendChild(container);

    await resolveArtifactImages(container);

    const img = container.querySelector("img")!;
    expect(img.src).toBe("https://s3.example.com/fresh");
    expect(img.hasAttribute("data-artifact-src")).toBe(false);
    expect(api).toHaveBeenCalledWith(path);
  });

  it("marks an image as failed instead of throwing when resolution fails", async () => {
    const path = artifactDownloadPath("artifact-1");
    vi.mocked(api).mockRejectedValue(new Error("boom"));

    const container = document.createElement("div");
    container.innerHTML = `<img data-artifact-src="${path}" alt="shot" />`;
    document.body.appendChild(container);

    await resolveArtifactImages(container);

    const img = container.querySelector("img")!;
    expect(img.alt).toContain("failed to load");
    expect(img.classList.contains("opacity-50")).toBe(true);
  });

  it("is a no-op for images that carry no data-artifact-src", async () => {
    const container = document.createElement("div");
    container.innerHTML = `<img src="https://example.com/cat.png" alt="cat" />`;
    document.body.appendChild(container);

    await resolveArtifactImages(container);

    expect(api).not.toHaveBeenCalled();
  });
});

describe("handleArtifactLinkClick", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    vi.mocked(toast).mockReset();
    vi.spyOn(window, "open").mockImplementation(() => null);
  });

  function clickEventFor(el: HTMLElement) {
    return {
      target: el,
      preventDefault: vi.fn(),
    } as unknown as React.MouseEvent;
  }

  it("intercepts a click on an internal artifact link and opens a fresh URL", async () => {
    const path = artifactDownloadPath("artifact-1");
    vi.mocked(api).mockResolvedValue({ url: "https://s3.example.com/fresh" });

    const link = document.createElement("a");
    link.setAttribute("data-artifact-download", path);
    document.body.appendChild(link);

    const handled = handleArtifactLinkClick(clickEventFor(link));
    expect(handled).toBe(true);

    await vi.waitFor(() => {
      expect(window.open).toHaveBeenCalledWith("https://s3.example.com/fresh", "_blank");
    });
  });

  it("ignores clicks that don't target an artifact link", () => {
    const div = document.createElement("div");
    document.body.appendChild(div);

    expect(handleArtifactLinkClick(clickEventFor(div))).toBe(false);
    expect(api).not.toHaveBeenCalled();
  });

  it("toasts an error when resolution fails", async () => {
    const path = artifactDownloadPath("artifact-1");
    vi.mocked(api).mockRejectedValue(new Error("boom"));

    const link = document.createElement("a");
    link.setAttribute("data-artifact-download", path);
    document.body.appendChild(link);

    handleArtifactLinkClick(clickEventFor(link));

    await vi.waitFor(() => {
      expect(toast).toHaveBeenCalledWith("Could not open file");
    });
  });
});
