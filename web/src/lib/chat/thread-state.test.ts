import { describe, expect, it } from "vitest";
import { initialThreadState, threadReducer, type ThreadState } from "./thread-state";
import type { ChatEvent, ChatMessage } from "./types";

const P = "p1";
const T = "t1";

function env(seq: number, over: Partial<{ thread_id: string; project_id: string }> = {}) {
  return { workspace_id: "w", project_id: over.project_id ?? P, thread_id: over.thread_id ?? T, seq };
}

function msg(over: Partial<ChatMessage>): ChatMessage {
  return {
    id: "m1",
    thread_id: T,
    role: "assistant",
    body: "",
    status: "streaming",
    truncated: false,
    turn_id: "turn1",
    error_code: null,
    client_message_id: null,
    attachments: [],
    created_at: "2026-10-24T10:00:00Z",
    ...over,
  };
}

const fold = (s: ThreadState, ...events: ChatEvent[]) =>
  events.reduce((acc, event) => threadReducer(acc, { type: "event", event }), s);

const delta = (seq: number, offset: number, text: string): ChatEvent => ({
  ...env(seq),
  event: "chat.message.delta",
  message_id: "m1",
  turn_id: "turn1",
  offset,
  text,
});

describe("threadReducer", () => {
  const start = initialThreadState(P, T);

  it("appends deltas by code-point offset", () => {
    const s = fold(start, delta(1, 0, "Привет "), delta(2, 7, "мир"));
    expect(s.messages[0]?.body).toBe("Привет мир");
    expect(s.needsResync).toBe(false);
  });

  it("counts an astral character as one code point", () => {
    const s = fold(start, delta(1, 0, "a😀"), delta(2, 2, "b"));
    expect(s.messages[0]?.body).toBe("a😀b");
    expect(s.needsResync).toBe(false);
  });

  it("drops a replayed event", () => {
    const s = fold(start, delta(1, 0, "ab"), delta(1, 0, "ab"));
    expect(s.messages[0]?.body).toBe("ab");
  });

  it("keeps only the new tail of an overlapping delta", () => {
    const s = fold(start, delta(1, 0, "abc"), delta(2, 1, "bcd"));
    expect(s.messages[0]?.body).toBe("abcd");
  });

  it("asks for a refetch on an offset gap and on a seq hole", () => {
    expect(fold(start, delta(1, 5, "x")).needsResync).toBe(true);
    expect(fold(start, delta(1, 0, "a"), delta(3, 1, "b")).needsResync).toBe(true);
  });

  it("ignores events of another thread or project", () => {
    const other: ChatEvent = { ...delta(1, 0, "secret"), ...env(1, { thread_id: "t2" }) };
    const otherProject: ChatEvent = { ...delta(1, 0, "secret"), ...env(1, { project_id: "p2" }) };
    expect(fold(start, other, otherProject)).toEqual(start);
  });

  it("completed replaces the streamed body", () => {
    const s = fold(start, delta(1, 0, "ab"), {
      ...env(2),
      event: "chat.message.completed",
      message: msg({ body: "final", status: "done" }),
    });
    expect(s.messages).toHaveLength(1);
    expect(s.messages[0]).toMatchObject({ body: "final", status: "done" });
  });

  it("replaces an optimistic row by client_message_id", () => {
    let s = threadReducer(start, {
      type: "optimistic",
      projectId: P,
      threadId: T,
      message: msg({ id: "local-1", role: "user", body: "hi", status: "queued", client_message_id: "c1" }),
    });
    s = fold(s, {
      ...env(1),
      event: "chat.message.created",
      message: msg({ id: "srv-1", role: "user", body: "hi", status: "queued", client_message_id: "c1" }),
    });
    expect(s.messages.map((m) => m.id)).toEqual(["srv-1"]);
  });

  it("tracks the turn, tools and a failure code", () => {
    let s = fold(
      start,
      { ...env(1), event: "chat.turn.state", turn: { id: "turn1", state: "streaming" }, session_state: "working", error_code: null },
      { ...env(2), event: "chat.turn.tool", turn_id: "turn1", label: "Читаю файл" },
    );
    expect(s.activeTurn?.state).toBe("streaming");
    expect(s.tools).toEqual([{ kind: undefined, label: "Читаю файл" }]);
    s = fold(s, {
      ...env(3),
      event: "chat.turn.state",
      turn: { id: "turn1", state: "failed" },
      session_state: "ready",
      error_code: "runner_lost",
    });
    expect(s.activeTurn).toBeNull();
    expect(s.tools).toEqual([]);
    expect(s.error).toEqual({ code: "runner_lost", turnId: "turn1" });
  });

  it("a stop is not an error", () => {
    const s = fold(start, {
      ...env(1),
      event: "chat.turn.state",
      turn: { id: "turn1", state: "cancelled" },
      session_state: "ready",
      error_code: null,
    });
    expect(s.error).toBeNull();
    expect(s.activeTurn).toBeNull();
  });

  it("a snapshot that was in flight does not erase a message the stream delivered meanwhile", () => {
    const old = msg({ id: "old", status: "done", created_at: "2026-10-24T09:00:00Z" });
    const live = msg({ id: "live", status: "streaming", created_at: "2026-10-24T10:00:00Z" });
    let s = threadReducer(initialThreadState(P, T), { type: "loaded", projectId: P, threadId: T, messages: [old], activeTurn: null });
    s = threadReducer(s, { type: "event", event: { ...env(1), event: "chat.message.created", message: live } as ChatEvent });
    expect(s.messages.map((m) => m.id)).toEqual(["old", "live"]);
    s = threadReducer(s, { type: "loaded", projectId: P, threadId: T, messages: [old], activeTurn: null });
    expect(s.messages.map((m) => m.id)).toEqual(["old", "live"]);
  });

  it("after a detected gap the snapshot replaces the list whole", () => {
    const a = msg({ id: "a", status: "done" });
    let s = threadReducer(initialThreadState(P, T), { type: "loaded", projectId: P, threadId: T, messages: [a], activeTurn: null });
    s = { ...s, needsResync: true };
    s = threadReducer(s, { type: "loaded", projectId: P, threadId: T, messages: [msg({ id: "b", status: "done" })], activeTurn: null });
    expect(s.messages.map((m) => m.id)).toEqual(["b"]);
  });
});

describe("late async results after switching threads", () => {
  const turn = { id: "turn-a", state: "running" } as never;
  it("drops a late send response, stop and page for the previous thread", () => {
    const b = initialThreadState(P, "t2");
    const sent = threadReducer(b, { type: "sent", projectId: P, threadId: T, message: msg({ id: "m-a" }), turn });
    expect(sent).toBe(b);
    expect(threadReducer(b, { type: "turn", projectId: P, threadId: T, turn })).toBe(b);
    expect(threadReducer(b, { type: "prepend", projectId: P, threadId: T, messages: [msg({ id: "x" })] })).toBe(b);
    // control: the same action for the open thread still applies
    expect(threadReducer(initialThreadState(P, T), { type: "sent", projectId: P, threadId: T, message: msg({ id: "m-a" }), turn }).messages).toHaveLength(1);
  });
});
