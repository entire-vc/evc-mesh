import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { ChatPage } from "@/pages/chat";
import { ApiRequestError, api } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { useProjectStore } from "@/stores/project";
import { useWebSocketStore } from "@/stores/websocket";
import type { ChatEvent, ChatMessage, ChatThread } from "@/lib/chat/types";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  api: vi.fn(),
  apiBlob: vi.fn().mockResolvedValue({ blob: new Blob(["x"], { type: "image/png" }), filename: "x.png" }),
}));
vi.mock("@/components/ui/toast", () => ({ toast: { error: vi.fn(), success: vi.fn() }, Toaster: () => null }));

const mockedApi = vi.mocked(api);
const ME = "user-me";
const P = "proj-1";
const AGENT_ON = "agent-on";
const AGENT_OFF = "agent-off";

const PROJECT = { id: P, slug: "demo", name: "Demo" } as never;

function thread(over: Partial<ChatThread> & { id: string }): ChatThread {
  return {
    project_id: P,
    agent_id: AGENT_ON,
    title: over.id,
    title_is_custom: false,
    last_message_at: "2026-10-24T10:00:00Z",
    created_at: "2026-10-24T09:00:00Z",
    session_state: "ready",
    active_turn: null,
    ...over,
  };
}

function message(over: Partial<ChatMessage> & { id: string }): ChatMessage {
  return {
    thread_id: "t1",
    role: "assistant",
    body: "",
    status: "done",
    truncated: false,
    turn_id: null,
    error_code: null,
    client_message_id: null,
    attachments: [],
    created_at: "2026-10-24T10:00:00Z",
    ...over,
  };
}

// The fake backend: what the API would hold. Only the transport is replaced.
let db: {
  threads: ChatThread[];
  messages: Record<string, ChatMessage[]>;
  activeTurn: Record<string, unknown>;
  enabled: boolean;
  calls: { method: string; path: string; body?: unknown }[];
};

function fakeApi(path: string, opts: { method?: string; body?: unknown; params?: Record<string, unknown> } = {}) {
  const method = opts.method ?? "GET";
  db.calls.push({ method, path, body: opts.body });
  const base = `/api/v1/projects/${P}/chat`;
  if (path === `${base}/settings`) {
    return Promise.resolve({ enabled: db.enabled, agents: [AGENT_ON] });
  }
  if (path === `${base}/agents`) {
    return Promise.resolve({
      items: [
        { agent_id: AGENT_ON, name: "Vera", color_token: "avatar-2", presence: "online", active_sessions: 0, queue_length: 0 },
        { agent_id: AGENT_OFF, name: "Other Agent", color_token: "avatar-5", presence: "online", active_sessions: 0, queue_length: 0 },
      ],
    });
  }
  if (path === `${base}/threads` && method === "GET") {
    return Promise.resolve({ items: db.threads, has_more: false, next_cursor: null });
  }
  if (path === `${base}/threads` && method === "POST") {
    const th = thread({ id: "t-new", title: "" });
    db.threads = [th, ...db.threads];
    db.messages["t-new"] = [];
    return Promise.resolve(th);
  }
  const m = /\/threads\/([^/]+)(\/messages|\/stop)?$/.exec(path);
  if (m) {
    const id = m[1]!;
    const th = db.threads.find((x) => x.id === id);
    if (!th) return Promise.reject(new ApiRequestError("not found", "chat_thread_not_found", 404));
    if (!m[2] && method === "GET") return Promise.resolve(th);
    if (!m[2] && method === "DELETE") {
      db.threads = db.threads.filter((x) => x.id !== id);
      return Promise.resolve({});
    }
    if (!m[2] && method === "PATCH") {
      th.title = (opts.body as { title: string }).title;
      return Promise.resolve(th);
    }
    if (m[2] === "/messages" && method === "GET") {
      return Promise.resolve({
        items: [...(db.messages[id] ?? [])].reverse(),
        has_more: false,
        next_cursor: null,
        active_turn: db.activeTurn[id] ?? null,
      });
    }
    if (m[2] === "/messages" && method === "POST") {
      const b = opts.body as { client_message_id: string; body: string; attachment_ids: string[] };
      const msg = message({
        id: `srv-${db.calls.length}`,
        thread_id: id,
        role: "user",
        body: b.body,
        status: "queued",
        client_message_id: b.client_message_id,
        created_at: "2026-10-24T11:00:00Z",
      });
      db.messages[id] = [...(db.messages[id] ?? []), msg];
      return Promise.resolve({ message: msg, turn: { id: "turn-1", state: "queued" } });
    }
    if (m[2] === "/stop") {
      delete db.activeTurn[id];
      return Promise.resolve({ stopped: true, turn: { id: "turn-1", state: "cancelled" } });
    }
  }
  if (path === `${base}/attachments`) {
    return Promise.resolve({
      attachment_id: "att-1",
      upload_url: "https://s3.example/put",
      upload_method: "PUT",
      upload_headers: { "Content-Type": "image/png" },
      expires_at: "2026-10-24T12:00:00Z",
    });
  }
  return Promise.reject(new Error(`unexpected request: ${method} ${path}`));
}

function Where() {
  return <div data-testid="where">{useLocation().pathname}</div>;
}

function renderChat(threadId?: string) {
  const base = "/w/acme/p/demo/chat";
  return render(
    <MemoryRouter initialEntries={[threadId ? `${base}/${threadId}` : base]}>
      <Where />
      <Routes>
        <Route path="/w/:wsSlug/p/:projectSlug/chat" element={<ChatPage />} />
        <Route path="/w/:wsSlug/p/:projectSlug/chat/:threadId" element={<ChatPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

let seq = 0;
function emit(event: Omit<ChatEvent, "workspace_id" | "seq" | "project_id" | "thread_id"> & { thread_id?: string; project_id?: string; seq?: number }) {
  act(() => {
    useWebSocketStore.setState({
      lastEvent: {
        type: "",
        channel: `ws:user:${ME}`,
        timestamp: new Date().toISOString(),
        data: { workspace_id: "w", project_id: P, thread_id: "t1", seq: ++seq, ...event } as never,
      },
    });
  });
}

beforeEach(() => {
  seq = 0;
  db = {
    threads: [thread({ id: "t1", title: "First chat" }), thread({ id: "t2", title: "Second chat" })],
    messages: {
      t1: [
        message({ id: "m1", thread_id: "t1", role: "user", body: "hello agent", created_at: "2026-10-24T10:00:00Z" }),
        message({ id: "m2", thread_id: "t1", role: "assistant", body: "hello human", created_at: "2026-10-24T10:00:05Z" }),
      ],
      t2: [],
    },
    activeTurn: {},
    enabled: true,
    calls: [],
  };
  mockedApi.mockReset();
  mockedApi.mockImplementation(fakeApi as never);
  useProjectStore.setState({ currentProject: PROJECT });
  useAuthStore.setState({ user: { id: ME, email: "m@e.vc", name: "Me" } as never });
  useWebSocketStore.setState({ lastEvent: null });
  localStorage.clear();
  URL.createObjectURL = vi.fn(() => "blob:fake");
  URL.revokeObjectURL = vi.fn();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ChatPage — navigation", () => {
  it("groups my chats under the agents the project turned on, newest first", async () => {
    renderChat();
    const agents = await screen.findAllByTestId("chat-agent");
    // The agent that is not in settings.agents is not offered.
    expect(agents).toHaveLength(1);
    expect(within(agents[0]!).getByText("Vera")).toBeInTheDocument();
    const items = within(agents[0]!).getAllByTestId("chat-thread-item");
    expect(items.map((i) => i.dataset.threadId)).toEqual(["t1", "t2"]);
  });

  it("filters chats by title", async () => {
    renderChat();
    await screen.findAllByTestId("chat-thread-item");
    fireEvent.change(screen.getByLabelText(/find a chat/i), { target: { value: "second" } });
    expect(screen.getAllByTestId("chat-thread-item").map((i) => i.dataset.threadId)).toEqual(["t2"]);
  });

  it("opens a thread from the URL and shows my message right, the agent's left", async () => {
    renderChat("t1");
    const msgs = await screen.findAllByTestId("chat-message");
    expect(msgs.map((m) => m.dataset.role)).toEqual(["user", "assistant"]);
    expect(screen.getByTestId("chat-title")).toHaveTextContent("First chat");
  });

  it("says a foreign or unknown thread does not exist and shows none of its content", async () => {
    renderChat("someone-elses");
    expect(await screen.findByTestId("chat-not-found")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-message")).toBeNull();
    expect(screen.queryByTestId("chat-composer")).toBeNull();
  });

  it("a new chat is created through the API and opened", async () => {
    renderChat();
    fireEvent.click((await screen.findAllByTestId("chat-new"))[0]!);
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/chat/t-new"));
    expect(db.calls.some((c) => c.method === "POST" && c.path.endsWith("/chat/threads"))).toBe(true);
    expect((db.calls.find((c) => c.method === "POST" && c.path.endsWith("/chat/threads"))!.body as { agent_id: string }).agent_id).toBe(AGENT_ON);
  });

  it("tells the admin to open settings when the chat is off", async () => {
    db.enabled = false;
    renderChat();
    expect(await screen.findByTestId("chat-disabled")).toBeInTheDocument();
  });
});

describe("ChatPage — rename and delete", () => {
  it("renames through the API", async () => {
    renderChat("t1");
    const item = (await screen.findAllByTestId("chat-thread-item")).find((i) => i.dataset.threadId === "t1")!;
    fireEvent.click(within(item).getByTestId("chat-thread-menu"));
    fireEvent.click(await screen.findByText("Rename"));
    const input = await screen.findByTestId("chat-rename-input");
    fireEvent.change(input, { target: { value: "Renamed" } });
    fireEvent.click(screen.getByTestId("chat-rename-submit"));
    await waitFor(() => expect(screen.getByTestId("chat-title")).toHaveTextContent("Renamed"));
    expect(db.threads.find((t) => t.id === "t1")!.title).toBe("Renamed");
  });

  it("asks before deleting, then the chat is gone from the list and from the API", async () => {
    renderChat("t1");
    const item = (await screen.findAllByTestId("chat-thread-item")).find((i) => i.dataset.threadId === "t1")!;
    fireEvent.click(within(item).getByTestId("chat-thread-menu"));
    fireEvent.click(await screen.findByText("Delete"));
    // Nothing is deleted until the dialog is confirmed.
    expect(db.threads.some((t) => t.id === "t1")).toBe(true);
    fireEvent.click(await screen.findByRole("button", { name: /^delete$/i }));
    await waitFor(() => expect(screen.queryAllByTestId("chat-thread-item").map((i) => i.dataset.threadId)).toEqual(["t2"]));
    expect(db.threads.map((t) => t.id)).toEqual(["t2"]);
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(/\/chat$/));
  });
});

describe("ChatPage — composer", () => {
  it("Enter sends, Shift+Enter does not", async () => {
    renderChat("t1");
    const input = await screen.findByTestId("chat-input");
    fireEvent.change(input, { target: { value: "line one" } });
    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
    expect(db.calls.some((c) => c.method === "POST" && c.path.endsWith("/messages"))).toBe(false);
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(screen.getAllByTestId("chat-message")).toHaveLength(3));
    const post = db.calls.find((c) => c.method === "POST" && c.path.endsWith("/messages"))!;
    expect((post.body as { body: string }).body).toBe("line one");
    expect((input as HTMLTextAreaElement).value).toBe("");
  });

  it("a pasted image becomes a thumbnail, uploads to storage and is sent as an attachment id", async () => {
    const put = vi.fn().mockResolvedValue({ ok: true, status: 200 });
    vi.stubGlobal("fetch", put);
    vi.stubGlobal(
      "Image",
      class {
        naturalWidth = 640;
        naturalHeight = 480;
        onload: (() => void) | null = null;
        onerror: (() => void) | null = null;
        set src(_: string) {
          queueMicrotask(() => this.onload?.());
        }
      },
    );
    renderChat("t1");
    const input = await screen.findByTestId("chat-input");
    const file = new File([new Uint8Array([1, 2, 3])], "image.png", { type: "image/png" });
    fireEvent.paste(input, { clipboardData: { files: [file], items: [] } });

    const thumb = await screen.findByTestId("chat-pending-attachment");
    expect(within(thumb).getByRole("img")).toBeInTheDocument();
    await waitFor(() => expect(thumb.dataset.status).toBe("ready"));
    expect(put).toHaveBeenCalledWith("https://s3.example/put", expect.objectContaining({ method: "PUT", body: expect.any(File) }));
    const presign = db.calls.find((c) => c.path.endsWith("/chat/attachments"))!;
    expect(presign.body).toMatchObject({ thread_id: "t1", mime: "image/png", size: 3, width: 640, height: 480 });

    fireEvent.click(screen.getByTestId("chat-send"));
    await waitFor(() => expect(screen.getAllByTestId("chat-message")).toHaveLength(3));
    const post = db.calls.find((c) => c.method === "POST" && c.path.endsWith("/messages"))!;
    expect((post.body as { attachment_ids: string[] }).attachment_ids).toEqual(["att-1"]);
    expect(screen.queryByTestId("chat-pending-attachment")).toBeNull();
  });

  it("the cross removes a pending attachment before it is sent", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true }));
    renderChat("t1");
    await screen.findByTestId("chat-input");
    const file = new File(["abc"], "notes.txt", { type: "text/plain" });
    fireEvent.change(screen.getByTestId("chat-file-input"), { target: { files: [file] } });
    const chip = await screen.findByTestId("chat-pending-attachment");
    fireEvent.click(within(chip).getByRole("button", { name: /remove attachment notes\.txt/i }));
    expect(screen.queryByTestId("chat-pending-attachment")).toBeNull();
  });
});

describe("ChatPage — stream, stop, states", () => {
  it("streams the agent's reply and replaces it with the final message", async () => {
    renderChat("t1");
    await screen.findAllByTestId("chat-message");
    emit({ event: "chat.turn.state", turn: { id: "turn-1", state: "streaming" }, session_state: "working", error_code: null } as never);
    emit({ event: "chat.message.delta", message_id: "m3", turn_id: "turn-1", offset: 0, text: "Typing" } as never);
    emit({ event: "chat.message.delta", message_id: "m3", turn_id: "turn-1", offset: 6, text: " now" } as never);
    await waitFor(() => expect(screen.getAllByTestId("chat-message")).toHaveLength(3));
    expect(screen.getAllByTestId("chat-message")[2]).toHaveTextContent("Typing now");
    expect(screen.getByTestId("chat-session-state")).toHaveAttribute("data-state", "working");
    emit({
      event: "chat.message.completed",
      message: message({ id: "m3", body: "Typing now, done", created_at: "2026-10-24T11:00:00Z" }),
    } as never);
    emit({ event: "chat.turn.state", turn: { id: "turn-1", state: "done" }, session_state: "ready", error_code: null } as never);
    await waitFor(() => expect(screen.getAllByTestId("chat-message")[2]).toHaveTextContent("Typing now, done"));
    expect(screen.queryByTestId("chat-stop")).toBeNull();
  });

  it("does not mix in events of another chat", async () => {
    renderChat("t1");
    await screen.findAllByTestId("chat-message");
    emit({ event: "chat.message.delta", thread_id: "t2", message_id: "x", turn_id: "tt", offset: 0, text: "SECRET" } as never);
    emit({ event: "chat.message.delta", project_id: "other-project", message_id: "y", turn_id: "tt", offset: 0, text: "SECRET" } as never);
    expect(screen.getAllByTestId("chat-message")).toHaveLength(2);
    expect(screen.queryByText(/SECRET/)).toBeNull();
  });

  it("shows Stop during a turn, and Stop calls the API", async () => {
    db.activeTurn.t1 = { id: "turn-1", state: "streaming" };
    renderChat("t1");
    const stop = await screen.findByTestId("chat-stop");
    expect(screen.queryByTestId("chat-send")).toBeNull();
    fireEvent.click(stop);
    await waitFor(() => expect(db.calls.some((c) => c.method === "POST" && c.path.endsWith("/stop"))).toBe(true));
    await waitFor(() => expect(screen.queryByTestId("chat-stop")).toBeNull());
  });

  it("shows the place in the queue", async () => {
    db.activeTurn.t1 = { id: "turn-1", state: "queued", queue_position: 3 };
    renderChat("t1");
    expect(await screen.findByTestId("chat-queued")).toHaveTextContent("3");
  });

  it("says the agent is offline instead of a bare queue", async () => {
    db.activeTurn.t1 = { id: "turn-1", state: "queued", queue_position: 1, agent_offline: true };
    renderChat("t1");
    expect(await screen.findByTestId("chat-agent-offline")).toHaveTextContent("Vera");
  });

  it("shows a failed turn with its code and retries by resending the last message", async () => {
    renderChat("t1");
    await screen.findAllByTestId("chat-message");
    emit({ event: "chat.turn.state", turn: { id: "turn-1", state: "failed" }, session_state: "ready", error_code: "runner_lost" } as never);
    const err = await screen.findByTestId("chat-turn-error");
    expect(err).toHaveTextContent("runner_lost");
    fireEvent.click(screen.getByTestId("chat-retry"));
    await waitFor(() => expect(screen.queryByTestId("chat-turn-error")).toBeNull());
    const post = db.calls.find((c) => c.method === "POST" && c.path.endsWith("/messages"))!;
    expect((post.body as { body: string }).body).toBe("hello agent");
  });

  it("folds tool events into one working line", async () => {
    renderChat("t1");
    await screen.findAllByTestId("chat-message");
    emit({ event: "chat.turn.state", turn: { id: "turn-1", state: "streaming" }, session_state: "working", error_code: null } as never);
    emit({ event: "chat.turn.tool", turn_id: "turn-1", label: "Reading main.go" } as never);
    expect(await screen.findByTestId("chat-tools")).toHaveTextContent("Reading main.go");
  });

  it("keeps text typed while the send is still in flight", async () => {
    renderChat("t1");
    await screen.findAllByTestId("chat-message");
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const orig = mockedApi.getMockImplementation()!;
    mockedApi.mockImplementation(async (path, opts) => {
      if ((opts as { method?: string } | undefined)?.method === "POST" && String(path).endsWith("/messages")) await gate;
      return orig(path, opts as never);
    });
    const input = screen.getByTestId("chat-input") as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: "first" } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.change(input, { target: { value: "first\n  second" } });
    release();
    await waitFor(() => expect(input.value).toBe("\n  second"));
  });

  it("a send of the previous chat that resolves late leaves the draft of the open chat alone", async () => {
    renderChat("t1");
    await screen.findAllByTestId("chat-message");
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const orig = mockedApi.getMockImplementation()!;
    mockedApi.mockImplementation(async (path, opts) => {
      if ((opts as { method?: string } | undefined)?.method === "POST" && String(path).endsWith("/messages")) await gate;
      return orig(path, opts as never);
    });
    const first = screen.getByTestId("chat-input") as HTMLTextAreaElement;
    fireEvent.change(first, { target: { value: "to-a" } });
    fireEvent.keyDown(first, { key: "Enter" });
    const row = (await screen.findAllByTestId("chat-thread-item")).find((i) => i.dataset.threadId === "t2")!;
    fireEvent.click(within(row).getByRole("link"));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/chat/t2"));
    const second = screen.getByTestId("chat-input") as HTMLTextAreaElement;
    fireEvent.change(second, { target: { value: "to-a and more" } });
    await act(async () => {
      release();
      await gate;
    });
    expect((screen.getByTestId("chat-input") as HTMLTextAreaElement).value).toBe("to-a and more");
  });

  it("renders markdown in my own message too", async () => {
    db.messages.t1 = [message({ id: "m-md", role: "user", body: "run **now** `ls`" })];
    renderChat("t1");
    const mine = (await screen.findAllByTestId("chat-message")).find((m) => m.dataset.role === "user")!;
    expect(mine.querySelector("strong")).toHaveTextContent("now");
    expect(mine.querySelector("code")).toHaveTextContent("ls");
  });

  it("stays at the newest message when the feed grows after the first paint", async () => {
    let fire: () => void = () => {};
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: () => void) {
          fire = cb;
        }
        observe() {}
        disconnect() {}
      },
    );
    try {
      renderChat("t1");
      const feed = await screen.findByTestId("chat-feed");
      Object.defineProperty(feed, "scrollHeight", { configurable: true, value: 900 });
      feed.scrollTop = 0;
      act(() => fire());
      expect(feed.scrollTop).toBe(900);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("opens a fresh feed when the thread changes, so it starts at the newest message", async () => {
    renderChat("t1");
    const first = await screen.findByTestId("chat-feed");
    const row = (await screen.findAllByTestId("chat-thread-item")).find((i) => i.dataset.threadId === "t2")!;
    fireEvent.click(within(row).getByRole("link"));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/chat/t2"));
    await waitFor(() => expect(screen.getByTestId("chat-feed")).not.toBe(first));
  });

  it("a late 'older messages' page of the previous chat does not touch the open chat", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    mockedApi.mockImplementation(((path: string, opts: { params?: Record<string, unknown> } = {}) => {
      if (path.endsWith("/threads/t1/messages") && opts.params?.before) {
        return gate.then(() => ({
          items: [message({ id: "old-1", thread_id: "t1", body: "ancient", created_at: "2026-10-23T10:00:00Z" })],
          has_more: true,
          next_cursor: "cursor-from-t1",
          active_turn: null,
        }));
      }
      if (path.endsWith("/threads/t1/messages")) {
        return Promise.resolve({ items: [...db.messages.t1!].reverse(), has_more: true, next_cursor: "c1", active_turn: null });
      }
      return fakeApi(path, opts as never);
    }) as never);
    renderChat("t1");
    fireEvent.click(await screen.findByRole("button", { name: /older|ранее|earlier/i }));
    const row = (await screen.findAllByTestId("chat-thread-item")).find((i) => i.dataset.threadId === "t2")!;
    fireEvent.click(within(row).getByRole("link"));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/chat/t2"));
    await act(async () => {
      release();
      await gate;
    });
    expect(screen.queryByText("ancient")).toBeNull();
    // control: t2 has no further pages, and the t1 cursor did not leak into it
    expect(screen.queryByRole("button", { name: /older|ранее|earlier/i })).toBeNull();
  });

  it("a late 'show more' thread page of the previous project does not reach the new project's list", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    mockedApi.mockImplementation(((path: string, opts: { method?: string; params?: Record<string, unknown> } = {}) => {
      if (path === `/api/v1/projects/${P}/chat/threads` && opts.params?.cursor) {
        return gate.then(() => ({ items: [thread({ id: "leak", title: "Leaked thread" })], has_more: true, next_cursor: "cursor-from-p1" }));
      }
      if (path === `/api/v1/projects/${P}/chat/threads`) {
        return Promise.resolve({ items: db.threads, has_more: true, next_cursor: "c-p1" });
      }
      if (path.startsWith("/api/v1/projects/proj-2/chat/threads")) {
        return Promise.resolve({ items: [], has_more: false, next_cursor: null });
      }
      if (path.startsWith("/api/v1/projects/proj-2/chat/settings")) return Promise.resolve({ enabled: true, agents: [AGENT_ON] });
      if (path.startsWith("/api/v1/projects/proj-2/chat/agents")) {
        return Promise.resolve({ items: [{ agent_id: AGENT_ON, name: "Vera", color_token: "avatar-2", presence: "online", active_sessions: 0, queue_length: 0 }] });
      }
      return fakeApi(path, opts as never);
    }) as never);
    renderChat();
    fireEvent.click(await screen.findByRole("button", { name: /show more/i }));
    act(() => useProjectStore.setState({ currentProject: { id: "proj-2", slug: "demo", name: "Demo 2" } as never }));
    await waitFor(() => expect(mockedApi.mock.calls.some(([p]) => String(p).startsWith("/api/v1/projects/proj-2/chat/threads"))).toBe(true));
    await act(async () => {
      release();
      await gate;
    });
    expect(screen.queryByText("Leaked thread")).toBeNull();
    // control: the new project's list has no further pages, and the old cursor did not leak into it
    expect(screen.queryByRole("button", { name: /show more/i })).toBeNull();
  });

  it("a late Stop response of the previous chat does not change the open chat", async () => {
    db.activeTurn.t1 = { id: "turn-1", state: "running" };
    db.activeTurn.t2 = { id: "turn-2", state: "running" };
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    mockedApi.mockImplementation(((path: string, opts: { method?: string } = {}) => {
      if (path.endsWith("/threads/t1/stop")) return gate.then(() => ({ stopped: true, turn: { id: "turn-1", state: "cancelled" } }));
      if (path.endsWith("/threads/t2/stop")) return new Promise(() => {});
      return fakeApi(path, opts as never);
    }) as never);
    renderChat("t1");
    fireEvent.click(await screen.findByRole("button", { name: /stop|остановить/i }));
    const row = (await screen.findAllByTestId("chat-thread-item")).find((i) => i.dataset.threadId === "t2")!;
    fireEvent.click(within(row).getByRole("link"));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/chat/t2"));
    fireEvent.click(await screen.findByRole("button", { name: /stop|остановить/i }));
    await waitFor(() => expect(screen.getByRole("button", { name: /stop|остановить/i })).toBeDisabled());
    await act(async () => {
      release();
      await gate;
    });
    // t2's own Stop is still in flight: t1's late answer must not re-enable it
    expect(screen.getByRole("button", { name: /stop|остановить/i })).toBeDisabled();
  });

  it("a Stop still pending in the previous chat does not disable Stop in the open chat", async () => {
    db.activeTurn.t1 = { id: "turn-1", state: "running" };
    db.activeTurn.t2 = { id: "turn-2", state: "running" };
    mockedApi.mockImplementation(((path: string, opts: { method?: string } = {}) => {
      if (path.endsWith("/threads/t1/stop")) return new Promise(() => {});
      return fakeApi(path, opts as never);
    }) as never);
    renderChat("t1");
    fireEvent.click(await screen.findByRole("button", { name: /stop|остановить/i }));
    await waitFor(() => expect(screen.getByRole("button", { name: /stop|остановить/i })).toBeDisabled());
    const row = (await screen.findAllByTestId("chat-thread-item")).find((i) => i.dataset.threadId === "t2")!;
    fireEvent.click(within(row).getByRole("link"));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/chat/t2"));
    await waitFor(() => expect(screen.getByRole("button", { name: /stop|остановить/i })).toBeEnabled());
  });

  describe("responsive layout", () => {
    it("shows the tree alone on /chat and the thread alone on /chat/:id below md", async () => {
      const { unmount } = renderChat();
      const tree = await screen.findByRole("complementary");
      expect(tree.className).toContain("flex");
      expect(tree.className).not.toMatch(/(^| )hidden( |$)/);
      expect(screen.getByTestId("chat-title").closest("section")!.className).toMatch(/(^| )hidden( |$)/);
      unmount();

      renderChat("t1");
      await screen.findAllByTestId("chat-message");
      const aside = screen.getByRole("complementary", { hidden: true });
      expect(aside.className).toMatch(/(^| )hidden( |$)/);
      expect(screen.getByTestId("chat-title").closest("section")!.className).toMatch(/(^| )flex( |$)/);
    });

    it("never leaves /chat blank when the tree is collapsed: the expand button sits in a pane shown from md up", async () => {
      localStorage.setItem("mesh_chat_tree_collapsed", "1");
      renderChat();
      const btn = await screen.findByTestId("chat-collapse");
      expect(btn.closest("section")!.className).toContain("md:flex");
      // below md the tree is the whole screen and ignores the collapsed flag
      const aside = screen.getByRole("complementary", { hidden: true });
      expect(aside.className).toMatch(/(^| )flex( |$)/);
      expect(aside.className).toContain("md:hidden");
      localStorage.removeItem("mesh_chat_tree_collapsed");
    });

    it("collapses the tree, drops the divider and remembers it", async () => {
      localStorage.removeItem("mesh_chat_tree_collapsed");
      renderChat("t1");
      await screen.findAllByTestId("chat-message");
      expect(screen.getByRole("separator")).toBeInTheDocument();
      fireEvent.click(screen.getByTestId("chat-collapse"));
      expect(screen.queryByRole("separator")).toBeNull();
      expect(screen.getByRole("complementary", { hidden: true }).className).toContain("md:hidden");
      expect(localStorage.getItem("mesh_chat_tree_collapsed")).toBeTruthy();
      fireEvent.click(screen.getByTestId("chat-collapse"));
      expect(screen.getByRole("separator")).toBeInTheDocument();
    });
  });
});
