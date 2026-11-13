import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { AgentDetailDialog } from "@/components/agent-detail-dialog";
import { useAgentWorkspaceGrantStore } from "@/stores/agent-workspace-grant";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import type { Agent, Workspace } from "@/types";

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

describe("AgentDetailDialog — profile fields", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    setRole("admin");
  });

  it("computed_status online with an old heartbeat shows no stale mark; without computed_status the mark stays", async () => {
    const old = { ...baseAgent, status: "online" as const, last_heartbeat: "2026-01-01T00:00:00Z" };
    vi.mocked(api).mockResolvedValue({});
    const { unmount } = render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={old} computedStatus="online" />);
    expect(await screen.findByText("Online")).toBeInTheDocument();
    expect(screen.queryByText("(stale)")).not.toBeInTheDocument();
    unmount();
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={old} />);
    expect(await screen.findByText("(stale)")).toBeInTheDocument();
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
    const agentWithAccepts = { ...baseAgent, accepts_from: ["the product lead"] };
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
    const editInput = screen.getByDisplayValue("the product lead");
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

  it("edits max concurrent tasks via the profile endpoint", async () => {
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

    fireEvent.click(screen.getByTitle("Edit Max concurrent tasks"));
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
        return Promise.resolve({ ...baseAgent, escalation_to: "the product lead" });
      }
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    fireEvent.click(screen.getByTitle("Edit Escalation contact"));
    const editInput = screen.getByLabelText("Escalation contact");
    fireEvent.change(editInput, { target: { value: "the product lead" } });
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
    // A single string ("Gary"), matching the MCP tool and the YAML
    // team-config contract — NOT ["Gary"]. Regression for the type
    // mismatch code review found (rules_service.go ExportConfig silently
    // dropped array-shaped escalation_to on YAML export).
    expect(body.escalation_to).toBe("the product lead");
  });

  it("refuses to save an invalid max concurrent tasks draft instead of silently committing 0 — Save is disabled, no request is sent", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(
      <AgentDetailDialog open onOpenChange={vi.fn()} agent={{ ...baseAgent, max_concurrent_tasks: 5 }} />,
    );

    fireEvent.click(screen.getByTitle("Edit Max concurrent tasks"));
    const editInput = screen.getByLabelText("Max concurrent tasks");
    fireEvent.change(editInput, { target: { value: "" } });

    const saveButton = screen.getByLabelText("Save");
    expect(saveButton).toBeDisabled();
    fireEvent.click(saveButton);

    // Give any accidental async save a tick to fire, then confirm nothing did.
    // Scoped to the profile PUT rather than "api not called at all": the
    // dialog also fires GET /agents/:id/workspaces on open (task U4,
    // unrelated to this save path), which would otherwise make this
    // assertion fail for a reason that has nothing to do with what it's
    // actually testing.
    await new Promise((r) => setTimeout(r, 20));
    expect(
      vi.mocked(api).mock.calls.some(
        ([p]) => typeof p === "string" && p.includes("/profile"),
      ),
    ).toBe(false);
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
    expect(screen.getByTitle("Edit Max concurrent tasks")).toBeTruthy();
    expect(screen.getByTitle("Edit Escalation contact")).toBeTruthy();
    expect(screen.getByTitle("Edit capabilities")).toBeTruthy();
  });
});

function setRole(role: "admin" | "viewer" | null) {
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as Workspace });
  useMemberStore.setState({
    myRole: role,
    myRoleLoaded: role !== null,
    myRoleError: null,
    myRoleWorkspaceId: "ws-1",
  });
}

describe("AgentDetailDialog — Workspaces section (task U4)", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    setRole("admin");
    useAgentWorkspaceGrantStore.setState({
      agentWorkspaces: [],
      isLoadingAgentWorkspaces: false,
      agentWorkspacesForbidden: false,
    });
  });

  it("a viewer makes no privileged requests: no /workspaces, no /runtime (zero console 403s)", async () => {
    setRole("viewer");
    vi.mocked(api).mockImplementation((path: string) =>
      Promise.reject(new Error(`unexpected call: ${path}`)),
    );

    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    await waitFor(() => expect(screen.getByText("Status")).toBeInTheDocument());
    const paths = vi.mocked(api).mock.calls.map(([p]) => String(p));
    expect(paths.filter((p) => /\/agents\/agent-1\/workspaces$|\/runtime$/.test(p))).toEqual([]);
    expect(screen.queryByText("Workspaces")).not.toBeInTheDocument();
  });

  it("a role error left over from another workspace does not open the gate", async () => {
    setRole("viewer");
    useMemberStore.setState({ myRole: null, myRoleLoaded: false, myRoleError: "stale", myRoleWorkspaceId: "ws-old" });
    vi.mocked(api).mockImplementation((path: string) =>
      Promise.reject(new Error(`unexpected call: ${path}`)),
    );

    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    await waitFor(() => expect(screen.getByText("Status")).toBeInTheDocument());
    const paths = vi.mocked(api).mock.calls.map(([p]) => String(p));
    expect(paths.filter((p) => /\/agents\/agent-1\/workspaces$|\/runtime$/.test(p))).toEqual([]);
  });

  it("an admin still reads both the agent's workspaces and the runtime catalog", async () => {
    vi.mocked(api).mockImplementation(() => Promise.reject(new Error("x")));
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);
    await waitFor(() => {
      const paths = vi.mocked(api).mock.calls.map(([p]) => String(p));
      expect(paths).toContain("/api/v1/agents/agent-1/workspaces");
      expect(paths).toContain("/api/v1/workspaces/ws-1/runtime");
    });
  });

  it("hides the Workspaces section on 403 instead of showing an error or dropping the dialog (AC4)", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/agents/agent-1/workspaces") {
        return Promise.reject(
          Object.assign(new Error("workspace access denied"), {
            code: "FORBIDDEN",
            status: 403,
          }),
        );
      }
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/v1/agents/agent-1/workspaces"),
    );
    // The dialog is still up — an unrelated detail row proves it didn't
    // unmount or throw.
    expect(screen.getByText("Status")).toBeInTheDocument();
    expect(screen.queryByText("Workspaces")).not.toBeInTheDocument();
    expect(
      screen.queryByText(/workspace access denied/i),
    ).not.toBeInTheDocument();
  });

  it("lists the agent's workspaces with a Home badge on its own workspace, none on the rest", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/agents/agent-1/workspaces") {
        return Promise.resolve({
          workspaces: [
            {
              id: "grant-home",
              agent_id: "agent-1",
              workspace_id: baseAgent.workspace_id,
              role: "admin",
              api_key_prefix: "agk_ws1",
              created_at: "2026-08-01T00:00:00Z",
              workspace: { id: baseAgent.workspace_id, name: "Home WS", slug: "home-ws" },
            },
            {
              id: "grant-guest",
              agent_id: "agent-1",
              workspace_id: "ws-2",
              role: "viewer",
              api_key_prefix: "agk_ws2",
              created_at: "2026-08-02T00:00:00Z",
              workspace: { id: "ws-2", name: "Guest WS", slug: "guest-ws" },
            },
          ],
          count: 2,
        });
      }
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    fireEvent.click(screen.getByRole("tab", { name: "Workspace access" }));
    await screen.findByText("Home WS");
    expect(screen.getByText("Guest WS")).toBeInTheDocument();
    // Exactly one Home badge — the guest row gets only its role badge.
    expect(screen.getAllByText("Home")).toHaveLength(1);
  });
});

// Shape of the real ApiError (code + status), so apiErrorMessage uses the fallback text.
const apiError502 = () => Object.assign(new Error("Server error (502)"), { code: "server_error", status: 502 });

describe("AgentDetailDialog — harness and model", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
  });

  it("shows 'Not reported' when the agent has no model, and the model when it has one", () => {
    const { unmount } = render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);
    expect(screen.getByText("Not reported")).toBeTruthy();
    unmount();
    render(
      <AgentDetailDialog open onOpenChange={vi.fn()} agent={{ ...baseAgent, model: "gpt-6.1-sol" }} />,
    );
    expect(screen.getByText("gpt-6.1-sol")).toBeTruthy();
  });

  it("saves the model through PATCH /agents/:id with the trimmed value", async () => {
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string }) => {
      if (path === "/api/v1/agents/agent-1" && opts?.method === "PATCH") {
        return Promise.resolve({ ...baseAgent, model: "gpt-6.1-sol" });
      }
      return Promise.reject(new Error(`unexpected call: ${path} ${opts?.method}`));
    });
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    fireEvent.click(screen.getByTitle("Edit model"));
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "  gpt-6.1-sol " } });
    fireEvent.click(screen.getByTitle("Save model"));

    await waitFor(() => {
      const call = vi.mocked(api).mock.calls.find(
        ([p, o]) => p === "/api/v1/agents/agent-1" && (o as { method?: string })?.method === "PATCH",
      );
      expect(call).toBeTruthy();
      expect((call![1] as { body: unknown }).body).toEqual({ model: "gpt-6.1-sol" });
    });
  });

  it("changes the harness through the select", async () => {
    vi.mocked(api).mockResolvedValue({ ...baseAgent, agent_type: "codex" });
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);
    fireEvent.change(screen.getByLabelText("Harness"), { target: { value: "codex" } });
    await waitFor(() => {
      const call = vi.mocked(api).mock.calls.find(([, o]) => (o as { method?: string })?.method === "PATCH");
      expect((call![1] as { body: unknown }).body).toEqual({ agent_type: "codex" });
    });
  });
  it("shows an error and reverts the select when the harness change is rejected, then clears it on the next successful edit", async () => {
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string; body?: unknown }) => {
      const body = opts?.body as { model?: string } | undefined;
      if (path === "/api/v1/agents/agent-1" && opts?.method === "PATCH") {
        if (body?.model !== undefined) return Promise.resolve({ ...baseAgent, model: body.model });
        return Promise.reject(apiError502());
      }
      return Promise.reject(new Error(`unexpected call: ${path} ${opts?.method}`));
    });
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    fireEvent.change(screen.getByLabelText("Harness"), { target: { value: "codex" } });
    await waitFor(() => {
      expect(screen.getByText("Failed to update harness")).toBeTruthy();
    });
    expect((screen.getByLabelText("Harness") as HTMLSelectElement).value).toBe("claude_code");

    fireEvent.click(screen.getByTitle("Edit model"));
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "gpt-6.1-sol" } });
    fireEvent.click(screen.getByTitle("Save model"));
    await waitFor(() => {
      expect(screen.queryByText("Failed to update harness")).toBeNull();
    });
  });

  it("clears a stale model error when the next harness change starts", async () => {
    let harnessCall = false;
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string; body?: unknown }) => {
      const body = opts?.body as { agent_type?: string } | undefined;
      if (path === "/api/v1/agents/agent-1" && opts?.method === "PATCH") {
        if (body?.agent_type) {
          harnessCall = true;
          return Promise.resolve({ ...baseAgent, agent_type: "codex" });
        }
        return Promise.reject(apiError502());
      }
      return Promise.reject(new Error(`unexpected call: ${path} ${opts?.method}`));
    });
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);

    fireEvent.click(screen.getByTitle("Edit model"));
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "x" } });
    fireEvent.click(screen.getByTitle("Save model"));
    await waitFor(() => expect(screen.getByText("Failed to update model")).toBeTruthy());

    fireEvent.change(screen.getByLabelText("Harness"), { target: { value: "codex" } });
    await waitFor(() => expect(harnessCall).toBe(true));
    await waitFor(() => expect(screen.queryByText("Failed to update model")).toBeNull());
  });
});

describe("AgentDetailDialog — home runtime tabs stay (invited dialog regression guard)", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    vi.mocked(api).mockImplementation(() => Promise.reject(Object.assign(new Error("x"), { code: "e", status: 500 })));
  });

  it("keeps the existing tabs and adds Route & runs", () => {
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual([
      "Profile", "Route & runs", "Workspace access", "Execution & routes", "Observed state",
    ]);
  });
});

describe("AgentDetailDialog — repository access scope", () => {
  const access = { revision: 3, repositories: [{ provider: "gitlab", host: "gitlab.com", project_path: "entire-vc/evc-mesh" }] };

  function mockApi(agent: Agent, operated: { agent_id: string; operator_user_id: string | null }[] = []) {
    vi.mocked(api).mockImplementation((path: string) => {
      const p = String(path);
      if (p.startsWith("/api/v1/me/runtime/agents")) return Promise.resolve({ agents: operated });
      if (p.includes("/repository-access/history")) return Promise.resolve({ items: [] });
      if (p === "/api/v1/agents/agent-1") return Promise.resolve({ ...agent, repository_access: access });
      return Promise.reject(new Error(`unexpected call: ${p}`));
    });
  }

  beforeEach(() => {
    vi.mocked(api).mockReset();
    setRole("admin");
  });

  it("an admin of the current workspace edits the repository access of an agent that lives there (positive control)", async () => {
    mockApi(baseAgent);
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={baseAgent} />);
    fireEvent.click(screen.getByRole("tab", { name: "Workspace access" }));
    await waitFor(() => expect(screen.getAllByTestId("repository-access-row").length).toBe(1));
    expect(screen.getByLabelText("Host")).toBeInTheDocument();
  });

  it("an admin of the current workspace gets no editor for a guest agent from another workspace: the server checks the home workspace", async () => {
    const guest: Agent = { ...baseAgent, workspace_id: "ws-home-elsewhere" };
    mockApi(guest);
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={guest} />);
    fireEvent.click(screen.getByRole("tab", { name: "Workspace access" }));
    await waitFor(() => expect(screen.getAllByTestId("repository-access-row").length).toBe(1));
    expect(screen.queryByLabelText("Host")).not.toBeInTheDocument();
  });
});

describe("AgentDetailDialog — repository access operator rights", () => {
  const access = { revision: 3, repositories: [{ provider: "gitlab", host: "gitlab.com", project_path: "entire-vc/evc-mesh" }] };
  const me = "user-me";

  function mockApi(agent: Agent, operated: { agent_id: string; operator_user_id: string | null }[]) {
    vi.mocked(api).mockImplementation((path: string) => {
      const p = String(path);
      if (p.startsWith("/api/v1/me/runtime/agents")) return Promise.resolve({ agents: operated });
      if (p.includes("/repository-access/history")) return Promise.resolve({ items: [] });
      if (p === "/api/v1/agents/agent-1") return Promise.resolve({ ...agent, repository_access: access });
      return Promise.reject(new Error(`unexpected call: ${p}`));
    });
  }

  async function openAccessTab(agent: Agent) {
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={agent} />);
    fireEvent.click(screen.getByRole("tab", { name: "Workspace access" }));
    await waitFor(() => expect(screen.getAllByTestId("repository-access-row").length).toBe(1));
  }

  beforeEach(() => {
    vi.mocked(api).mockReset();
    setRole("viewer");
    useAuthStore.setState({ user: { id: me } as never });
  });

  it("a member who is only the supervisor, not the operator, gets no editor: supervisor_user_id grants nothing on the server", async () => {
    const agent: Agent = { ...baseAgent, supervisor_user_id: me };
    mockApi(agent, [{ agent_id: "agent-1", operator_user_id: "someone-else" }]);
    await openAccessTab(agent);
    await waitFor(() => expect(vi.mocked(api).mock.calls.some(([p]) => String(p).startsWith("/api/v1/me/runtime/agents"))).toBe(true));
    expect(screen.queryByLabelText("Host")).not.toBeInTheDocument();
  });

  it("the real operator, without admin role and without being the supervisor, gets the editor (positive control)", async () => {
    mockApi(baseAgent, [{ agent_id: "agent-1", operator_user_id: me }]);
    await openAccessTab(baseAgent);
    expect(await screen.findByLabelText("Host")).toBeInTheDocument();
  });

  it("a failed operator lookup leaves the editor read-only instead of guessing", async () => {
    mockApi(baseAgent, []);
    vi.mocked(api).mockImplementation((path: string) => {
      const p = String(path);
      if (p.startsWith("/api/v1/me/runtime/agents")) return Promise.reject(new Error("boom"));
      if (p.includes("/repository-access/history")) return Promise.resolve({ items: [] });
      if (p === "/api/v1/agents/agent-1") return Promise.resolve({ ...baseAgent, repository_access: access });
      return Promise.reject(new Error(`unexpected call: ${p}`));
    });
    await openAccessTab(baseAgent);
    expect(screen.queryByLabelText("Host")).not.toBeInTheDocument();
  });

  it("the operator of a guest agent asks for the runtime list of the agent's HOME workspace and gets the editor", async () => {
    const guest: Agent = { ...baseAgent, workspace_id: "ws-home-elsewhere" };
    vi.mocked(api).mockImplementation((path: string) => {
      const p = String(path);
      if (p === "/api/v1/me/runtime/agents?workspace_id=ws-home-elsewhere") return Promise.resolve({ agents: [{ agent_id: "agent-1", operator_user_id: me }] });
      if (p.startsWith("/api/v1/me/runtime/agents")) return Promise.resolve({ agents: [] });
      if (p.includes("/repository-access/history")) return Promise.resolve({ items: [] });
      if (p === "/api/v1/agents/agent-1") return Promise.resolve({ ...guest, repository_access: access });
      return Promise.reject(new Error(`unexpected call: ${p}`));
    });
    await openAccessTab(guest);
    expect(await screen.findByLabelText("Host")).toBeInTheDocument();
  });

  it("an admin of the viewing workspace who is not the operator of a guest agent still gets no editor (guest-admin ban kept)", async () => {
    setRole("admin");
    const guest: Agent = { ...baseAgent, workspace_id: "ws-home-elsewhere" };
    mockApi(guest, [{ agent_id: "agent-1", operator_user_id: "someone-else" }]);
    await openAccessTab(guest);
    await waitFor(() => expect(vi.mocked(api).mock.calls.some(([p]) => String(p) === "/api/v1/me/runtime/agents?workspace_id=ws-home-elsewhere")).toBe(true));
    expect(screen.queryByLabelText("Host")).not.toBeInTheDocument();
  });
});
