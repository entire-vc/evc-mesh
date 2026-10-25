// Shapes of the agent-chat API (internal/repository/postgres/agent_chat_repo.go)
// and of its chat.* WebSocket events (internal/service/chat_event_publisher.go).

export type ChatTurnState =
  | "queued"
  | "claimed"
  | "streaming"
  | "done"
  | "failed"
  | "cancelled"
  | "expired";

export type ChatMessageStatus = "queued" | "streaming" | "done" | "failed" | "cancelled";

// The header state of a chat: `working` while the agent answers, `starting`
// while a runner has claimed the turn, `ready` with a live session, `stopped`
// when the next message starts one again.
export type ChatSessionState = "working" | "starting" | "ready" | "stopped";

export type ChatPresence = "online" | "offline" | "busy";

export interface ChatTurnRef {
  id: string;
  state: ChatTurnState;
  queue_position?: number;
  agent_offline?: boolean;
}

export interface ChatThread {
  id: string;
  project_id: string;
  agent_id: string;
  title: string;
  title_is_custom: boolean;
  last_message_at: string | null;
  created_at: string;
  session_state: ChatSessionState;
  active_turn: ChatTurnRef | null;
}

export interface ChatAttachment {
  id: string;
  file_name: string;
  mime: string;
  size: number;
  width: number | null;
  height: number | null;
  download_url?: string;
}

export interface ChatMessage {
  id: string;
  thread_id: string;
  role: "user" | "assistant";
  body: string;
  status: ChatMessageStatus;
  truncated: boolean;
  turn_id: string | null;
  error_code: string | null;
  client_message_id: string | null;
  attachments: ChatAttachment[];
  created_at: string;
}

export interface ChatAgent {
  agent_id: string;
  name: string;
  color_token: string;
  presence: ChatPresence;
  active_sessions: number;
  queue_length: number;
}

export interface ChatPage<T> {
  items: T[];
  has_more: boolean;
  next_cursor: string | null;
}

export interface ChatMessagePage extends ChatPage<ChatMessage> {
  active_turn: ChatTurnRef | null;
}

export interface SendMessageResult {
  message: ChatMessage;
  turn: ChatTurnRef;
}

export interface StopTurnResult {
  stopped: boolean;
  turn: ChatTurnRef | null;
}

export interface ChatPresign {
  attachment_id: string;
  upload_url: string;
  upload_method: "PUT";
  upload_headers: Record<string, string>;
  expires_at: string;
}

export interface ChatProjectSettings {
  enabled: boolean;
  agents: string[];
  // Only the workspace owner and admins receive the roles.
  allowed_roles?: string[];
}

// One entry of the tool strip: what the agent is doing right now.
export interface ChatToolEvent {
  kind?: string;
  label: string;
}

interface Envelope {
  event: string;
  workspace_id: string;
  project_id: string;
  thread_id: string;
  seq: number;
}

export type ChatEvent =
  | (Envelope & { event: "chat.message.created" | "chat.message.completed"; message: ChatMessage })
  | (Envelope & {
      event: "chat.message.delta";
      message_id: string;
      turn_id: string;
      // Unicode code points already sent, not UTF-16 units.
      offset: number;
      text: string;
    })
  | (Envelope & {
      event: "chat.turn.state";
      turn: ChatTurnRef;
      session_state: ChatSessionState;
      error_code: string | null;
    })
  | (Envelope & { event: "chat.turn.tool"; turn_id: string; kind?: string; label: string })
  | (Envelope & {
      event: "chat.thread.changed";
      change: "created" | "renamed" | "deleted" | string;
      thread?: ChatThread;
    });
