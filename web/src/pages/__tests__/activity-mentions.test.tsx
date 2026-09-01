/**
 * The Mentions tab must not contradict the bell.
 *
 * The defect: the bell delivered two document mentions while this tab, on the
 * same screen, said "No mentions yet" — because it read only the task inbox.
 * These tests pin the three things that has to mean going forward: a document
 * mention appears, clicking it opens the document at that comment, and the
 * reassuring empty state is only shown when both inboxes actually answered.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: vi.fn(() => null) }));
vi.mock("@/components/ui/toast", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { api } from "@/lib/api";
import { ActivityPage } from "@/pages/activity-page";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { DocumentMention, Mention, Project } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

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

const TASK_MENTION: Mention = {
  comment_id: "c-task-1",
  mentioned_id: "u1",
  mentioned_kind: "user",
  mentioned_slug: "pavel",
  extracted_at: "2026-08-19T10:00:00Z",
  seen_at: null,
  task_id: "task-1",
  task_title: "Ship the migration",
  project_id: "proj-1",
  comment_body: "@pavel take a look",
  author_id: "a-1",
  author_name: "Ann Author",
};

const DOC_MENTION: DocumentMention = {
  comment_id: "c-doc-1",
  mentioned_id: "u1",
  mentioned_kind: "user",
  mentioned_slug: "pavel",
  extracted_at: "2026-08-19T12:00:00Z",
  seen_at: null,
  document_id: "doc-1",
  document_title: "Rollback Plan",
  document_slug: "rollback-plan",
  project_id: "proj-1",
  comment_body: "@pavel this section",
  author_id: "a-1",
  author_name: "Ann Author",
};

const TASK_PATH = "/api/v1/me/mentions";
const DOC_PATH = "/api/v1/me/document-mentions";

/** Answers the two inboxes; `Error` values are rejected as a failed request. */
function mockInbox(tasks: Mention[] | Error, docs: DocumentMention[] | Error) {
  mockedApi.mockImplementation((path: string) => {
    if (path === TASK_PATH) {
      return tasks instanceof Error ? Promise.reject(tasks) : Promise.resolve(tasks);
    }
    if (path === DOC_PATH) {
      return docs instanceof Error ? Promise.reject(docs) : Promise.resolve(docs);
    }
    if (path.endsWith("/seen")) return Promise.resolve(undefined);
    return Promise.reject(new Error(`unexpected path ${path}`));
  });
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/w/acme/activity"]}>
      <Routes>
        <Route path="/w/:wsSlug/activity" element={<ActivityPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockedNavigate.mockReset();
  mockedApi.mockReset();
  localStorage.clear();
  useProjectStore.setState({ projects: [PROJECT] });
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

describe("Activity → Mentions", () => {
  it("shows a mention from a document comment (acceptance 1)", async () => {
    mockInbox([], [DOC_MENTION]);
    renderPage();

    expect(await screen.findByText("Rollback Plan")).toBeInTheDocument();
    // And it must NOT be claiming the inbox is empty at the same time.
    expect(screen.queryByText(/No mentions yet/)).not.toBeInTheDocument();
  });

  it("opens the document at the mentioned comment when clicked (acceptance 1)", async () => {
    mockInbox([], [DOC_MENTION]);
    renderPage();

    fireEvent.click(await screen.findByTestId("mention-card-document"));

    await waitFor(() =>
      expect(mockedNavigate).toHaveBeenCalledWith(
        "/w/acme/p/demo/docs/doc-1?comment=c-doc-1",
      ),
    );
  });

  it("marks a document mention seen on its own endpoint, not the task one", async () => {
    mockInbox([], [DOC_MENTION]);
    renderPage();

    fireEvent.click(await screen.findByTestId("mention-card-document"));

    await waitFor(() =>
      expect(mockedApi).toHaveBeenCalledWith(
        `${DOC_PATH}/c-doc-1/seen`,
        { method: "POST" },
      ),
    );
  });

  it("still shows task mentions and still opens the task (regression, acceptance 3)", async () => {
    mockInbox([TASK_MENTION], []);
    renderPage();

    fireEvent.click(await screen.findByTestId("mention-card-task"));

    // Carries `?comment=` now, so task-panel.tsx can focus/scroll to the
    // specific comment the mention names (#8d097e67).
    await waitFor(() =>
      expect(mockedNavigate).toHaveBeenCalledWith(
        "/w/acme/p/demo/t/task-1?comment=c-task-1",
      ),
    );
    expect(mockedApi).toHaveBeenCalledWith(`${TASK_PATH}/c-task-1/seen`, {
      method: "POST",
    });
  });

  it("shows both kinds in one list, newest first", async () => {
    mockInbox([TASK_MENTION], [DOC_MENTION]);
    renderPage();

    await screen.findByText("Rollback Plan");
    const cards = screen.getAllByTestId(/^mention-card-/);
    expect(cards).toHaveLength(2);
    // DOC_MENTION is two hours newer than TASK_MENTION; both are unseen.
    expect(cards[0]).toHaveAttribute("data-testid", "mention-card-document");
  });

  it("does not claim 'no mentions yet' when an inbox failed to answer", async () => {
    mockInbox([], new Error("500"));
    renderPage();

    expect(await screen.findByTestId("mentions-load-failed")).toBeInTheDocument();
    expect(screen.queryByText(/No mentions yet/)).not.toBeInTheDocument();
  });

  it("warns above the list when one inbox failed but the other returned rows", async () => {
    mockInbox([TASK_MENTION], new Error("500"));
    renderPage();

    expect(await screen.findByTestId("mentions-load-failed")).toBeInTheDocument();
    expect(screen.getByText("Ship the migration")).toBeInTheDocument();
  });

  it("says 'no mentions yet' only when both inboxes answered and both were empty", async () => {
    mockInbox([], []);
    renderPage();

    expect(await screen.findByText(/No mentions yet/)).toBeInTheDocument();
    expect(screen.queryByTestId("mentions-load-failed")).not.toBeInTheDocument();
  });
});
