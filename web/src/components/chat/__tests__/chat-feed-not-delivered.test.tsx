import { afterEach, describe, expect, it } from "vitest";
import { act, render, screen } from "@testing-library/react";
import i18n, { setLanguage } from "@/i18n";
import { MemoryRouter } from "react-router";
import { ChatFeed, type ChatFeedProps } from "@/components/chat/chat-feed";
import type { ChatMessage } from "@/lib/chat/types";

// An expired turn (S07: nobody took the message within the offline TTL) leaves the user
// message failed with error_code not_delivered. After a reload there is no live turn error
// banner, so the bubble itself has to say the message was not delivered, not just "failed".

const base: ChatMessage = {
  id: "m1",
  thread_id: "t1",
  role: "user",
  body: "hello",
  status: "failed",
  truncated: false,
  turn_id: "turn1",
  error_code: "not_delivered",
  client_message_id: null,
  attachments: [],
  created_at: "2026-10-29T10:00:00Z",
};

const props = (m: ChatMessage): ChatFeedProps => ({
  projectId: "p1",
  messages: [m],
  tools: [],
  activeTurn: null,
  error: null,
  agentName: "Agent",
  hasMore: false,
  loadingMore: false,
  loading: false,
  loadError: null,
  online: false,
  onLoadMore: () => {},
  onRetryLoad: () => {},
  onRetryTurn: () => {},
  language: "en",
});

const view = (m: ChatMessage) =>
  render(
    <MemoryRouter>
      <ChatFeed {...props(m)} />
    </MemoryRouter>,
  );

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("chat user message the agent never picked up", () => {
  it("says not delivered on the bubble, not the generic failure", () => {
    view(base);
    const mark = screen.getByTestId("chat-not-delivered");
    expect(mark).toHaveTextContent("not delivered");
    expect(screen.queryByText("failed", { exact: true })).toBeNull();
  });

  it("says не доставлено in Russian", async () => {
    await act(async () => {
      await setLanguage("ru");
    });
    view(base);
    expect(screen.getByTestId("chat-not-delivered")).toHaveTextContent("не доставлено");
    expect(screen.queryByText("ошибка", { exact: true })).toBeNull();
  });

  it("keeps the generic failure for any other error", () => {
    view({ ...base, error_code: "runner_error" });
    expect(screen.queryByTestId("chat-not-delivered")).toBeNull();
    expect(screen.getByText("failed", { exact: true })).toBeInTheDocument();
  });
});
