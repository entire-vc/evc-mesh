import { api } from "@/lib/api";

/**
 * A file uploaded into a document — mirrors domain.DocumentAttachment.
 *
 * Declared here rather than in types/index.ts on purpose: nothing renders a
 * document yet (the viewer is a separate unit), so the shape has exactly one
 * consumer and moving it to the shared types barrel can wait until a second one
 * exists.
 */
export interface DocumentAttachment {
  id: string;
  document_id: string;
  name: string;
  mime_type: string;
  size_bytes: number;
  storage_key: string;
  uploaded_by: string;
  uploaded_by_type: "user" | "agent" | "system";
  created_at: string;
  deleted_at?: string;
}

/**
 * Upload a file into a document.
 *
 * Routed through `api()` with a FormData body, which api() passes to fetch
 * untouched and without a Content-Type of its own (see serializeBody there) —
 * the browser must set that header itself because it carries the multipart
 * boundary.
 *
 * This used to be a raw fetch, for exactly that Content-Type reason, and the
 * cost of that shortcut was the whole of this bug: api() is also where a 401 is
 * turned into a token refresh and a replay of the request. Mesh access tokens
 * live 15 minutes in tab memory and are only refreshed when something gets a 401
 * back, so an upload attempted across that boundary was the one action in the
 * app that could not recover — it just threw, and the editor said nothing.
 *
 * The returned `id` is what a markdown reference is built from — see
 * documentAttachmentDownloadPath in artifact-links.ts. Nothing writes a presigned
 * URL into a body: those expire in an hour and the stored text does not.
 */
export async function uploadDocumentAttachment(
  documentId: string,
  file: File,
): Promise<DocumentAttachment> {
  const form = new FormData();
  form.append("file", file, file.name);
  form.append("name", file.name);

  return api<DocumentAttachment>(`/api/v1/documents/${documentId}/attachments`, {
    method: "POST",
    body: form,
  });
}
