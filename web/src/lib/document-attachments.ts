import { getAccessToken } from "@/lib/api";

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
 * Deliberately not routed through `api()`: that helper sets a JSON Content-Type,
 * and a multipart body needs the browser to set its own with the boundary it
 * generated. Same shape as uploadArtifact in markdown-editor.tsx, which does this
 * for the same reason.
 *
 * The returned `id` is what a markdown reference is built from — see
 * documentAttachmentDownloadPath in artifact-links.ts. Nothing writes a presigned
 * URL into a body: those expire in an hour and the stored text does not.
 */
export async function uploadDocumentAttachment(
  documentId: string,
  file: File,
): Promise<DocumentAttachment> {
  const token = getAccessToken();
  const baseUrl = import.meta.env.VITE_API_URL || "";

  const form = new FormData();
  form.append("file", file, file.name);
  form.append("name", file.name);

  const headers: HeadersInit = {};
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }

  const res = await fetch(`${baseUrl}/api/v1/documents/${documentId}/attachments`, {
    method: "POST",
    headers,
    body: form,
  });

  if (!res.ok) {
    const err = (await res.json().catch(() => ({}))) as { message?: string };
    throw new Error(err.message ?? `Upload failed (${res.status})`);
  }

  return (await res.json()) as DocumentAttachment;
}
