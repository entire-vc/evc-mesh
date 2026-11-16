import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useWebSocketStore } from "@/stores/websocket";
import { useAuthStore } from "@/stores/auth";
import { Widget } from "../../..";
import { useUnseenMentionCount } from "../hooks";

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: () => null }));
import { api } from "@/lib/api";

const mention = (id: string, over: object = {}) => ({
  comment_id: id, task_id: `t-${id}`, task_title: `Task of ${id}`, extracted_at: "2026-01-01T00:00:00Z", seen_at: null,
  project_id: "p1", comment_body: "hello", author_id: "u1", author_name: "Ann", ...over,
});
const comment = (n: number, over: object = {}) => ({
  comment_id: `c${n}`, task_id: `t${n}`, task_title: `Commented ${n}`, project_id: "p1", project_name: "Core", comment_body: `Body ${n}`,
  author_id: "u1", author_name: "Ann", author_kind: "user", is_internal: false, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", ...over,
});

let taskMentions: object[] = [];
let unseen = 0;
function serve(over: Partial<{ docDown: boolean; recent: (params?: Record<string, unknown>) => object; mine: object[] }> = {}) {
  vi.mocked(api).mockImplementation(async (url: string, opts?: { params?: Record<string, unknown>; method?: string }) => {
    if (opts?.method === "POST" && url.endsWith("/seen")) { unseen = Math.max(0, unseen - 1); return {}; }
    if (url === "/api/v1/me/mentions") return taskMentions;
    if (url === "/api/v1/me/document-mentions") { if (over.docDown) throw new Error("down"); return [{ ...mention("d1"), document_id: "doc1", document_slug: "doc", document_title: "Doc one" }]; }
    if (url.endsWith("/unseen_count")) return { count: url.includes("document") ? 0 : unseen };
    if (url === "/api/v1/me/comments") return { items: over.mine ?? [comment(1)], next_cursor: null, next_cursor_id: null };
    if (url.endsWith("/comments/recent")) return (over.recent ?? (() => ({ items: [comment(2)], next_cursor: null, next_cursor_id: null })))(opts?.params);
    return {};
  });
}

function Where() {
  const l = useLocation();
  return <output data-testid="where">{l.pathname}{l.search}</output>;
}
function Count() {
  const r = useUnseenMentionCount();
  return <output data-testid="unseen">{r.state === "ready" ? r.value : r.state}</output>;
}
function mount(id: string, config?: Record<string, unknown>) {
  window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {} })) as never;
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useProjectStore.setState({ projects: [{ id: "p1", slug: "core", name: "Core" } as never, { id: "p2", slug: "ops", name: "Ops" } as never] });
  return render(
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <Routes>
        <Route path="/w/:wsSlug/dashboard" element={<><Widget instance={{ id, type: id, x: 0, y: 0, w: 6, h: 2, settings: config ?? {} } as never} /><Count /></>} />
        <Route path="*" element={<Count />} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 20)); });

beforeEach(() => { localStorage.clear(); taskMentions = [mention("c1")]; unseen = 2; serve(); useAuthStore.setState({ user: { id: "me" } as never }); });
afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe("mentions widget", () => {
  it("shows task and document mentions, a failed source is named, not 'no mentions'", async () => {
    serve({ docDown: true });
    mount("mentions");
    await settle();
    expect(screen.getByText("Task of c1")).toBeTruthy();
    expect(screen.getByTestId("mentions-load-failed").textContent).toMatch(/document|документ/i);
  });

  it("a mention from another window appears without reload (WS mention.* on the personal channel)", async () => {
    const subscribe = vi.spyOn(useWebSocketStore.getState(), "subscribe");
    mount("mentions");
    await settle();
    // the server publishes mentions only to ws:user:<id>; nobody else subscribes it here
    expect(subscribe).toHaveBeenCalledWith("ws:user:me");
    expect(screen.queryByText("Task of c2")).toBeNull();
    expect(screen.getByTestId("unseen").textContent).toBe("2");
    taskMentions = [mention("c1"), mention("c2", { extracted_at: "2026-02-01T00:00:00Z" })];
    unseen = 3;
    // the frame as the hub sends it: event_type is absent, so `type` is empty and the name is in data.event
    const frame = { type: "", channel: "ws:user:me", data: { event: "mention.badge", workspace_id: "ws1", task_id: "t-c2", comment_id: "c2" }, timestamp: "1" };
    act(() => useWebSocketStore.setState({ lastEvent: frame as never }));
    await waitFor(() => expect(screen.getByText("Task of c2")).toBeTruthy());
    await waitFor(() => expect(screen.getByTestId("unseen").textContent).toBe("3"));
    subscribe.mockRestore();
  });

  it("unmounting the widget keeps the personal channel: the sidebar badge and the bell share it", async () => {
    const unsubscribe = vi.spyOn(useWebSocketStore.getState(), "unsubscribe");
    const view = mount("mentions");
    await settle();
    view.unmount();
    expect(unsubscribe).not.toHaveBeenCalledWith("ws:user:me");
    unsubscribe.mockRestore();
  });

  it("a mention of another workspace (same personal channel) does not touch this one", async () => {
    mount("mentions");
    await settle();
    taskMentions = [mention("c1"), mention("c2")];
    unseen = 5;
    const frame = { type: "", channel: "ws:user:me", data: { event: "mention.badge", workspace_id: "ws-other" }, timestamp: "1" };
    act(() => useWebSocketStore.setState({ lastEvent: frame as never }));
    await settle();
    expect(screen.queryByText("Task of c2")).toBeNull();
    expect(screen.getByTestId("unseen").textContent).toBe("2");
  });

  it("click marks the mention seen on the server, the counter goes down, the row opens its target", async () => {
    mount("mentions");
    await settle();
    expect(screen.getByTestId("unseen").textContent).toBe("2");
    fireEvent.click(screen.getByText("Task of c1"));
    expect(vi.mocked(api)).toHaveBeenCalledWith("/api/v1/me/mentions/c1/seen", { method: "POST" });
    await waitFor(() => expect(screen.getByTestId("unseen").textContent).toBe("1"));
    expect(screen.getByTestId("where").textContent).toBe("/t/t-c1?comment=c1");
  });

  it("only-new hides seen mentions; ❓ is marked; the shown-state survives broken localStorage", async () => {
    taskMentions = [mention("c1", { comment_body: "is it done ❓" }), mention("c3", { seen_at: "2026-01-02T00:00:00Z" })];
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
    mount("mentions");
    await settle();
    expect(screen.getByText("Task of c3")).toBeTruthy();
    expect(screen.getByLabelText(/question|вопрос/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("checkbox"));
    expect(screen.queryByText("Task of c3")).toBeNull();
    vi.restoreAllMocks();
  });

  it("the limit setting caps the rows", async () => {
    taskMentions = Array.from({ length: 8 }, (_, i) => mention(`m${i}`, { task_title: `Row ${i}`, extracted_at: `2026-01-0${i + 1}T00:00:00Z` }));
    mount("mentions", { limit: 5 });
    await settle();
    expect(screen.getAllByTestId("mention-card-task")).toHaveLength(5);
  });
});

describe("my-comments widget", () => {
  it("lists my comments, asks the server for this workspace, «Ещё» loads the next page by cursor + id", async () => {
    const calls: Record<string, unknown>[] = [];
    vi.mocked(api).mockImplementation(async (url: string, opts?: { params?: Record<string, unknown> }) => {
      if (url !== "/api/v1/me/comments") return {};
      calls.push(opts?.params ?? {});
      return opts?.params?.before
        ? { items: [comment(3, { task_title: "Second page" })], next_cursor: null, next_cursor_id: null }
        : { items: [comment(1)], next_cursor: "2026-01-01T00:00:00Z", next_cursor_id: "c1" };
    });
    mount("my-comments", { limit: 5 });
    await settle();
    expect(screen.getByText("Commented 1")).toBeTruthy();
    // the workspace is asked of the server, on the first page and on the next one, even with no projects loaded
    expect(calls.every((c) => c.workspace_id === "ws1")).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: /load more|ещё/i }));
    await waitFor(() => expect(screen.getByText("Second page")).toBeTruthy());
    expect(calls[1]).toMatchObject({ before: "2026-01-01T00:00:00Z", before_id: "c1" });
    expect(screen.queryByRole("button", { name: /load more|ещё/i })).toBeNull();
  });

  it("empty and error are different; retry reloads", async () => {
    serve({ mine: [] });
    mount("my-comments");
    await settle();
    expect(screen.getByText(/haven't commented|не комментировали/i)).toBeTruthy();
    cleanup();
    vi.mocked(api).mockRejectedValue(new Error("boom"));
    mount("my-comments");
    await settle();
    expect(screen.getByRole("alert").textContent).toContain("boom");
    serve();
    fireEvent.click(screen.getByRole("button", { name: /retry|повтор/i }));
    await waitFor(() => expect(screen.getByText("Commented 1")).toBeTruthy());
  });

  it("the project select narrows the list", async () => {
    serve({ mine: [comment(1), comment(4, { project_id: "p2", task_title: "Ops thing" })] });
    mount("my-comments");
    await settle();
    expect(screen.getByText("Ops thing")).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/project|проект/i), { target: { value: "p1" } });
    expect(screen.queryByText("Ops thing")).toBeNull();
    expect(screen.getByText("Commented 1")).toBeTruthy();
  });
});

describe("recent-comments widget", () => {
  it("shows the author, refreshes on comment.created, shows internal notes only when asked", async () => {
    let list = [comment(2), comment(5, { is_internal: true, task_title: "Secret note" })];
    serve({ recent: () => ({ items: list, next_cursor: null, next_cursor_id: null }) });
    mount("recent-comments");
    await settle();
    expect(screen.getByText(/Ann/)).toBeTruthy();
    expect(screen.queryByText("Secret note")).toBeNull(); // off by default
    fireEvent.click(screen.getByRole("checkbox")); // the toggle exists because the server returned internal notes
    expect(screen.getByText("Secret note")).toBeTruthy();
    list = [comment(6, { task_title: "Fresh one" }), ...list];
    act(() => useWebSocketStore.setState({ lastEvent: { type: "comment.created", data: {}, timestamp: "2" } as never }));
    await waitFor(() => expect(screen.getByText("Fresh one")).toBeTruthy());
  });
});

describe("filters owned by the view (review R1-R3)", () => {
  it("R1: recent-comments asks the server for internal notes (the toggle can only hide them again)", async () => {
    const seen: Record<string, unknown>[] = [];
    serve({ recent: (p) => { seen.push(p ?? {}); return { items: [comment(2), comment(5, { is_internal: true, task_title: "Secret note" })], next_cursor: null, next_cursor_id: null }; } });
    mount("recent-comments");
    await settle();
    expect(seen.length).toBeGreaterThan(0);
    expect(seen.every((p) => String(p.include_internal) === "true")).toBe(true);
  });

  it("R2: config onlyNew=true starts filtered, unchecking brings the seen rows back", async () => {
    taskMentions = [mention("c1", { seen_at: "2026-01-02T00:00:00Z", task_title: "Already seen" }), mention("c2", { task_title: "Brand new" })];
    mount("mentions", { onlyNew: true });
    await settle();
    expect(screen.getByText("Brand new")).toBeTruthy();
    expect(screen.queryByText("Already seen")).toBeNull();
    fireEvent.click(screen.getByRole("checkbox"));
    expect(screen.getByText("Already seen")).toBeTruthy();
  });

  it("R3: config projectId=p1 starts filtered, choosing another project shows its comments", async () => {
    serve({ recent: () => ({ items: [comment(2, { task_title: "In core" }), comment(3, { project_id: "p2", task_title: "In ops" })], next_cursor: null, next_cursor_id: null }) });
    mount("recent-comments", { projectId: "p1" });
    await settle();
    expect(screen.getByText("In core")).toBeTruthy();
    expect(screen.queryByText("In ops")).toBeNull();
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "p2" } });
    expect(screen.getByText("In ops")).toBeTruthy();
  });
});
