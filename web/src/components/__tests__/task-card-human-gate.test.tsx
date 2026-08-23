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
    priority: "medium",
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

const indicator = () => screen.queryByTestId("human-gate-indicator");

describe("TaskCard human_gate indicator", () => {
  // Negative control (#098468fa AC4): a card with human_gate=false must carry no
  // trace of the badge — this is what proves the indicator discriminates rather
  // than always rendering.
  it("shows nothing when human_gate is false", () => {
    render(<TaskCard task={makeTask({ human_gate: false })} />);
    expect(indicator()).toBeNull();
  });

  it("marks a gated card, defaulting the class to hard when unset", () => {
    render(<TaskCard task={makeTask({ human_gate: true })} />);
    const el = indicator();
    expect(el).not.toBeNull();
    expect(el?.getAttribute("data-human-gate-class")).toBe("hard");
  });

  it("carries the soft class through when the task is soft-gated", () => {
    render(
      <TaskCard task={makeTask({ human_gate: true, human_gate_class: "soft" })} />,
    );
    expect(indicator()?.getAttribute("data-human-gate-class")).toBe("soft");
  });
});
