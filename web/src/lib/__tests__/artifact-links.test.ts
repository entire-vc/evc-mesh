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
  documentAttachmentDownloadPath,
  isArtifactDownloadPath,
  isResolvableAttachmentPath,
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

  it("marks an image as failed instead of throwing when resolution fails, but does not strip data-artifact-src", async () => {
    vi.useFakeTimers();
    try {
      const path = artifactDownloadPath("artifact-1");
      vi.mocked(api).mockRejectedValue(new Error("boom"));

      const container = document.createElement("div");
      container.innerHTML = `<img data-artifact-src="${path}" alt="shot" />`;
      document.body.appendChild(container);

      await resolveArtifactImages(container);

      const img = container.querySelector("img")!;
      expect(img.alt).toContain("failed to load");
      expect(img.classList.contains("opacity-50")).toBe(true);
      // A transient failure must stay retryable: stripping this attribute is
      // exactly what used to make the image permanently broken until an
      // unrelated Edit→Done regenerated the tag from scratch.
      expect(img.getAttribute("data-artifact-src")).toBe(path);
    } finally {
      vi.useRealTimers();
    }
  });

  it("auto-retries once after a transient failure and self-heals without any external action", async () => {
    vi.useFakeTimers();
    try {
      const path = artifactDownloadPath("artifact-1");
      vi.mocked(api)
        .mockRejectedValueOnce(new Error("boom"))
        .mockResolvedValueOnce({ url: "https://s3.example.com/fresh" });

      const container = document.createElement("div");
      container.innerHTML = `<img data-artifact-src="${path}" alt="shot" />`;
      document.body.appendChild(container);

      await resolveArtifactImages(container);
      const img = container.querySelector("img")!;
      expect(img.alt).toContain("failed to load");

      await vi.advanceTimersByTimeAsync(1500);

      expect(img.src).toBe("https://s3.example.com/fresh");
      expect(img.hasAttribute("data-artifact-src")).toBe(false);
      expect(img.classList.contains("opacity-50")).toBe(false);
      expect(img.alt).toBe("shot");
      expect(api).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a later re-run still retries an image whose one scheduled retry also failed", async () => {
    vi.useFakeTimers();
    try {
      const path = artifactDownloadPath("artifact-1");
      vi.mocked(api).mockRejectedValue(new Error("boom"));

      const container = document.createElement("div");
      container.innerHTML = `<img data-artifact-src="${path}" alt="shot" />`;
      document.body.appendChild(container);

      await resolveArtifactImages(container);
      await vi.advanceTimersByTimeAsync(1500);
      const img = container.querySelector("img")!;
      // Both the initial attempt and the one scheduled retry failed.
      expect(api).toHaveBeenCalledTimes(2);
      expect(img.getAttribute("data-artifact-src")).toBe(path);

      // A later re-render (content change, reopening the task) calls
      // resolveArtifactImages again — this must still find the image and
      // try it, not skip it as already-handled.
      vi.mocked(api).mockReset();
      vi.mocked(api).mockResolvedValue({ url: "https://s3.example.com/fresh-2" });
      await resolveArtifactImages(container);

      expect(img.src).toBe("https://s3.example.com/fresh-2");
      expect(img.hasAttribute("data-artifact-src")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
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

// The predicate is the allow-list that decides what may become a
// data-artifact-src, so widening it to a second route family is the change worth
// stating twice: once for what it now accepts, and once — at more length — for
// what it still refuses.
describe("documentAttachmentDownloadPath / isResolvableAttachmentPath", () => {
  it("builds a stable, inline-disposition path from an attachment id", () => {
    expect(documentAttachmentDownloadPath("att-1")).toBe(
      "/api/v1/document-attachments/att-1/download?disposition=inline",
    );
  });

  it("accepts BOTH generated forms through one predicate", () => {
    expect(
      isResolvableAttachmentPath(artifactDownloadPath("6b1f9c2a-1111-4a2b-8c3d-000000000001")),
    ).toBe(true);
    expect(
      isResolvableAttachmentPath(
        documentAttachmentDownloadPath("6b1f9c2a-1111-4a2b-8c3d-000000000002"),
      ),
    ).toBe(true);
  });

  it("accepts the bare paths without a query string", () => {
    expect(isResolvableAttachmentPath("/api/v1/artifacts/a1/download")).toBe(true);
    expect(isResolvableAttachmentPath("/api/v1/document-attachments/a1/download")).toBe(true);
  });

  it("refuses a path traversal appended after /download", () => {
    // The id group is [^/?]+ and the pattern is anchored, so an extra path
    // segment cannot ride along behind the part that looks legitimate.
    expect(isResolvableAttachmentPath("/api/v1/artifacts/x/download/../../evil")).toBe(false);
    expect(
      isResolvableAttachmentPath("/api/v1/document-attachments/x/download/../../evil"),
    ).toBe(false);
  });

  it("refuses a traversal inside the id segment", () => {
    expect(isResolvableAttachmentPath("/api/v1/document-attachments/../../evil/download")).toBe(
      false,
    );
  });

  it("refuses an absolute URL on another origin that merely contains the path", () => {
    // Anchoring at ^ is what stops this: without it, an attacker-controlled
    // origin with our path as a suffix would resolve as an internal reference and
    // be fetched with the caller's credentials.
    expect(
      isResolvableAttachmentPath("https://evil.example/api/v1/document-attachments/x/download"),
    ).toBe(false);
    expect(isResolvableAttachmentPath("https://evil.example/api/v1/artifacts/x/download")).toBe(
      false,
    );
  });

  it("refuses a document path that is not an attachment download", () => {
    expect(isResolvableAttachmentPath("/api/v1/documents/x")).toBe(false);
    expect(isResolvableAttachmentPath("/api/v1/documents/x/attachments")).toBe(false);
    expect(isResolvableAttachmentPath("/api/v1/document-attachments/x")).toBe(false);
  });

  it("still refuses the unrelated URLs the artifact-only predicate refused", () => {
    expect(isResolvableAttachmentPath("https://evil.example.com/x")).toBe(false);
    expect(isResolvableAttachmentPath("javascript:alert(1)")).toBe(false);
  });

  it("keeps isArtifactDownloadPath as the same predicate for its existing importers", () => {
    // Several components import the old name. It has to be the widened predicate,
    // not a surviving narrow copy, or a document attachment would render as plain
    // alt text in exactly those components.
    expect(isArtifactDownloadPath).toBe(isResolvableAttachmentPath);
    expect(isArtifactDownloadPath(documentAttachmentDownloadPath("att-1"))).toBe(true);
  });
});

describe("the renderers treat a document attachment exactly like an artifact", () => {
  it("defers an attachment image to data-artifact-src", () => {
    const url = documentAttachmentDownloadPath("att-1");
    const html = renderArtifactAwareImage("screenshot", url);
    expect(html).toContain(`data-artifact-src="${url}"`);
    expect(html).not.toMatch(/(?<!data-artifact-)src="/);
  });

  it("routes an attachment link through data-artifact-download", () => {
    const url = documentAttachmentDownloadPath("att-1");
    const html = renderArtifactAwareLink("spec.pdf", url);
    expect(html).toContain(`data-artifact-download="${url}"`);
    expect(html).toContain('href="#"');
  });

  it("resolves an attachment image through the same authenticated fetch", async () => {
    const path = documentAttachmentDownloadPath("att-1");
    vi.mocked(api).mockReset();
    vi.mocked(api).mockResolvedValue({ url: "https://s3.example.com/fresh-attachment" });

    const container = document.createElement("div");
    container.innerHTML = `<img data-artifact-src="${path}" alt="shot" />`;
    document.body.appendChild(container);

    await resolveArtifactImages(container);

    expect(api).toHaveBeenCalledWith(path);
    expect(container.querySelector("img")!.src).toBe("https://s3.example.com/fresh-attachment");
  });
});

// The path written into a markdown body must carry no signature material at all.
// This is the mechanical half of "the image still opens an hour later": a stored
// reference that contained a signature would be a reference with an expiry date,
// and the page would break exactly one hour after it was written. The Go side
// proves the other half — that resolving it twice yields two different signatures.
describe("stored markdown references carry no expiring credential", () => {
  it("has no AWS signature material in either generated path", () => {
    for (const path of [
      artifactDownloadPath("6b1f9c2a-1111-4a2b-8c3d-000000000001"),
      documentAttachmentDownloadPath("6b1f9c2a-1111-4a2b-8c3d-000000000002"),
    ]) {
      expect(path).not.toContain("X-Amz-");
      expect(path).not.toMatch(/[?&](Signature|Expires|Policy)=/i);
      expect(path.startsWith("/api/v1/")).toBe(true);
    }
  });
});
