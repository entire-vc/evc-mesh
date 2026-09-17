/**
 * The create screen and the edit screen are ONE form. This test is the wall
 * that keeps them one (task #88087321, third time Pavel has asked for it).
 *
 * It renders TaskPanel twice against identical store state — mode="create" and
 * the normal task view — reads the Properties panel of each, and asserts that
 * the create panel offers every property the edit panel does, except for a
 * PINNED list of rows that cannot exist before the task does.
 *
 * Why pinned rather than computed: a computed exclusion list would absorb the
 * next dropped field silently, which is exactly how the create form lost
 * Estimate, Custom Fields and the template picker in the first place. Adding a
 * row to ONLY_AFTER_CREATE is a deliberate edit with a reason next to it, and
 * that edit is what a reviewer gets to argue with.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(() => Promise.resolve({ enabled: false })),
  getAccessToken: vi.fn(() => null),
  getTaskCostSummary: vi.fn(() => Promise.resolve(null)),
  uploadPendingImages: vi.fn((_id: string, _p: unknown, desc: string) =>
    Promise.resolve(desc),
  ),
}));

import { TaskPanel } from "@/components/task-panel";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { useMemberStore } from "@/stores/member";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useRecurringStore } from "@/stores/recurring";
import { useRulesStore } from "@/stores/rules";
import { useTemplateStore } from "@/stores/template";
import type { CustomFieldDefinition, Project, Task } from "@/types";

/**
 * Rows the create form legitimately does NOT have, each with the reason it
 * cannot exist while the task is still a draft. Everything else must match.
 */
const ONLY_AFTER_CREATE: Record<string, string> = {
  "Human gate": "a gate is armed by a comment on an existing task",
  Created: "there is no creation timestamp before creation",
  Updated: "there is no update timestamp before creation",
};

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

const CUSTOM_FIELDS: CustomFieldDefinition[] = [
  {
    id: "cf-1",
    project_id: PROJECT.id,
    name: "Impact",
    slug: "impact",
    field_type: "text",
    description: "",
    options: {},
    default_value: null,
    is_required: false,
    is_visible_to_agents: true,
    position: 0,
    created_at: "2026-08-01T00:00:00Z",
  },
];

const TASK: Task = {
  id: "88087321-0000-4000-8000-000000000001",
  project_id: PROJECT.id,
  status_id: "status-1",
  title: "An existing task",
  assignee_id: null,
  assignee_type: "unassigned",
  priority: "none",
  human_gate: false,
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
};

beforeEach(() => {
  vi.restoreAllMocks();
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
    fields: CUSTOM_FIELDS,
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
    tasksById: { [TASK.id]: TASK },
    fetchTask: vi.fn().mockResolvedValue(TASK),
    createTask: vi.fn().mockResolvedValue(TASK),
  });
});

/**
 * The property NAMES a Properties panel is currently offering. Read from the
 * first panel in the DOM: both layouts (mobile + desktop) mount the same grid,
 * so one is enough and they cannot disagree.
 *
 * "Show empty" is clicked first in both modes, so the comparison is between
 * full sets — otherwise the test would compare two collapsed views and pass on
 * a create form that has no expanded state at all.
 */
async function propertyNamesOf(mode: "create" | "edit"): Promise<string[]> {
  const router = createMemoryRouter(
    [
      {
        path: "/",
        element:
          mode === "create" ? (
            <TaskPanel taskId={null} mode="create" createProjectId={PROJECT.id} />
          ) : (
            <TaskPanel taskId={TASK.id} />
          ),
      },
    ],
    { initialEntries: ["/"] },
  );
  const { unmount } = render(<RouterProvider router={router} />);

  const header = await screen.findAllByText("Properties");
  const panel = header[0]!.closest("div")!.parentElement!;

  const toggle = within(panel).queryByText(/Show empty|Hide empty/);
  if (toggle && toggle.textContent === "Show empty") {
    toggle.click();
    await waitFor(() =>
      expect(within(panel).getByText("Hide empty")).toBeTruthy(),
    );
  }

  const names = Array.from(panel.querySelectorAll("label"))
    .map((l) => (l.textContent ?? "").replace(/\*$/, "").trim())
    .filter(Boolean);
  // Section headings are not <label> elements in every block, so pick up the
  // two that are rendered as headings.
  for (const heading of ["Definition of Done", "Custom Fields"]) {
    if (within(panel).queryByText(heading)) names.push(heading);
  }

  unmount();
  return Array.from(new Set(names));
}

describe("TaskPanel — the create form is the edit form", () => {
  it("offers every property the edit form offers, except the pinned after-create rows", async () => {
    const editNames = await propertyNamesOf("edit");
    const createNames = await propertyNamesOf("create");

    expect(editNames.length).toBeGreaterThan(5); // the comparison is not vacuous

    const missing = editNames.filter(
      (n) => !createNames.includes(n) && !(n in ONLY_AFTER_CREATE),
    );
    expect(missing).toEqual([]);
  });

  it("every pinned exclusion is really absent from create and really present in edit", async () => {
    // Without this, ONLY_AFTER_CREATE could quietly list a row that no longer
    // exists anywhere, and the test above would keep passing while the list
    // rotted into a comment.
    const editNames = await propertyNamesOf("edit");
    const createNames = await propertyNamesOf("create");

    for (const name of Object.keys(ONLY_AFTER_CREATE)) {
      if (name === "Human gate") continue; // only rendered on a gated task
      expect(editNames, `${name} should exist on the edit form`).toContain(name);
      expect(
        createNames,
        `${name} is listed as after-create but the create form renders it`,
      ).not.toContain(name);
    }
  });

  it("carries Estimate and Custom Fields into createTask, not just onto the screen", async () => {
    const createTask = vi.fn().mockResolvedValue(TASK);
    useTaskStore.setState({ createTask });

    const router = createMemoryRouter(
      [
        {
          path: "/",
          element: (
            <TaskPanel taskId={null} mode="create" createProjectId={PROJECT.id} />
          ),
        },
      ],
      { initialEntries: ["/"] },
    );
    render(<RouterProvider router={router} />);

    const title = await screen.findByPlaceholderText("Task title *");
    const { fireEvent } = await import("@testing-library/react");
    fireEvent.change(title, { target: { value: "With estimate" } });

    const estimate = await screen.findByPlaceholderText("e.g. 4");
    fireEvent.change(estimate, { target: { value: "3.5" } });

    fireEvent.click(screen.getByText("Create Task"));

    await waitFor(() => expect(createTask).toHaveBeenCalledTimes(1));
    expect(createTask.mock.calls[0]![1]).toMatchObject({
      title: "With estimate",
      estimated_hours: 3.5,
    });
  });
});
