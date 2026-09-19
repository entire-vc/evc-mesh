/**
 * The "Clear gate" button in the task panel (task #b044935d).
 *
 * The button used to render only when the gate carried a marker comment
 * (`human_gate_info.marker_comment_id`). A gate armed through the API has no
 * marker, so the one person allowed to lift it had no control at all. Both
 * shapes must now be clearable, each by its own door:
 *
 *   marker present → POST /human-gate-decisions  (recordHumanGateDecision)
 *   marker absent  → DELETE /human-gate          (clearHumanGate)
 *
 * The button must still be absent without a signed-in user — that control is
 * what proves the assertions below are not passing because the button is
 * simply always there.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";

const recordHumanGateDecision = vi.fn();
const clearHumanGate = vi.fn();

vi.mock("@/lib/api", () => ({
  api: vi.fn((path: string) => {
    if (path.includes("/vcs-links")) return Promise.resolve({ vcs_links: [] });
    if (path.includes("/dependencies")) {
      return Promise.resolve({ blocked_by: [], blocks: [], parent: null, children: [] });
    }
    return Promise.resolve({ enabled: false });
  }),
  getAccessToken: vi.fn(() => null),
  getTaskCostSummary: vi.fn(() =>
    Promise.resolve({
      total_cost: 0,
      tokens_in: 0,
      tokens_out: 0,
      session_count: 0,
      rework_count: 0,
      quality_flag: "",
    }),
  ),
  recordHumanGateDecision: (...args: unknown[]) => recordHumanGateDecision(...args),
  clearHumanGate: (...args: unknown[]) => clearHumanGate(...args),
  uploadPendingImages: vi.fn((_id: string, _p: unknown, desc: string) =>
    Promise.resolve(desc),
  ),
}));

import { TaskPanel } from "@/components/task-panel";
import { useAuthStore } from "@/stores/auth";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { useMemberStore } from "@/stores/member";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useRecurringStore } from "@/stores/recurring";
import { useRulesStore } from "@/stores/rules";
import { useTemplateStore } from "@/stores/template";
import type { HumanGateInfo, Project, Task, User } from "@/types";

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

const USER = { id: "user-1", email: "p@example.com", name: "P" } as unknown as User;

function gatedTask(info: HumanGateInfo): Task {
  return {
    id: "b044935d-0000-4000-8000-000000000001",
    project_id: PROJECT.id,
    status_id: "status-1",
    title: "A gated task",
    assignee_id: null,
    assignee_type: "unassigned",
    priority: "none",
    human_gate: true,
    human_gate_class: "hard",
    human_gate_info: info,
    parent_task_id: null,
    position: 0,
    due_date: null,
    estimated_hours: null,
    custom_fields: null,
    labels: null,
    created_by: "u1",
    created_by_type: "user",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    completed_at: null,
  } as Task;
}

const MARKER_GATE: HumanGateInfo = {
  gated: true,
  owner_name: "Riker",
  marker_comment_id: "comment-1",
  marker_created_at: "2026-09-18T10:00:00Z",
  clearable_by_owner: true,
  clear_path: "withdraw_marker",
};

// What the server reports for an API-armed gate: no marker comment at all.
const MARKERLESS_GATE: HumanGateInfo = {
  gated: true,
  owner_name: "Riker",
  clearable_by_owner: true,
  clear_path: "clear_endpoint",
};

function mount(task: Task, user: User | null) {
  useAuthStore.setState({ user, isAuthenticated: user !== null });
  useProjectStore.setState({
    projects: [PROJECT],
    currentProject: PROJECT,
    statuses: [],
    fetchStatuses: vi.fn().mockResolvedValue(undefined),
  });
  useMemberStore.setState({
    projectMembers: [],
    fetchProjectMembers: vi.fn().mockResolvedValue(undefined),
  });
  useCustomFieldStore.setState({
    fields: [],
    fetchFields: vi.fn().mockResolvedValue(undefined),
  });
  useRecurringStore.setState({
    schedules: [],
    fetchSchedules: vi.fn().mockResolvedValue(undefined),
  });
  useRulesStore.setState({
    teamDirectory: { agents: [], humans: [] } as never,
    fetchTeamDirectory: vi.fn().mockResolvedValue(undefined),
  });
  useTemplateStore.setState({
    templates: [],
    fetchTemplates: vi.fn().mockResolvedValue(undefined),
  });
  useTaskStore.setState({
    tasksById: { [task.id]: task },
    fetchTask: vi.fn().mockResolvedValue(task),
  });

  const router = createMemoryRouter(
    [{ path: "/", element: <TaskPanel taskId={task.id} /> }],
    { initialEntries: ["/"] },
  );
  return render(<RouterProvider router={router} />);
}

beforeEach(() => {
  vi.restoreAllMocks();
  recordHumanGateDecision.mockReset().mockResolvedValue({});
  clearHumanGate.mockReset().mockResolvedValue({});
  vi.spyOn(window, "confirm").mockReturnValue(true);
});

describe("TaskPanel — Clear gate", () => {
  it("marker-armed gate: clears through the decision endpoint, not DELETE", async () => {
    const task = gatedTask(MARKER_GATE);
    mount(task, USER);

    fireEvent.click((await screen.findAllByTestId("human-gate-clear-button"))[0]!);

    await waitFor(() => expect(recordHumanGateDecision).toHaveBeenCalledTimes(1));
    expect(recordHumanGateDecision).toHaveBeenCalledWith(task.id, {
      question_ref: "comment-1",
      decided_by: USER.id,
      provenance: "direct",
      channel: "mesh",
    });
    expect(clearHumanGate).not.toHaveBeenCalled();
  });

  it("marker-less gate: the button exists and clears through DELETE", async () => {
    const task = gatedTask(MARKERLESS_GATE);
    mount(task, USER);

    // Before the fix this findAllByTestId never resolved: no marker, no button.
    fireEvent.click((await screen.findAllByTestId("human-gate-clear-button"))[0]!);

    await waitFor(() => expect(clearHumanGate).toHaveBeenCalledTimes(1));
    expect(clearHumanGate).toHaveBeenCalledWith(task.id);
    expect(recordHumanGateDecision).not.toHaveBeenCalled();
  });

  it("asks for confirmation and does nothing when it is declined", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(false);
    mount(gatedTask(MARKERLESS_GATE), USER);

    fireEvent.click((await screen.findAllByTestId("human-gate-clear-button"))[0]!);

    expect(window.confirm).toHaveBeenCalledWith(
      "Clear the human gate on this task? This records that the question was answered and unfreezes the task.",
    );
    expect(clearHumanGate).not.toHaveBeenCalled();
    expect(recordHumanGateDecision).not.toHaveBeenCalled();
  });

  it("positive control: no signed-in user, no button — for either shape", async () => {
    for (const info of [MARKER_GATE, MARKERLESS_GATE]) {
      const { unmount } = mount(gatedTask(info), null);
      // The badge proves the gate block rendered; only the button is absent.
      await screen.findAllByTestId("human-gate-badge");
      expect(screen.queryAllByTestId("human-gate-clear-button")).toHaveLength(0);
      unmount();
    }
  });
});
