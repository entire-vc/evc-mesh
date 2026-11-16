// The mentions hooks against the real WebSocket store: a workspace switch (AppLayout calls connect(B), which
// disconnects A and clears the store's channel list) must not leave the personal channel unsubscribed.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

vi.mock("@/lib/api", () => ({ api: vi.fn(async () => ({ count: 0 })), getAccessToken: () => "token" }));
import { useWebSocketStore } from "@/stores/websocket";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useUnseenMentionCount } from "../hooks";

class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  sent: { action: string; channel: string }[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) { FakeSocket.all.push(this); }
  send(raw: string) { this.sent.push(JSON.parse(raw)); }
  // A real socket reports its close later, on its own task: finishClose() is that moment.
  close() { this.readyState = 2; }
  finishClose() { this.readyState = 3; this.onclose?.(); }
  open() { this.readyState = FakeSocket.OPEN; this.onopen?.(); }
}

beforeEach(() => {
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  useAuthStore.setState({ user: { id: "me" } as never });
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "a", name: "A" } as never });
});
afterEach(() => { useWebSocketStore.getState().disconnect(); vi.unstubAllGlobals(); });

describe("mentions over a workspace switch", () => {
  it("the personal channel is subscribed again on the new socket", async () => {
    act(() => useWebSocketStore.getState().connect("a"));
    act(() => FakeSocket.all[0]!.open());
    renderHook(() => useUnseenMentionCount());
    act(() => useWebSocketStore.getState().subscribe("project:p-of-a"));
    expect(FakeSocket.all[0]!.sent).toContainEqual({ action: "subscribe", channel: "ws:user:me" });

    // what AppLayout does on a workspace switch
    act(() => {
      useWorkspaceStore.setState({ currentWorkspace: { id: "ws2", slug: "b", name: "B" } as never });
      useWebSocketStore.getState().connect("b");
    });
    act(() => FakeSocket.all[1]!.open());
    expect(FakeSocket.all[1]!.url).toContain("workspace=b");
    expect(FakeSocket.all[1]!.sent).toContainEqual({ action: "subscribe", channel: "ws:user:me" });
    // a workspace's own channels stay behind with it
    expect(FakeSocket.all[1]!.sent).not.toContainEqual({ action: "subscribe", channel: "project:p-of-a" });
  });

  function switchToB() {
    act(() => useWebSocketStore.getState().connect("a"));
    act(() => FakeSocket.all[0]!.open());
    renderHook(() => useUnseenMentionCount());
    act(() => {
      useWorkspaceStore.setState({ currentWorkspace: { id: "ws2", slug: "b", name: "B" } as never });
      useWebSocketStore.getState().connect("b");
    });
    return [FakeSocket.all[0]!, FakeSocket.all[1]!] as const;
  }

  it("the old socket's late close before the new one opens does not cost the subscription", () => {
    const [a, b] = switchToB();
    act(() => a.finishClose());
    act(() => b.open());
    expect(b.sent).toContainEqual({ action: "subscribe", channel: "ws:user:me" });
    expect(useWebSocketStore.getState().isConnected).toBe(true);
    // the store still holds B: a later subscribe reaches it
    act(() => useWebSocketStore.getState().subscribe("project:p-of-b"));
    expect(b.sent).toContainEqual({ action: "subscribe", channel: "project:p-of-b" });
  });

  it("the old socket's late close after the new one opened leaves the store connected to it", () => {
    const [a, b] = switchToB();
    act(() => b.open());
    act(() => a.finishClose());
    expect(b.sent).toContainEqual({ action: "subscribe", channel: "ws:user:me" });
    expect(useWebSocketStore.getState().isConnected).toBe(true);
    act(() => useWebSocketStore.getState().subscribe("project:p-of-b"));
    expect(b.sent).toContainEqual({ action: "subscribe", channel: "project:p-of-b" });
    // no reconnect was scheduled for the stale socket
    expect(FakeSocket.all).toHaveLength(2);
  });
});
