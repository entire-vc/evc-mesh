import type {
  ChatEvent,
  ChatMessage,
  ChatSessionState,
  ChatToolEvent,
  ChatTurnRef,
} from "./types";

/**
 * Everything the open conversation shows, folded from the REST page and the
 * chat.* events. A pure reducer so the stream rules (offsets, duplicates,
 * isolation between threads) are tested without a socket.
 */
export interface ThreadState {
  threadId: string;
  projectId: string;
  // Oldest first.
  messages: ChatMessage[];
  activeTurn: ChatTurnRef | null;
  sessionState: ChatSessionState;
  tools: ChatToolEvent[];
  // Set by a failed turn; cleared by the next send.
  error: { code: string; turnId: string } | null;
  // Last seq folded in. Events at or below it are duplicates.
  lastSeq: number;
  // A hole in seq or in a delta offset: the page must be refetched.
  needsResync: boolean;
  // Turns that ended here, newest last: a send response may arrive after its turn's end.
  endedTurnIds: string[];
}

export function initialThreadState(projectId: string, threadId: string): ThreadState {
  return {
    projectId,
    threadId,
    messages: [],
    activeTurn: null,
    sessionState: "stopped",
    tools: [],
    error: null,
    lastSeq: 0,
    needsResync: false,
    endedTurnIds: [],
  };
}

/** Names the conversation an async result belongs to; a late one for another thread is dropped. */
interface Scope {
  projectId: string;
  threadId: string;
}

export type ThreadAction =
  | { type: "reset"; projectId: string; threadId: string }
  | ({ type: "loaded"; messages: ChatMessage[]; activeTurn: ChatTurnRef | null; sessionState?: ChatSessionState } & Scope)
  | ({ type: "prepend"; messages: ChatMessage[] } & Scope)
  | ({ type: "optimistic"; message: ChatMessage } & Scope)
  | ({ type: "send-failed"; clientMessageId: string } & Scope)
  | ({ type: "sent"; message: ChatMessage; turn: ChatTurnRef } & Scope)
  | ({ type: "turn"; turn: ChatTurnRef | null } & Scope)
  | { type: "event"; event: ChatEvent };

/** Id prefix of a message shown before the server has accepted it. */
export const LOCAL_PREFIX = "local-";

const TERMINAL = new Set(["done", "failed", "cancelled", "expired"]);

/** Code points, because the server counts offsets in Unicode code points. */
function codePointLength(s: string): number {
  let n = 0;
  for (const _ of s) n++;
  return n;
}

function sliceCodePoints(s: string, from: number): string {
  return Array.from(s).slice(from).join("");
}

function byCreatedAt(a: ChatMessage, b: ChatMessage): number {
  return a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0;
}

/** Inserts or replaces by id; an optimistic row is replaced by client_message_id. */
export function upsertMessage(list: ChatMessage[], m: ChatMessage): ChatMessage[] {
  let idx = list.findIndex((x) => x.id === m.id);
  if (idx < 0 && m.client_message_id) {
    idx = list.findIndex((x) => x.client_message_id === m.client_message_id);
  }
  if (idx >= 0) {
    const next = list.slice();
    next[idx] = m;
    return next.sort(byCreatedAt);
  }
  return [...list, m].sort(byCreatedAt);
}

/**
 * A REST snapshot that was in flight while events kept arriving must not erase
 * them: keep what the stream already delivered that the snapshot lacks or has
 * seen only half way. After a detected gap (needsResync) the snapshot is trusted whole.
 */
function mergeLoaded(current: ChatMessage[], loaded: ChatMessage[]): ChatMessage[] {
  if (current.length === 0) return [...loaded].sort(byCreatedAt);
  const oldest = loaded.length > 0 ? loaded.reduce((a, m) => (m.created_at < a ? m.created_at : a), loaded[0]!.created_at) : "";
  const byId = new Map(loaded.map((m) => [m.id, m]));
  const byClient = new Set(loaded.map((m) => m.client_message_id).filter((c): c is string => !!c));
  const out = new Map(byId);
  for (const m of current) {
    const seen = byId.get(m.id);
    if (seen) {
      if (TERMINAL.has(m.status) && !TERMINAL.has(seen.status)) out.set(m.id, m);
    } else if (!(m.client_message_id && byClient.has(m.client_message_id)) && m.created_at >= oldest) {
      out.set(m.id, m);
    }
  }
  return [...out.values()].sort(byCreatedAt);
}

export function threadReducer(state: ThreadState, action: ThreadAction): ThreadState {
  switch (action.type) {
    case "reset":
      return initialThreadState(action.projectId, action.threadId);
    case "event":
      return applyEvent(state, action.event);
  }
  // Every other action carries the thread it was started in: the user may have
  // opened another conversation while the request was in flight.
  if (action.threadId !== state.threadId || action.projectId !== state.projectId) return state;
  switch (action.type) {
    case "loaded":
      return {
        ...state,
        messages: state.needsResync ? [...action.messages].sort(byCreatedAt) : mergeLoaded(state.messages, action.messages),
        activeTurn: action.activeTurn,
        sessionState: action.sessionState ?? state.sessionState,
        needsResync: false,
      };
    case "prepend": {
      const known = new Set(state.messages.map((m) => m.id));
      const older = action.messages.filter((m) => !known.has(m.id));
      return { ...state, messages: [...older, ...state.messages].sort(byCreatedAt) };
    }
    case "optimistic":
      return { ...state, messages: upsertMessage(state.messages, action.message), error: null };
    case "send-failed":
      return {
        ...state,
        messages: state.messages.filter(
          (m) => !(m.client_message_id === action.clientMessageId && m.id.startsWith(LOCAL_PREFIX)),
        ),
      };
    case "sent": {
      // The turn already ended on the stream while the request was in flight:
      // its state and the user row's status are read back from the page.
      const ended = TERMINAL.has(action.turn.state) || state.endedTurnIds.includes(action.turn.id);
      return {
        ...state,
        messages: upsertMessage(state.messages, action.message),
        activeTurn: ended ? null : action.turn,
        error: null,
        needsResync: state.needsResync || (ended && !TERMINAL.has(action.message.status)),
      };
    }
    case "turn":
      return { ...state, activeTurn: action.turn };
  }
}

function applyEvent(state: ThreadState, e: ChatEvent): ThreadState {
  // The channel is the owner's, shared by all of their threads and projects.
  if (e.thread_id !== state.threadId || e.project_id !== state.projectId) return state;
  if (e.seq <= state.lastSeq) return state;
  const hole = state.lastSeq > 0 && e.seq > state.lastSeq + 1;
  const base: ThreadState = { ...state, lastSeq: e.seq, needsResync: state.needsResync || hole };

  switch (e.event) {
    case "chat.message.created":
    case "chat.message.completed":
      return { ...base, messages: upsertMessage(base.messages, e.message) };

    case "chat.message.delta": {
      const idx = base.messages.findIndex((m) => m.id === e.message_id);
      const current =
        idx >= 0
          ? base.messages[idx]!
          : ({
              id: e.message_id,
              thread_id: e.thread_id,
              role: "assistant",
              body: "",
              status: "streaming",
              truncated: false,
              turn_id: e.turn_id,
              error_code: null,
              client_message_id: null,
              attachments: [],
              created_at: new Date().toISOString(),
            } satisfies ChatMessage);
      const have = codePointLength(current.body);
      if (e.offset > have) return { ...base, needsResync: true };
      const text = e.offset < have ? sliceCodePoints(e.text, have - e.offset) : e.text;
      const updated: ChatMessage = { ...current, body: current.body + text, status: "streaming" };
      const messages = idx >= 0 ? base.messages.map((m, i) => (i === idx ? updated : m)) : [...base.messages, updated];
      return { ...base, messages };
    }

    case "chat.turn.state": {
      const terminal = TERMINAL.has(e.turn.state);
      // The server sends no event for the user row of a turn; when the turn ends
      // its status is read back from the page rather than guessed here.
      const userUnsettled =
        terminal && base.messages.some((m) => m.role === "user" && m.turn_id === e.turn.id && !TERMINAL.has(m.status));
      return {
        ...base,
        needsResync: base.needsResync || userUnsettled,
        endedTurnIds: terminal ? [...base.endedTurnIds, e.turn.id].slice(-20) : base.endedTurnIds,
        activeTurn: terminal ? null : e.turn,
        sessionState: e.session_state,
        tools: terminal ? [] : base.tools,
        error:
          e.error_code && e.turn.state !== "cancelled"
            ? { code: e.error_code, turnId: e.turn.id }
            : terminal
              ? null
              : base.error,
      };
    }

    case "chat.turn.tool":
      return { ...base, tools: [...base.tools, { kind: e.kind, label: e.label }].slice(-50) };

    default:
      return base;
  }
}
