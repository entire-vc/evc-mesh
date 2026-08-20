import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TaskCard } from "@/components/task-card";
import type { Task } from "@/types";

const AGENT = "aaaaaaaa-1111-1111-1111-111111111111";
const OTHER_AGENT = "bbbbbbbb-2222-2222-2222-222222222222";

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: "task-1",
    project_id: "proj-1",
    status_id: "status-1",
    title: "A card",
    assignee_id: AGENT,
    assignee_type: "agent",
    assignee_name: "Gandalf",
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
    ...overrides,
  } as Task;
}

const indicator = () => screen.queryByTestId("checkout-indicator");

describe("TaskCard checkout indicator", () => {
  it("shows nothing when the card is not checked out", () => {
    render(<TaskCard task={makeTask()} />);
    expect(indicator()).toBeNull();
  });

  it("marks a card that is in an agent's hands", () => {
    const future = new Date(Date.now() + 90 * 60 * 1000).toISOString();
    render(<TaskCard task={makeTask({ checked_out_by: AGENT, checkout_expires: future })} />);

    const el = indicator();
    expect(el).not.toBeNull();
    expect(el?.getAttribute("data-checkout-state")).toBe("live");
  });

  // The distinction the board needs: a lapsed lock means the holder's session is
  // gone and nobody is working the card, which must not look like live work.
  it("distinguishes a lapsed lock from a live one", () => {
    const past = new Date(Date.now() - 30 * 60 * 1000).toISOString();
    render(<TaskCard task={makeTask({ checked_out_by: AGENT, checkout_expires: past })} />);

    expect(indicator()?.getAttribute("data-checkout-state")).toBe("lapsed");
  });

  it("names the holder only when it is not the assignee", () => {
    const future = new Date(Date.now() + 90 * 60 * 1000).toISOString();

    const { unmount } = render(
      <TaskCard
        task={makeTask({ checked_out_by: AGENT, checkout_expires: future })}
        checkedOutByName="Gandalf"
      />,
    );
    // Holder == assignee: the avatar already says who, so the badge stays quiet.
    expect(indicator()?.textContent).not.toContain("Gandalf");
    unmount();

    render(
      <TaskCard
        task={makeTask({ checked_out_by: OTHER_AGENT, checkout_expires: future })}
        checkedOutByName="Linus"
      />,
    );
    expect(indicator()?.textContent).toContain("Linus");
  });

  // Guard against a half-populated payload rendering a badge with no expiry:
  // "held, unknown until when" is the ambiguity this card exists to remove.
  it("renders no badge when the expiry is missing", () => {
    render(<TaskCard task={makeTask({ checked_out_by: AGENT })} />);
    expect(indicator()).toBeNull();
  });
});
