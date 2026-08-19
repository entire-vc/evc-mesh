import { api } from "@/lib/api";
import { toast } from "@/components/ui/toast";

// Mesh has no cookie auth (see the /workspaces/:ws_id/icon comment in
// cmd/api/main.go) — the access token lives only in tab memory and is sent as
// an Authorization header, which neither <img src> nor a plain navigation can
// attach. Presigned S3 URLs also expire (1h), so they can't be written into a
// task description or a document body directly either.
//
// Instead, markdown produced by an in-app upload references a stable,
// never-expiring API path (the row's UUID is the permanent identifier).
// Renderers resolve it at render/click time via an authenticated fetch to get
// a fresh presigned URL, so the reference in the stored text never goes stale.
//
// Two kinds of upload produce such a path — a task artifact and a document
// attachment — and they are recognised by ONE predicate rather than two, because
// this predicate is the allow-list that decides what may become a
// data-artifact-src. A second, forked copy would be the thing that drifts.
//
// Anchored at both ends, and `[^/?]+` for the id: that is what refuses a path
// traversal (`/download/../../evil`), a second path segment, and an absolute URL
// on another origin, all of which would otherwise pass through the renderers as
// an internal reference.
const ARTIFACT_DOWNLOAD_PATH_RE = /^\/api\/v1\/artifacts\/[^/?]+\/download(\?.*)?$/;
const DOCUMENT_ATTACHMENT_DOWNLOAD_PATH_RE =
  /^\/api\/v1\/document-attachments\/[^/?]+\/download(\?.*)?$/;

export function artifactDownloadPath(artifactId: string): string {
  return `/api/v1/artifacts/${artifactId}/download?disposition=inline`;
}

export function documentAttachmentDownloadPath(attachmentId: string): string {
  return `/api/v1/document-attachments/${attachmentId}/download?disposition=inline`;
}

/**
 * True for the internal download paths a renderer may resolve through an
 * authenticated fetch: a task artifact or a document attachment.
 */
export function isResolvableAttachmentPath(url: string): boolean {
  return (
    ARTIFACT_DOWNLOAD_PATH_RE.test(url) || DOCUMENT_ATTACHMENT_DOWNLOAD_PATH_RE.test(url)
  );
}

/**
 * @deprecated Use isResolvableAttachmentPath. Kept because several components
 * import this name; it is the same predicate, which now also covers document
 * attachments.
 */
export const isArtifactDownloadPath = isResolvableAttachmentPath;

/**
 * Build an <img> tag for markdown image syntax.
 * `altHtml` must already be HTML-escaped by the caller (both renderers escape
 * the full line before extracting inline matches) — this only decides the src.
 */
export function renderArtifactAwareImage(altHtml: string, url: string): string {
  if (isResolvableAttachmentPath(url)) {
    return `<img data-artifact-src="${url}" alt="${altHtml}" style="max-width:100%;border-radius:4px;" class="mesh-artifact-img" />`;
  }
  // Only allow http/https for arbitrary external images — no javascript:/data: src.
  if (url.startsWith("http://") || url.startsWith("https://")) {
    return `<img src="${url}" alt="${altHtml}" style="max-width:100%;border-radius:4px;" />`;
  }
  return altHtml;
}

/**
 * Build an <a> tag for markdown link syntax. Internal artifact and document
 * attachment links get a data attribute resolved on click (see
 * handleArtifactLinkClick) instead of a bare href the browser can't authenticate.
 */
export function renderArtifactAwareLink(labelHtml: string, url: string): string {
  if (isResolvableAttachmentPath(url)) {
    return `<a href="#" data-artifact-download="${url}" class="text-primary underline underline-offset-2 hover:opacity-80">${labelHtml}</a>`;
  }
  const safe = url.startsWith("http://") || url.startsWith("https://") ? url : "#";
  return `<a href="${safe}" target="_blank" rel="noopener noreferrer" class="text-primary underline underline-offset-2 hover:opacity-80">${labelHtml}</a>`;
}

/**
 * Resolve every unresolved artifact/attachment <img> inside `container` to a
 * fresh presigned URL. Clears data-artifact-src as it goes so a re-run over
 * already-resolved images (e.g. an unrelated re-render) is a no-op.
 */
export async function resolveArtifactImages(container: HTMLElement): Promise<void> {
  const imgs = container.querySelectorAll<HTMLImageElement>("img[data-artifact-src]");
  await Promise.all(
    Array.from(imgs).map(async (img) => {
      const path = img.getAttribute("data-artifact-src");
      if (!path) return;
      img.removeAttribute("data-artifact-src");
      try {
        const data = await api<{ url: string }>(path);
        img.src = data.url;
      } catch {
        img.alt = `${img.alt} (failed to load)`;
        img.classList.add("opacity-50");
      }
    }),
  );
}

/**
 * Delegated click handler for rendered-markdown containers: intercepts clicks
 * on internal artifact/attachment download links and opens a freshly-resolved
 * URL. Returns true if it handled the click (caller should skip its own logic).
 */
export function handleArtifactLinkClick(e: React.MouseEvent): boolean {
  const target = e.target as HTMLElement;
  const link = target.closest("a[data-artifact-download]");
  if (!(link instanceof HTMLAnchorElement)) return false;
  e.preventDefault();
  const path = link.getAttribute("data-artifact-download");
  if (!path) return true;
  void api<{ url: string }>(path)
    .then((data) => window.open(data.url, "_blank"))
    .catch(() => toast("Could not open file"));
  return true;
}
