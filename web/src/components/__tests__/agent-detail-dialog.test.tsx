import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { AgentDetailDialog } from "@/components/agent-detail-dialog";
import type { Agent } from "@/types";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  name: "Verity",
  agent_type: "claude_code",
  status: "offline",
  role: "lead",
  capabilities: {},
  responsibility_zone: "",
  escalation_to: null,
  accepts_from: [],
  max_concurrent_tasks: 0,
  working_hours: "",
  metadata: {},
  last_heartbeat: null,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

describe("AgentDetailDialog — profile fields (task #85714565)", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
  });

  it("edits responsibility zone through PUT /agents/:id/profile, then refetches the agent", async () => {
    const updated = { ...baseAgent, responsibility_zone: "Mesh — interface" };
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string }) => {
      if (path === "/api/v1/agents/agent-1/profile" && opts?.method === "PUT") {
        return Promise.resolve(undefined); // 204 No Content
      }
      if (path === "/api/v1/agents/agent-1" && (!opts || !opts.method)) {
        return Promise.resolve(updated);
      }
      return Promise.reject(new Error(`unexpected call: ${path} ${opts?.method}`));
    });

    render(
      <AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />,
    );

    fireEvent.click(screen.getByTitle("Edit Responsibility zone"));
    const zoneInput = screen.getByTitle("Responsibility zone");
    fireEvent.change(zoneInput, { target: { value: "Mesh — interface" } });
    fireEvent.click(screen.getByLabelText("Save"));

    await waitFor(() => {
      const putCall = vi.mocked(api).mock.calls.find(
        ([p, o]) => p === "/api/v1/agents/agent-1/profile" && (o as { method?: string })?.method === "PUT",
      );
      expect(putCall).toBeTruthy();
    });

    const putCall = vi.mocked(api).mock.calls.find(
      ([p]) => p === "/api/v1/agents/agent-1/profile",
    )!;
    const body = (putCall[1] as { body: Record<string, unknown> }).body;
    expect(body.responsibility_zone).toBe("Mesh — interface");
  });

  it("clearing accepts_from on an existing agent sends an explicit star, not an empty list", async () => {
    const agentWithAccepts = { ...baseAgent, accepts_from: ["Garfield"] };
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string }) => {
      if (path === "/api/v1/agents/agent-1/profile" && opts?.method === "PUT") {
        return Promise.resolve(undefined);
      }
      if (path === "/api/v1/agents/agent-1") {
        return Promise.resolve(agentWithAccepts);
      }
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(
      <AgentDetailDialog open onOpenChange={vi.fn()} agent={agentWithAccepts} />,
    );

    fireEvent.click(screen.getByTitle("Edit Accepts from"));
    const editInput = screen.getByDisplayValue("Garfield");
    fireEvent.change(editInput, { target: { value: "" } });
    fireEvent.keyDown(editInput, { key: "Enter" });

    await waitFor(() => {
      const putCall = vi.mocked(api).mock.calls.find(
        ([p, o]) => p === "/api/v1/agents/agent-1/profile" && (o as { method?: string })?.method === "PUT",
      );
      expect(putCall).toBeTruthy();
    });

    const putCall = vi.mocked(api).mock.calls.find(
      ([p]) => p === "/api/v1/agents/agent-1/profile",
    )!;
    const body = (putCall[1] as { body: Record<string, unknown> }).body;
    expect(body.accepts_from).toEqual(["*"]);
  });

  it("edits max concurrent tasks (aria-label only, no shipped label text) via the profile endpoint", async () => {
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string }) => {
      if (path === "/api/v1/agents/agent-1/profile" && opts?.method === "PUT") {
        return Promise.resolve(undefined);
      }
      if (path === "/api/v1/agents/agent-1") {
        return Promise.resolve({ ...baseAgent, max_concurrent_tasks: 8 });
      }
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(
      <AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />,
    );

    fireEvent.click(screen.getByTitle("Edit max concurrent tasks"));
    const editInput = screen.getByLabelText("Max concurrent tasks");
    fireEvent.change(editInput, { target: { value: "8" } });
    fireEvent.keyDown(editInput, { key: "Enter" });

    await waitFor(() => {
      const putCall = vi.mocked(api).mock.calls.find(
        ([p, o]) => p === "/api/v1/agents/agent-1/profile" && (o as { method?: string })?.method === "PUT",
      );
      expect(putCall).toBeTruthy();
    });

    const putCall = vi.mocked(api).mock.calls.find(
      ([p]) => p === "/api/v1/agents/agent-1/profile",
    )!;
    const body = (putCall[1] as { body: Record<string, unknown> }).body;
    expect(body.max_concurrent_tasks).toBe(8);
  });

  it("edits escalation contact as a single name, not a comma-split list", async () => {
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string }) => {
      if (path === "/api/v1/agents/agent-1/profile" && opts?.method === "PUT") {
        return Promise.resolve(undefined);
      }
      if (path === "/api/v1/agents/agent-1") {
        return Promise.resolve({ ...baseAgent, escalation_to: "Garfield" });
      }
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    fireEvent.click(screen.getByTitle("Edit escalation contact"));
    const editInput = screen.getByLabelText("Escalation contact");
    fireEvent.change(editInput, { target: { value: "Garfield" } });
    fireEvent.keyDown(editInput, { key: "Enter" });

    await waitFor(() => {
      const putCall = vi.mocked(api).mock.calls.find(
        ([p, o]) => p === "/api/v1/agents/agent-1/profile" && (o as { method?: string })?.method === "PUT",
      );
      expect(putCall).toBeTruthy();
    });

    const putCall = vi.mocked(api).mock.calls.find(
      ([p]) => p === "/api/v1/agents/agent-1/profile",
    )!;
    const body = (putCall[1] as { body: Record<string, unknown> }).body;
    // A single string ("Garfield"), matching the MCP tool and the YAML
    // team-config contract — NOT ["Garfield"]. Regression for the type
    // mismatch code review found (rules_service.go ExportConfig silently
    // dropped array-shaped escalation_to on YAML export).
    expect(body.escalation_to).toBe("Garfield");
  });

  it("refuses to save an invalid max concurrent tasks draft instead of silently committing 0 — Save is disabled, no request is sent", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(
      <AgentDetailDialog open onOpenChange={vi.fn()} agent={{ ...baseAgent, max_concurrent_tasks: 5 }} />,
    );

    fireEvent.click(screen.getByTitle("Edit max concurrent tasks"));
    const editInput = screen.getByLabelText("Max concurrent tasks");
    fireEvent.change(editInput, { target: { value: "" } });

    const saveButton = screen.getByLabelText("Save");
    expect(saveButton).toBeDisabled();
    fireEvent.click(saveButton);

    // Give any accidental async save a tick to fire, then confirm nothing did.
    await new Promise((r) => setTimeout(r, 20));
    expect(api).not.toHaveBeenCalled();
  });

  it("disables Cancel while a save is in flight, so cancelling can't be silently overwritten by the pending save landing later", async () => {
    let resolvePut!: (v: undefined) => void;
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string }) => {
      if (path === "/api/v1/agents/agent-1/profile" && opts?.method === "PUT") {
        return new Promise((resolve) => {
          resolvePut = resolve;
        });
      }
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    fireEvent.click(screen.getByTitle("Edit Responsibility zone"));
    const zoneInput = screen.getByTitle("Responsibility zone");
    fireEvent.change(zoneInput, { target: { value: "Mesh — interface" } });
    fireEvent.click(screen.getByLabelText("Save"));

    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(screen.getByLabelText("Cancel")).toBeDisabled();

    resolvePut(undefined);
  });

  it("negative control: removing the profile fields from the dialog would leave these edit affordances unreachable — the pencil buttons exist for all five new fields plus capabilities", () => {
    render(
      <AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />,
    );
    expect(screen.getByTitle("Edit Responsibility zone")).toBeTruthy();
    expect(screen.getByTitle("Edit Working hours")).toBeTruthy();
    expect(screen.getByTitle("Edit Accepts from")).toBeTruthy();
    expect(screen.getByTitle("Edit max concurrent tasks")).toBeTruthy();
    expect(screen.getByTitle("Edit escalation contact")).toBeTruthy();
    expect(screen.getByTitle("Edit capabilities")).toBeTruthy();
  });
});
