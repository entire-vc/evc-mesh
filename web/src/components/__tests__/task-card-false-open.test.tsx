import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TaskCard } from "@/components/task-card";
import type { Task } from "@/types";

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: "task-1",
    project_id: "proj-1",
    status_id: "status-1",
    title: "A card",
    assignee_id: null,
    assignee_type: "unassigned",
    priority: "urgent",
    parent_task_id: null,
    position: 0,
    due_date: null,
    estimated_hours: null,
    custom_fields: null,
    labels: null,
    created_by: "cccccccc-3333-3333-3333-333333333333",
    created_by_type: "agent",
    created_at: "2026-08-20T10:00:00Z",
    updated_at: "2026-08-20T10:00:00Z",
    completed_at: null,
    human_gate: false,
    ...overrides,
  } as Task;
}

const indicators = () => screen.queryAllByTestId("false-open-indicator");

// Task #c80fe88f: priority/created_at describe the card, not whether the
// graph underneath it is still moving. These tests exercise the board's
// rendering of the false_open field the server now sends.
describe("TaskCard false-open indicator", () => {
  // Negative control: no false_open payload at all (leaf task, or an open
  // umbrella with live children) -> no trace of either badge.
  it("shows nothing when false_open is absent", () => {
    render(<TaskCard task={makeTask({ false_open: undefined })} />);
    expect(indicators()).toHaveLength(0);
  });

  // Negative control: false_open present (server checked) but both flags
  // false -- e.g. a genuinely live open child. Must not render either badge.
  it("shows nothing when both flags are false", () => {
    render(
      <TaskCard
        task={makeTask({
          false_open: {
            all_children_closed: false,
            only_parked_children_left: false,
            open_children_count: 2,
            stale_days: 20,
          },
        })}
      />,
    );
    expect(indicators()).toHaveLength(0);
  });

  it("marks all_children_closed with the strong-signal badge", () => {
    render(
      <TaskCard
        task={makeTask({
          false_open: {
            all_children_closed: true,
            only_parked_children_left: false,
            open_children_count: 0,
            stale_days: 80,
          },
        })}
      />,
    );
    const els = indicators();
    expect(els).toHaveLength(1);
    expect(els[0].getAttribute("data-false-open-kind")).toBe(
      "all-children-closed",
    );
    expect(els[0].getAttribute("title")).toContain("80");
  });

  // #65dc5949's own shape (task #c80fe88f review, 2026-08-20): one open
  // backlog child left. Must render the weak-signal badge, never the
  // all-children-closed one -- the two are mutually exclusive by
  // construction, but this pins the board never merges them visually either.
  it("marks only_parked_children_left with the weak-signal badge, distinct from all_children_closed", () => {
    render(
      <TaskCard
        task={makeTask({
          false_open: {
            all_children_closed: false,
            only_parked_children_left: true,
            open_children_count: 1,
            stale_days: 65,
          },
        })}
      />,
    );
    const els = indicators();
    expect(els).toHaveLength(1);
    expect(els[0].getAttribute("data-false-open-kind")).toBe(
      "only-parked-children-left",
    );
    expect(els[0].getAttribute("title")).toContain("1");
  });
});
