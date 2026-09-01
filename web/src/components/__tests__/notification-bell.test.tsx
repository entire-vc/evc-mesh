/**
 * The bell and the Mentions tab must not contradict each other.
 *
 * The regression: `/api/v1/notifications` (the bell's data source) only ever
 * gets a row for a recipient with an *enabled* `web_push` preference for that
 * event type (notification_service.go dispatch()) — nobody is auto-subscribed
 * to it, so most accounts see a permanently empty bell. `/me/mentions` and
 * `/me/document-mentions` are written unconditionally whenever a comment
 * mentions someone, so the Mentions tab (merged since #639) shows the mention
 * regardless. These tests pin: the bell now shows an unseen mention even when
 * the generic notifications endpoint is empty; it does not double-count a
 * mention that also happens to exist as a generic notification; clicking a
 * mention row marks it seen on its own endpoint and navigates; and a failed
 * mention fetch does not get rendered as "no notifications" (the same
 * "unanswered source is not an empty inbox" principle `lib/mentions/inbox.ts`
 * already enforces for the Mentions tab).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: vi.fn(() => null) }));

import { api } from "@/lib/api";
import { NotificationBell } from "@/components/notification-bell";
import { useAuthStore } from "@/stores/auth";
import { useNotificationStore } from "@/stores/notification";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { DocumentMention, Mention, Notification, Project, User } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const NOTIFICATIONS_PATH = "/api/v1/notifications";
const TASK_MENTIONS_PATH = "/api/v1/me/mentions";
const DOC_MENTIONS_PATH = "/api/v1/me/document-mentions";

const PROJECT: Project = {
  id: "proj-1",
  workspace_id: "ws-1",
  name: "Demo",
  description: "",
  slug: "demo",
  icon: "",
  settings: {},
  default_assignee_type: "none",
  is_archived: false,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

const USER: User = {
  id: "u1",
  email: "hugh@entire.vc",
  name: "Hugh",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const TASK_MENTION: Mention = {
  comment_id: "c-task-1",
  mentioned_id: "u1",
  mentioned_kind: "user",
  mentioned_slug: "hugh",
  extracted_at: "2026-08-31T10:00:00Z",
  seen_at: null,
  task_id: "task-1",
  task_title: "[probe] Коллизия слагов",
  project_id: "proj-1",
  comment_body: "Человек с коллизией: @hugh",
  author_id: "a-1",
  author_name: "Ann Author",
};

const DOC_MENTION: DocumentMention = {
  comment_id: "c-doc-1",
  mentioned_id: "u1",
  mentioned_kind: "user",
  mentioned_slug: "hugh",
  extracted_at: "2026-08-31T11:00:00Z",
  seen_at: null,
  document_id: "doc-1",
  document_title: "Rollback Plan",
  document_slug: "rollback-plan",
  project_id: "proj-1",
  comment_body: "@hugh this section",
  author_id: "a-1",
  author_name: "Ann Author",
};

/** Answers /api/v1/notifications, both mention inboxes, and any *_/seen or mark-read POST. */
function mockBackend(opts: {
  notifications?: Notification[];
  tasks?: Mention[] | Error;
  docs?: DocumentMention[] | Error;
}) {
  const { notifications = [], tasks = [], docs = [] } = opts;
  mockedApi.mockImplementation((path: string) => {
    if (path === NOTIFICATIONS_PATH) {
      return Promise.resolve({
        items: notifications,
        unread_count: notifications.filter((n) => !n.is_read).length,
      });
    }
    if (path === TASK_MENTIONS_PATH) {
      return tasks instanceof Error ? Promise.reject(tasks) : Promise.resolve(tasks);
    }
    if (path === DOC_MENTIONS_PATH) {
      return docs instanceof Error ? Promise.reject(docs) : Promise.resolve(docs);
    }
    if (path === `${TASK_MENTIONS_PATH}/unseen_count`) {
      const count = tasks instanceof Error ? 0 : tasks.filter((m) => !m.seen_at).length;
      return tasks instanceof Error ? Promise.reject(tasks) : Promise.resolve({ count });
    }
    if (path === `${DOC_MENTIONS_PATH}/unseen_count`) {
      const count = docs instanceof Error ? 0 : docs.filter((m) => !m.seen_at).length;
      return docs instanceof Error ? Promise.reject(docs) : Promise.resolve({ count });
    }
    if (path.endsWith("/seen") || path === "/api/v1/notifications/mark-read") {
      return Promise.resolve(undefined);
    }
    return Promise.reject(new Error(`unexpected path ${path}`));
  });
}

function renderBell() {
  return render(
    <MemoryRouter>
      <NotificationBell />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockedNavigate.mockReset();
  mockedApi.mockReset();
  useNotificationStore.setState({ notifications: [], unreadCount: 0, pollingHandle: null });
  useProjectStore.setState({ projects: [PROJECT] });
  useAuthStore.setState({ user: USER, isAuthenticated: true, isLoading: false });
  useWorkspaceStore.setState({
    currentWorkspace: {
      id: "ws-1",
      name: "Acme",
      slug: "acme",
      owner_id: "u1",
      billing_plan_id: null,
      billing_customer_id: null,
      icon_url: null,
      settings: {},
      created_at: "2026-08-01T00:00:00Z",
      updated_at: "2026-08-01T00:00:00Z",
    } as never,
  });
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("NotificationBell — merged @-mention inbox", () => {
  it("shows an unseen mention even though /api/v1/notifications is empty (root regression)", async () => {
    mockBackend({ notifications: [], tasks: [TASK_MENTION], docs: [] });
    renderBell();

    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));

    expect(await screen.findByText(/Коллизия слагов/)).toBeInTheDocument();
    expect(screen.queryByText("No notifications")).not.toBeInTheDocument();
  });

  it("badges the icon with the unseen mention count before the dropdown is ever opened", async () => {
    mockBackend({ notifications: [], tasks: [TASK_MENTION], docs: [DOC_MENTION] });
    renderBell();

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /2 unread/ })).toBeInTheDocument(),
    );
  });

  it("shows a document mention too, even though document.mentioned never reaches /notifications", async () => {
    mockBackend({ notifications: [], tasks: [], docs: [DOC_MENTION] });
    renderBell();

    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));

    expect(await screen.findByText(/Rollback Plan/)).toBeInTheDocument();
  });

  it("does not double-render a task.mentioned notification also present as a mention (dedup)", async () => {
    // The account this simulates HAS a web_push preference for task.mentioned,
    // so the generic endpoint carries the same comment the mention inbox does.
    const dup: Notification = {
      id: "n-dup",
      workspace_id: "ws-1",
      user_id: "u1",
      event_type: "task.mentioned",
      title: "Ann mentioned you on: [probe] Коллизия слагов",
      body: "Человек с коллизией: @hugh",
      metadata: { task_id: "task-1", comment_id: "c-task-1" },
      is_read: false,
      created_at: "2026-08-31T10:00:00Z",
    };
    mockBackend({ notifications: [dup], tasks: [TASK_MENTION], docs: [] });
    renderBell();

    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    await screen.findByText(/Коллизия слагов/);

    expect(screen.getAllByText(/Коллизия слагов/)).toHaveLength(1);
  });

  it("marks a clicked mention seen on its own endpoint and navigates to the task", async () => {
    mockBackend({ notifications: [], tasks: [TASK_MENTION], docs: [] });
    renderBell();

    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    fireEvent.click(await screen.findByText(/Коллизия слагов/));

    await waitFor(() =>
      expect(mockedApi).toHaveBeenCalledWith(`${TASK_MENTIONS_PATH}/c-task-1/seen`, {
        method: "POST",
      }),
    );
    expect(mockedNavigate).toHaveBeenCalledWith("/w/acme/p/demo/t/task-1");
  });

  it("marks a clicked document mention seen on the document endpoint and opens the comment", async () => {
    mockBackend({ notifications: [], tasks: [], docs: [DOC_MENTION] });
    renderBell();

    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    fireEvent.click(await screen.findByText(/Rollback Plan/));

    await waitFor(() =>
      expect(mockedApi).toHaveBeenCalledWith(`${DOC_MENTIONS_PATH}/c-doc-1/seen`, {
        method: "POST",
      }),
    );
    expect(mockedNavigate).toHaveBeenCalledWith(
      "/w/acme/p/demo/docs/doc-1?comment=c-doc-1",
    );
  });

  it("negative control: a real empty inbox still shows the reassuring empty state", async () => {
    mockBackend({ notifications: [], tasks: [], docs: [] });
    renderBell();

    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));

    expect(await screen.findByText("No notifications")).toBeInTheDocument();
  });

  it("does not claim 'No notifications' when a mention source failed to answer", async () => {
    mockBackend({ notifications: [], tasks: new Error("network"), docs: [] });
    renderBell();

    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));

    await screen.findByText(/could not be loaded/);
    expect(screen.queryByText("No notifications")).not.toBeInTheDocument();
  });
});
