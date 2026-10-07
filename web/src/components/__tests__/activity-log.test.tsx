import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { ActivityLog } from "@/components/activity-log";
import { useAgentStore } from "@/stores/agent";

// mock: external HTTP boundary — use the real API client and UI components.
const fetchMock = vi.fn();
function respond(items: unknown[]) {
  fetchMock.mockResolvedValue(new Response(JSON.stringify({ items, page: 1, has_more: false }), {
    headers: { "Content-Type": "application/json" },
  }));
}

// Synthetic IDs and event data matching the checkout payload; no live secrets.
const holder = "11111111-1111-4111-8111-111111111111";
const entry = (action: string, changes: unknown) => ({
  id: action, workspace_id: "workspace", entity_type: "task", entity_id: "task",
  actor_id: holder, actor_type: "agent", actor_name: "Example Developer",
  action, changes, created_at: "2026-10-07T01:00:00Z",
});

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  useAgentStore.setState({ agents: [{ id: holder, name: "Example Developer" }] as never });
});

afterEach(() => vi.unstubAllGlobals());

describe("task Activity payloads", () => {
  it("shows checkout scalar details without inventing old/new transitions", async () => {
    respond([
      entry("task.checkout_acquired", { ttl_min: 120, expires_at: "2026-10-07T03:00:00Z" }),
      entry("task.checkout_auto_progress", { checked_out_by: holder }),
      entry("task.checkout_released", { actor_id: holder, source: "checkout", reason: "completed" }),
    ]);
    render(<ActivityLog taskId="task" />);
    expect(await screen.findByText("TTL (minutes):")).toBeInTheDocument();
    expect(screen.getByText("120")).toBeInTheDocument();
    expect(screen.getByText("Expires at:")).toBeInTheDocument();
    expect(screen.getByText("Checked out by:")).toBeInTheDocument();
    expect(screen.getByText("Source:")).toBeInTheDocument();
    expect(screen.getByText("Reason:")).toBeInTheDocument();
    expect(screen.getByText("completed")).toBeInTheDocument();
    for (const details of screen.getAllByLabelText("Event details")) {
      expect(within(details).queryByLabelText("changed to")).not.toBeInTheDocument();
      expect(details).not.toHaveTextContent("none");
    }
  });

  it("retains real status transitions and clearing a value to null", async () => {
    respond([entry("task.moved", {
      status: { old: "Todo", new: "In Progress" },
      assignee_id: { old: holder, new: null },
      source: "checkout",
    })]);
    render(<ActivityLog taskId="task" />);
    const details = await screen.findByLabelText("Event details");
    expect(within(details).getByText("Todo")).toBeInTheDocument();
    expect(within(details).getByText("In Progress")).toBeInTheDocument();
    expect(within(details).getByText("Example Developer")).toBeInTheDocument();
    expect(within(details).getByText("none")).toBeInTheDocument();
    expect(within(details).getAllByLabelText("changed to")).toHaveLength(2);
    expect(within(details).getByText("checkout")).toBeInTheDocument();
  });
});
