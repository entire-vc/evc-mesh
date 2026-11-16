import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router";
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { EventFeedWidget } from "../event-feed-widget";
import { activeFilterCount, feedParams, mergeEvents, DEFAULT_FILTERS, EVENT_TYPES, type FeedEvent } from "../use-events";
import { EVENT_FEED_SETTINGS_SCHEMA } from "../schema";
import { filtersFromSettings } from "../settings";
import { EventFeedWidgetView, ROW_REM } from "../view";
import { descriptors } from "..";
import { views } from "../views";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));

// The WebSocket is the external boundary: capture the handler the widget registers.
let socketHandler: ((e: { type: string; data: Record<string, unknown>; at: string }) => void) | null = null;
vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: ({ onEvent }: { onEvent: NonNullable<typeof socketHandler> }) => {
    socketHandler = onEvent;
    return { isConnected: true };
  },
}));

function ev(id: string, at: string, extra: Partial<FeedEvent> = {}): FeedEvent {
  return {
    id,
    workspace_id: "ws-1",
    project_id: "p-6",
    task_id: `task-${id}-0000`,
    agent_id: null,
    event_type: "status_change",
    subject: "task.moved",
    payload: { status: { old: "Todo", new: "In Progress" } },
    tags: ["auto", "task"],
    ttl: "86400 seconds",
    created_at: at,
    expires_at: null,
    task_title: `Task ${id}`,
    project_name: "Sixth project",
    actor_name: "Ada",
    actor_type: "agent",
    actor_id: "agent-1",
    ...extra,
  };
}

beforeEach(() => {
  socketHandler = null;
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "acme", name: "Acme" } as never });
  useProjectStore.setState({ projects: [{ id: "p-6", name: "Sixth project", slug: "six" }] as never });
});
afterEach(() => {
  cleanup();
  vi.mocked(api).mockReset();
});

describe("feed parameters", () => {
  it("sends several types, the actor and the project to the server", () => {
    const p = feedParams({ ...DEFAULT_FILTERS, eventTypes: ["summary", "error"], actorType: "system", projectId: "p-6" });
    expect(p).toMatchObject({ event_type: "summary,error", actor_type: "system", project_id: "p-6", limit: "50" });
    expect(feedParams(DEFAULT_FILTERS)).toEqual({ limit: "50" });
  });

  it("instance settings become the default filters", () => {
    const f = filtersFromSettings(
      { event_types: ["error", "bogus" as never], project_id: "p-6", actor_type: "user", period: { days: 7 } },
      new Date(2026, 10, 13, 12),
    );
    expect(f).toEqual({ eventTypes: ["error"], projectId: "p-6", actorType: "user", dateFrom: "2026-11-07", dateTo: "" });
  });

  it("merges by id, newest first", () => {
    const merged = mergeEvents([ev("b", "2026-11-13T10:00:02Z")], [ev("a", "2026-11-13T10:00:01Z"), ev("b", "2026-11-13T10:00:02Z")]);
    expect(merged.map((e) => e.id)).toEqual(["b", "a"]);
  });
});

describe("event-feed widget", () => {
  it("shows the history, then a socket event appears on top without a reload", async () => {
    const history = [ev("old", "2026-11-13T10:00:00Z")];
    vi.mocked(api).mockResolvedValue({ items: history, has_more: false, next_cursor: null });
    render(
      <MemoryRouter>
        <EventFeedWidget />
      </MemoryRouter>,
    );
    expect(await screen.findByText("Task old")).toBeTruthy();
    expect(vi.mocked(api)).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/events", { params: { limit: "50" } });
    const first = screen.getByText("Task old").closest("li")!;
    expect(first.textContent).toContain("Sixth project");
    expect(first.textContent).toContain("Todo");
    expect(first.textContent).toContain("In Progress");
    expect(first.textContent).toContain("Ada");
    expect(first.querySelector("a")?.getAttribute("href")).toContain("task-old");

    vi.mocked(api).mockResolvedValue({ items: [ev("new", "2026-11-13T10:05:00Z"), ...history], has_more: false, next_cursor: null });
    // A message that is not a bus event is ignored.
    act(() => socketHandler?.({ type: "task.updated", data: { id: "x" }, at: "" }));
    act(() => socketHandler?.({ type: "status_change", data: { id: "new", project_id: "p-6" }, at: "" }));
    const row = await screen.findByText("Task new", {}, { timeout: 2000 });
    const li = row.closest("li")!;
    expect(li.getAttribute("data-live")).toBe("true");
    const rows = Array.from(document.querySelectorAll("li[data-event-id]")).map((n) => n.getAttribute("data-event-id"));
    expect(rows).toEqual(["new", "old"]);
    expect(vi.mocked(api)).toHaveBeenCalledTimes(2);
  });

  it("a socket event that arrives while the first page loads is not lost: the head is re-read once it is ready", async () => {
    const history = [ev("old", "2026-11-13T10:00:00Z")];
    let resolveFirst: (v: unknown) => void = () => {};
    vi.mocked(api).mockImplementationOnce(() => new Promise((r) => (resolveFirst = r)) as never);
    render(
      <MemoryRouter>
        <EventFeedWidget />
      </MemoryRouter>,
    );
    await waitFor(() => expect(vi.mocked(api)).toHaveBeenCalledTimes(1));
    // The bus event lands while the first request is still in flight, and that page was read before it.
    act(() => socketHandler?.({ type: "status_change", data: { id: "new", project_id: "p-6" }, at: "" }));
    vi.mocked(api).mockResolvedValue({ items: [ev("new", "2026-11-13T10:05:00Z"), ...history], has_more: false, next_cursor: null });
    await act(async () => resolveFirst({ items: history, has_more: false, next_cursor: null }));
    const row = await screen.findByText("Task new", {}, { timeout: 2000 });
    expect(row.closest("li")!.getAttribute("data-live")).toBe("true");
    expect(vi.mocked(api)).toHaveBeenCalledTimes(2);
  });

  it("filters go to the server; empty with filters offers a reset; errors offer a retry", async () => {
    vi.mocked(api).mockResolvedValue({ items: [], has_more: false, next_cursor: null });
    render(
      <MemoryRouter>
        <EventFeedWidget />
      </MemoryRouter>,
    );
    await waitFor(() => expect(vi.mocked(api)).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: /eventFeed.types.summary|Summary|Итог/ }));
    fireEvent.click(screen.getByRole("button", { name: /eventFeed.types.error|Error|Ошибка/ }));
    fireEvent.click(screen.getByRole("button", { name: /eventFeed.actors.system|System|Система/ }));
    await waitFor(() =>
      expect(vi.mocked(api)).toHaveBeenLastCalledWith("/api/v1/workspaces/ws-1/events", {
        params: { limit: "50", event_type: "summary,error", actor_type: "system" },
      }),
    );

    vi.mocked(api).mockRejectedValueOnce(new Error("boom"));
    fireEvent.click(screen.getByRole("button", { name: /eventFeed.types.custom|Custom|Прочее/ }));
    expect(await screen.findByRole("alert")).toBeTruthy();
  });

  it("on the dashboard: instance settings set the opening filters; the widget has no link to the retired Events page", async () => {
    vi.mocked(api).mockResolvedValue({ items: [], has_more: false, next_cursor: null });
    expect(descriptors.map((d) => d.id)).toEqual(["event-feed"]);
    expect(Object.keys(descriptors[0]!.settings!.properties)).toEqual(
      expect.arrayContaining(["event_types", "project_id", "actor_type", "period", "auto_scroll", "auto_refresh"]),
    );
    expect(views["event-feed"]).toBe(EventFeedWidgetView);
    // The settings schema (registry chunk) spells the event types out; they must stay the widget's list.
    expect(EVENT_FEED_SETTINGS_SCHEMA.properties.event_types.items.enum).toEqual(EVENT_TYPES);
    const instance = { id: "i-1", type: "event-feed", x: 0, y: 0, w: 12, h: 4, settings: {} };
    render(
      <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
        <Routes>
          <Route
            path="/w/:wsSlug/dashboard"
            element={
              <EventFeedWidgetView
                instance={instance}
                settings={{ event_types: ["error"], actor_type: "system" }}
                size={{ w: 12, h: 4 }}
                actions={{}}
              />
            }
          />
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() =>
      expect(vi.mocked(api)).toHaveBeenLastCalledWith("/api/v1/workspaces/ws-1/events", {
        params: { limit: "50", event_type: "error", actor_type: "system" },
      }),
    );
    // «Развернуть» is the dashboard cell's (?w=<instance>), not a link of the widget.
    expect(screen.queryByRole("link", { name: /eventFeed.expand|Expand|Развернуть/ })).toBeNull();
  });

  it("on a dashboard the feed is a titled card as tall as its instance, so its rows scroll inside it", async () => {
    vi.mocked(api).mockResolvedValue({ items: [], has_more: false, next_cursor: null });
    const instance = { id: "i-2", type: "event-feed", x: 0, y: 0, w: 6, h: 3, settings: {} };
    render(
      <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
        <Routes>
          <Route path="/w/:wsSlug/dashboard" element={<EventFeedWidgetView instance={instance} settings={{}} size={{ w: 6, h: 3 }} actions={{}} />} />
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() => expect(vi.mocked(api)).toHaveBeenCalled());
    const card = screen.getByTestId("event-feed-card");
    expect(card.style.height).toBe(`${3 * ROW_REM}rem`);
    expect(screen.getByRole("heading", { name: /eventFeed.title|Event feed|Лента событий/ })).toBeTruthy();
    expect(card.querySelector('[data-widget="event-feed"]')).not.toBeNull();
    // The Events page draws the bare feed: no card of a fixed height around it.
    cleanup();
    render(
      <MemoryRouter>
        <EventFeedWidget expanded />
      </MemoryRouter>,
    );
    expect(screen.queryByTestId("event-feed-card")).toBeNull();
  });

  it("on the dashboard the filter panel starts closed behind «Фильтры» with the active count; the Events page keeps it open", async () => {
    vi.mocked(api).mockResolvedValue({ items: [ev("a", "2026-11-13T10:00:00Z")], has_more: false, next_cursor: null });
    const instance = { id: "i-3", type: "event-feed", x: 0, y: 0, w: 12, h: 4, settings: {} };
    render(
      <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
        <Routes>
          <Route
            path="/w/:wsSlug/dashboard"
            element={
              <EventFeedWidgetView
                instance={instance}
                settings={{ event_types: ["error", "summary"], actor_type: "system" }}
                size={{ w: 12, h: 4 }}
                actions={{}}
              />
            }
          />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText("Task a")).toBeTruthy();
    // Closed: no panel, the button carries the count of filters set by the instance (types + actor).
    const toggle = screen.getByRole("button", { name: /eventFeed.filters|Filters|Фильтры/ });
    const panelOf = () => document.getElementById(toggle.getAttribute("aria-controls") ?? "-");
    const count = () => toggle.querySelector("[aria-label]")?.textContent;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(panelOf()).toBeNull();
    expect(count()).toBe("2");

    // Open: the panel is inside the card, «Сбросить» stays in it; a user's choice moves the count.
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    const panel = panelOf()!;
    expect(screen.getByTestId("event-feed-card").contains(panel)).toBe(true);
    expect(panel.querySelector('[role="group"]')).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /eventFeed.actors.all|All actors|Все исполнители/ }));
    await waitFor(() => expect(count()).toBe("1"));
    const reset = screen.getByRole("button", { name: /eventFeed.reset|^Reset$|^Сбросить$/ });
    expect(panel.contains(reset)).toBe(true);
    fireEvent.click(reset);
    await waitFor(() => expect(count()).toBe("2"));

    // Closed again: the panel goes away.
    fireEvent.click(toggle);
    expect(panelOf()).toBeNull();

    // The Events page («Развернуть»): the panel is open and there is no toggle.
    cleanup();
    render(
      <MemoryRouter>
        <EventFeedWidget expanded />
      </MemoryRouter>,
    );
    expect(await screen.findByText("Task a")).toBeTruthy();
    expect(screen.getByRole("button", { name: /eventFeed.types.error|Error|Ошибка/ })).toBeTruthy();
    expect(document.querySelector("[aria-expanded]")).toBeNull();
  });

  it("counts each filter away from the defaults once", () => {
    expect(activeFilterCount(DEFAULT_FILTERS)).toBe(0);
    expect(activeFilterCount({ ...DEFAULT_FILTERS, eventTypes: ["error", "summary"] })).toBe(1);
    expect(
      activeFilterCount({ eventTypes: ["error"], projectId: "p-6", actorType: "agent", dateFrom: "2026-11-01", dateTo: "2026-11-02" }),
    ).toBe(4);
  });

});
