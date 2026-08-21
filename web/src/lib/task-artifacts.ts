import { api } from "@/lib/api";
import type { Artifact, ArtifactType } from "@/types";

/**
 * Uploading a file as an artifact of a task.
 *
 * Lived in markdown-editor.tsx until that editor was deleted. It is a data
 * operation, not a component concern, and keeping it next to the textarea it
 * happened to be written for is what made three components import an editor to
 * get at an upload function.
 */

/** Best guess at the artifact type, from the MIME type the browser reports. */
function detectArtifactType(mime: string): ArtifactType {
  if (mime.startsWith("image/")) return "image";
  if (
    mime.startsWith("text/") ||
    mime.includes("json") ||
    mime.includes("xml") ||
    mime.includes("yaml")
  )
    return "code";
  if (mime.includes("pdf") || mime.includes("document") || mime.includes("spreadsheet"))
    return "report";
  if (mime.includes("zip") || mime.includes("tar") || mime.includes("gzip")) return "data";
  return "file";
}

/**
 * Routed through `api()` with a FormData body, which api() passes to fetch
 * untouched and without a Content-Type of its own (see serializeBody there) —
 * the browser must set that header itself because it carries the multipart
 * boundary.
 *
 * This used to be a raw fetch, for exactly that Content-Type reason, and the
 * cost of that shortcut was a whole bug (PR #655): api() is also where a 401 is
 * turned into a token refresh and a replay of the request. Mesh access tokens
 * live 15 minutes in tab memory (internal/auth/service.go) and are only
 * refreshed when something gets a 401 back, so an upload attempted across that
 * boundary was the one action on a task that could not recover — it just threw,
 * while every neighbouring action (autosave, posting a comment) sailed through.
 * That is why the defect read as "sometimes it doesn't attach" rather than as a
 * broken button. Same fix as PR #620 made for document attachments.
 *
 * The raw-fetch form was carried into this file when the function was lifted
 * out of markdown-editor.tsx, before #655 landed on that editor; it is restored
 * here rather than lost with the deleted file.
 */
export async function uploadArtifact(
  taskId: string,
  file: File,
  artifactType?: ArtifactType,
): Promise<Artifact> {
  const form = new FormData();
  form.append("file", file, file.name);
  form.append("name", file.name);
  form.append("artifact_type", artifactType ?? detectArtifactType(file.type));

  return api<Artifact>(`/api/v1/tasks/${taskId}/artifacts`, {
    method: "POST",
    body: form,
  });
}

/**
 * An image dropped or pasted into a task that does not exist yet.
 *
 * The create dialog cannot upload — there is no task to own the bytes — so the
 * editor inserts a placeholder link and hands the file over; the dialog uploads
 * after the task is created and swaps the placeholder for the real path.
 *
 * `placeholder` is the exact markdown the editor put in the body, so the swap is
 * a plain string replace on the description.
 */
export interface PendingImage {
  file: File;
  placeholder: string;
}

/**
 * The href a not-yet-uploaded image carries.
 *
 * `.invalid` is reserved by RFC 2606 and can never resolve, so a placeholder
 * that somehow survives to the saved description is a dead link rather than a
 * request to somebody else's server. It is an http URL rather than an empty one
 * so that it round-trips through the markdown serializer unchanged — which is
 * what makes the string replace after upload deterministic.
 */
export const PENDING_IMAGE_PREFIX = "https://pending.invalid/";

let pendingSeq = 0;

export function pendingImageHref(): string {
  pendingSeq += 1;
  return `${PENDING_IMAGE_PREFIX}${Date.now()}-${pendingSeq}`;
}
