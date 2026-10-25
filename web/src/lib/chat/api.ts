import { api, apiBlob } from "@/lib/api";
import type {
  ChatAgent,
  ChatMessagePage,
  ChatPage,
  ChatPresign,
  ChatProjectSettings,
  ChatThread,
  SendMessageResult,
  StopTurnResult,
} from "./types";

const base = (projectId: string) => `/api/v1/projects/${projectId}/chat`;

export async function listChatAgents(projectId: string): Promise<ChatAgent[]> {
  const res = await api<{ items: ChatAgent[] }>(`${base(projectId)}/agents`);
  return res.items;
}

export function listChatThreads(
  projectId: string,
  opts: { agentId?: string; cursor?: string; limit?: number } = {},
): Promise<ChatPage<ChatThread>> {
  return api<ChatPage<ChatThread>>(`${base(projectId)}/threads`, {
    params: { agent_id: opts.agentId, cursor: opts.cursor, limit: opts.limit },
  });
}

export function createChatThread(projectId: string, agentId: string, title = ""): Promise<ChatThread> {
  return api<ChatThread>(`${base(projectId)}/threads`, {
    method: "POST",
    body: { agent_id: agentId, title },
  });
}

export function getChatThread(projectId: string, threadId: string): Promise<ChatThread> {
  return api<ChatThread>(`${base(projectId)}/threads/${threadId}`);
}

export function renameChatThread(projectId: string, threadId: string, title: string): Promise<ChatThread> {
  return api<ChatThread>(`${base(projectId)}/threads/${threadId}`, {
    method: "PATCH",
    body: { title },
  });
}

export async function deleteChatThread(projectId: string, threadId: string): Promise<void> {
  await api<unknown>(`${base(projectId)}/threads/${threadId}`, { method: "DELETE" });
}

export function listChatMessages(
  projectId: string,
  threadId: string,
  opts: { before?: string; limit?: number } = {},
): Promise<ChatMessagePage> {
  return api<ChatMessagePage>(`${base(projectId)}/threads/${threadId}/messages`, {
    params: { before: opts.before, limit: opts.limit },
  });
}

export function sendChatMessage(
  projectId: string,
  threadId: string,
  input: { clientMessageId: string; body: string; attachmentIds: string[] },
): Promise<SendMessageResult> {
  return api<SendMessageResult>(`${base(projectId)}/threads/${threadId}/messages`, {
    method: "POST",
    body: {
      client_message_id: input.clientMessageId,
      body: input.body,
      attachment_ids: input.attachmentIds,
    },
  });
}

export function stopChatTurn(projectId: string, threadId: string): Promise<StopTurnResult> {
  return api<StopTurnResult>(`${base(projectId)}/threads/${threadId}/stop`, { method: "POST" });
}

export function getChatProjectSettings(projectId: string): Promise<ChatProjectSettings> {
  return api<ChatProjectSettings>(`${base(projectId)}/settings`);
}

export function putChatProjectSettings(
  projectId: string,
  settings: ChatProjectSettings,
): Promise<ChatProjectSettings> {
  return api<ChatProjectSettings>(`${base(projectId)}/settings`, { method: "PUT", body: settings });
}

// The attachment download is a 302 to a presigned URL that needs the bearer
// token on the first hop, so an <img src> cannot use it directly.
export function chatAttachmentPath(projectId: string, attachmentId: string): string {
  return `${base(projectId)}/attachments/${attachmentId}/download`;
}

/**
 * Registers the file with the API, then PUTs the bytes straight to object
 * storage with the headers the presign signed. Returns the attachment id to
 * send with the message.
 */
export async function uploadChatAttachment(
  projectId: string,
  threadId: string,
  file: File,
  size?: { width: number; height: number },
): Promise<string> {
  const presign = await api<ChatPresign>(`${base(projectId)}/attachments`, {
    method: "POST",
    body: {
      thread_id: threadId,
      file_name: file.name,
      mime: file.type || "application/octet-stream",
      size: file.size,
      width: size?.width ?? null,
      height: size?.height ?? null,
    },
  });
  const res = await fetch(presign.upload_url, {
    method: presign.upload_method,
    headers: presign.upload_headers,
    body: file,
  });
  if (!res.ok) throw new Error(`upload failed: ${res.status}`);
  return presign.attachment_id;
}

/** The attachment bytes through the authed hop (the endpoint is a 302 to storage). */
export async function fetchChatAttachmentBlob(projectId: string, attachmentId: string): Promise<Blob> {
  return (await apiBlob(chatAttachmentPath(projectId, attachmentId))).blob;
}
