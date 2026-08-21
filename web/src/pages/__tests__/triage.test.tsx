import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { TriageTaskRow } from "@/pages/triage";
import type { Task } from "@/types";

const SAMPLE_TASK: Task = {
  id: "12345678-aaaa-bbbb-cccc-000000000001",
  project_id: "p1",
  status_id: "s1",
  title: "Investigate flaky test",
  description: "",
  assignee_id: null,
  assignee_type: "unassigned",
  priority: "medium",
  human_gate: false,
  parent_task_id: null,
  position: 0,
  due_date: null,
  estimated_hours: null,
  custom_fields: null,
  labels: null,
  created_by: "u1",
  created_by_type: "user",
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  completed_at: null,
};

describe("TriageTaskRow", () => {
  it("links the task #id to a relative /t/<id> path, not a hardcoded vendor domain", () => {
    render(
      <TriageTaskRow
        task={SAMPLE_TASK}
        projectName="Demo Project"
        onMove={vi.fn()}
        onEdit={vi.fn()}
      />,
    );

    const idLink = screen.getByRole("link", { name: /^#12345678$/ });
    expect(idLink).toHaveAttribute("href", `/t/${SAMPLE_TASK.id}`);
  });
});
