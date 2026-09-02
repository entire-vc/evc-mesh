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
const ARTIFACT_DOWNLOAD_PATH_RE = /^\/api\/v1\/artifacts\/([^/?]+)\/download(\?.*)?$/;
const DOCUMENT_ATTACHMENT_DOWNLOAD_PATH_RE =
  /^\/api\/v1\/document-attachments\/[^/?]+\/download(\?.*)?$/;

export function artifactDownloadPath(artifactId: string): string {
  return `/api/v1/artifacts/${artifactId}/download?disposition=inline`;
}

/**
 * The task-artifact id embedded in a resolvable download path, or null.
 *
 * Deliberately narrower than isResolvableAttachmentPath: it only recognises the
 * task-artifact shape, because the one caller (rich-text-editor's inline
 * attachment delete) only ever needs to call DELETE /api/v1/artifacts/:id — a
 * document attachment is a different resource with a different delete route,
 * and task descriptions never reference one.
 */
export function artifactIdFromDownloadPath(path: string): string | null {
  return ARTIFACT_DOWNLOAD_PATH_RE.exec(path)?.[1] ?? null;
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
 * Read the internal download path off a clicked anchor, in either of the two
 * shapes a renderer produces.
 *
 * The HTML renderer withholds the href and parks the path on
 * data-artifact-download (see renderArtifactAwareLink). Milkdown's `link` mark
 * has no such attribute to park it on — it stores an href and nothing else — so
 * an attachment link in a document body arrives here as an ordinary
 * `<a href="/api/v1/document-attachments/...">`. Recognising only the first
 * shape is what made an attached file unopenable: the click fell through to the
 * router, which took the whole app to the API path and landed the reader on the
 * workspace fallback page, out of the document they were reading.
 *
 * Both shapes are checked against the same allow-list, so this cannot become a
 * way to resolve an arbitrary href through an authenticated fetch.
 */
function attachmentPathOf(link: HTMLAnchorElement): string | null {
  const parked = link.getAttribute("data-artifact-download");
  if (parked) return isResolvableAttachmentPath(parked) ? parked : null;
  const href = link.getAttribute("href");
  if (href && isResolvableAttachmentPath(href)) return href;
  return null;
}

/**
 * Delegated click handler for rendered-markdown containers: intercepts clicks
 * on internal artifact/attachment download links and opens a freshly-resolved
 * URL. Returns true if it handled the click (caller should skip its own logic).
 */
export function handleArtifactLinkClick(e: React.MouseEvent): boolean {
  const target = e.target as HTMLElement;
  const link = target.closest("a");
  if (!(link instanceof HTMLAnchorElement)) return false;
  const path = attachmentPathOf(link);
  if (!path) return false;
  e.preventDefault();
  void api<{ url: string }>(path)
    .then((data) => window.open(data.url, "_blank"))
    .catch(() => toast("Could not open file"));
  return true;
}
