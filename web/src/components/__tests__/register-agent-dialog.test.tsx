import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { RegisterAgentDialog } from "@/components/register-agent-dialog";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";

// Regression coverage for task #85714565: a human registering an agent
// through the UI and touching nothing but Name must not end up with a lane
// that reads max_concurrent_tasks=0 ("takes no tasks") or an unset
// accepts_from — that is exactly what happened to the Verity lane.
describe("RegisterAgentDialog", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
  });

  it("submits non-empty defaults for max_concurrent_tasks, accepts_from and working_hours when the human leaves them untouched", async () => {
    vi.mocked(api).mockResolvedValue({
      agent: { id: "a1", name: "Bare Agent" },
      api_key: "agk_ws_deadbeef",
    });

    render(
      <RegisterAgentDialog open onOpenChange={vi.fn()} workspaceId="ws-1" />,
    );

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Bare Agent" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Register Agent" }));

    await waitFor(() => expect(api).toHaveBeenCalled());

    const [path, opts] = vi.mocked(api).mock.calls[0]!;
    expect(path).toBe("/api/v1/workspaces/ws-1/agents");
    const body = opts?.body as Record<string, unknown>;

    expect(body.max_concurrent_tasks).toBe(5);
    expect(body.working_hours).toBe("24/7");
    expect(body.accepts_from).toEqual(["*"]);
    // Zone/escalation/role have no universal default — omitted, not invented.
    expect(body.responsibility_zone).toBeUndefined();
    expect(body.escalation_to).toBeUndefined();
    expect(body.role).toBeUndefined();
  });

  it("respects explicit values the human types over the pre-filled defaults", async () => {
    vi.mocked(api).mockResolvedValue({
      agent: { id: "a1", name: "Configured Agent" },
      api_key: "agk_ws_deadbeef",
    });

    render(
      <RegisterAgentDialog open onOpenChange={vi.fn()} workspaceId="ws-1" />,
    );

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Configured Agent" },
    });
    fireEvent.change(screen.getByLabelText("Role"), {
      target: { value: "reviewer" },
    });
    fireEvent.change(screen.getByLabelText("Responsibility zone"), {
      target: { value: "Mesh — QA" },
    });
    fireEvent.change(screen.getByLabelText("Accepts from"), {
      target: { value: "Garfield, Linus" },
    });
    fireEvent.change(screen.getByLabelText("Max concurrent tasks"), {
      target: { value: "12" },
    });
    fireEvent.change(screen.getByLabelText("Escalation contact"), {
      target: { value: "Pavel" },
    });
    fireEvent.change(screen.getByLabelText("Capabilities"), {
      target: { value: "code-review, docs" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Register Agent" }));

    await waitFor(() => expect(api).toHaveBeenCalled());

    const [, opts] = vi.mocked(api).mock.calls[0]!;
    const body = opts?.body as Record<string, unknown>;

    expect(body.role).toBe("reviewer");
    expect(body.responsibility_zone).toBe("Mesh — QA");
    expect(body.accepts_from).toEqual(["Garfield", "Linus"]);
    expect(body.max_concurrent_tasks).toBe(12);
    expect(body.escalation_to).toBe("Pavel");
    expect(body.capabilities).toEqual({ "code-review": true, docs: true });
  });

  it("clearing accepts_from back to empty still sends an explicit star, not an empty list", async () => {
    vi.mocked(api).mockResolvedValue({
      agent: { id: "a1", name: "Bare Agent" },
      api_key: "agk_ws_deadbeef",
    });

    render(
      <RegisterAgentDialog open onOpenChange={vi.fn()} workspaceId="ws-1" />,
    );

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Bare Agent" },
    });
    fireEvent.change(screen.getByLabelText("Accepts from"), {
      target: { value: "" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Register Agent" }));

    await waitFor(() => expect(api).toHaveBeenCalled());

    const [, opts] = vi.mocked(api).mock.calls[0]!;
    const body = opts?.body as Record<string, unknown>;
    // Omitted entirely — the backend/repo default for "omitted" is ["*"].
    expect(body.accepts_from).toBeUndefined();
  });
});
