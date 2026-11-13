import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { AgentRepositoryAccess } from "@/components/agent-repository-access";
import { useAgentStore } from "@/stores/agent";
import type { Agent } from "@/types";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";

const agent = (ra?: Agent["repository_access"]): Agent => ({
  id: "a1", workspace_id: "w1", name: "Vega", agent_type: "claude_code", status: "offline",
  capabilities: {}, metadata: {}, last_heartbeat: null,
  created_at: "2026-08-01T00:00:00Z", updated_at: "2026-08-01T00:00:00Z",
  ...(ra === undefined ? {} : { repository_access: ra }),
});
const repo = { provider: "gitlab", host: "gitlab.com", project_path: "g/p" };
const history = { items: [{ id: "h1", created_at: "2026-11-11T10:00:00Z", actor: { type: "user", id: "u1", name: "Alex" }, old: null, new: { revision: 1, repositories: [repo] } }] };

type RA = Agent["repository_access"];
// `server` is what GET /agents/:id returns: the list never carries repository_access.
function mockApi(put?: (body: unknown) => unknown, server: RA = null) {
  vi.mocked(api).mockImplementation((path: string, opts?: { method?: string; body?: unknown }) => {
    if (path.includes("/repository-access/history")) return Promise.resolve(history);
    if (path.endsWith("/repository-access") && opts?.method === "PUT") return Promise.resolve(put ? put(opts.body) : { revision: 1, repositories: [repo] });
    if (path === "/api/v1/agents/a1") return Promise.resolve(agent(server));
    return Promise.reject(new Error(`unexpected ${path}`));
  });
}

// The dialog hands the component the store's copy of the agent; mirror that.
function Live({ canEdit = true }: { canEdit?: boolean }) {
  const a = useAgentStore((st) => st.agents[0]) as Agent;
  return <AgentRepositoryAccess agent={a} canEdit={canEdit} />;
}

describe("AgentRepositoryAccess", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    useAgentStore.setState({ agents: [] });
  });

  it("shows legacy (null) and deny-all ([]) differently", async () => {
    mockApi(undefined, null);
    const { unmount } = render(<AgentRepositoryAccess agent={agent(null)} canEdit />);
    expect(await screen.findByTestId("repository-access-legacy")).toBeTruthy();
    expect(screen.queryByTestId("repository-access-deny-all")).toBeNull();
    unmount();
    mockApi(undefined, { revision: 2, repositories: [] });
    useAgentStore.setState({ agents: [agent(null)] });
    render(<Live />);
    expect(await screen.findByTestId("repository-access-deny-all")).toBeTruthy();
    expect(screen.queryByTestId("repository-access-legacy")).toBeNull();
    await waitFor(() => expect(screen.getByTestId("repository-access-audit-entry")).toBeTruthy());
  });

  it("saves with expected_revision 0 from legacy and shows the normalized result", async () => {
    mockApi(() => ({ revision: 1, repositories: [{ ...repo, host: "gitlab.com" }] }));
    render(<AgentRepositoryAccess agent={agent(null)} canEdit />);
    fireEvent.click(await screen.findByText("Add repository"));
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "GitLab.com" } });
    fireEvent.change(screen.getByLabelText("Project path"), { target: { value: "g/p" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => {
      const call = vi.mocked(api).mock.calls.find((c) => (c[1] as { method?: string } | undefined)?.method === "PUT");
      expect((call?.[1] as { body: unknown }).body).toEqual({
        expected_revision: 0,
        repositories: [{ provider: "gitlab", host: "GitLab.com", project_path: "g/p" }],
      });
    });
    await waitFor(() => expect((screen.getByLabelText("Host") as HTMLInputElement).value).toBe("gitlab.com"));
  });

  it("keeps the draft on 409 and offers a reload", async () => {
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string }) => {
      if (path.includes("/history")) return Promise.resolve({ items: [] });
      if (opts?.method === "PUT") return Promise.reject(Object.assign(new Error("conflict"), { status: 409 }));
      if (path === "/api/v1/agents/a1") return Promise.resolve(agent({ revision: 1, repositories: [repo] }));
      return Promise.reject(new Error("unexpected"));
    });
    render(<AgentRepositoryAccess agent={agent(null)} canEdit />);
    fireEvent.change(await screen.findByLabelText("Project path"), { target: { value: "g/other" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => expect(screen.getByTestId("repository-access-conflict")).toBeTruthy());
    expect((screen.getByLabelText("Project path") as HTMLInputElement).value).toBe("g/other");
    expect(screen.getByText("Reload from server")).toBeTruthy();
  });

  it("is read-only without edit rights", async () => {
    mockApi(undefined, { revision: 1, repositories: [repo] });
    render(<AgentRepositoryAccess agent={agent(null)} canEdit={false} />);
    expect(await screen.findByTestId("repository-access-readonly")).toBeTruthy();
    expect(screen.queryByText("Save")).toBeNull();
    await waitFor(() => expect(screen.getByTestId("repository-access-audit-entry")).toBeTruthy());
  });

  it("drops the previous agent's history when switching agents", async () => {
    mockApi();
    const { rerender } = render(<AgentRepositoryAccess agent={agent(null)} canEdit />);
    await waitFor(() => expect(screen.getByTestId("repository-access-audit-entry")).toBeTruthy());
    vi.mocked(api).mockImplementation(() => new Promise(() => {})); // next history never arrives
    rerender(<AgentRepositoryAccess agent={{ ...agent(null), id: "a2" }} canEdit />);
    await waitFor(() => expect(screen.queryByTestId("repository-access-audit-entry")).toBeNull());
  });

  it("loads the configured access on open instead of showing the list's missing field as legacy", async () => {
    mockApi(undefined, { revision: 3, repositories: [repo] });
    useAgentStore.setState({ agents: [agent()] });
    render(<Live />);
    await waitFor(() => expect((screen.getByLabelText("Project path") as HTMLInputElement).value).toBe("g/p"));
    expect(screen.queryByTestId("repository-access-legacy")).toBeNull();
    fireEvent.change(screen.getByLabelText("Project path"), { target: { value: "g/q" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => {
      const call = vi.mocked(api).mock.calls.find((c) => (c[1] as { method?: string } | undefined)?.method === "PUT");
      expect((call?.[1] as { body: { expected_revision: number } }).body.expected_revision).toBe(3);
    });
  });

  it("keeps the draft's base revision when the server moves on (CAS cannot be bypassed)", async () => {
    mockApi(undefined, { revision: 1, repositories: [repo] });
    useAgentStore.setState({ agents: [agent()] });
    render(<Live />);
    fireEvent.change(await screen.findByLabelText("Project path"), { target: { value: "g/mine" } });
    // a concurrent change arrives through the store while the draft is dirty
    act(() => useAgentStore.setState({ agents: [agent({ revision: 2, repositories: [{ ...repo, project_path: "g/theirs" }] })] }));
    expect((screen.getByLabelText("Project path") as HTMLInputElement).value).toBe("g/mine");
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => {
      const call = vi.mocked(api).mock.calls.find((c) => (c[1] as { method?: string } | undefined)?.method === "PUT");
      expect((call?.[1] as { body: { expected_revision: number } }).body.expected_revision).toBe(1);
    });
  });

  it("does not offer editing until the agent has loaded", async () => {
    vi.mocked(api).mockImplementation((path: string) => path.includes("/history") ? Promise.resolve({ items: [] }) : new Promise(() => {}));
    render(<AgentRepositoryAccess agent={agent()} canEdit />);
    expect(screen.queryByText("Save")).toBeNull();
    expect(screen.queryByTestId("repository-access-legacy")).toBeNull();
  });

  it("keeps the loaded access when an ordinary settings save replaces the agent with a PATCH response", async () => {
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string; body?: unknown }) => {
      if (path.includes("/repository-access/history")) return Promise.resolve({ items: [] });
      if (opts?.method === "PATCH") return Promise.resolve(agent()); // PATCH never carries repository_access
      if (opts?.method === "PUT") return Promise.resolve({ revision: 4, repositories: [repo] });
      if (path === "/api/v1/agents/a1") return Promise.resolve(agent({ revision: 3, repositories: [repo] }));
      return Promise.reject(new Error(`unexpected ${path}`));
    });
    useAgentStore.setState({ agents: [agent()] });
    render(<Live />);
    await waitFor(() => expect((screen.getByLabelText("Project path") as HTMLInputElement).value).toBe("g/p"));
    await act(async () => { await useAgentStore.getState().updateAgent("a1", { name: "Renamed" }); });
    expect((screen.getByLabelText("Project path") as HTMLInputElement).value).toBe("g/p");
    expect(screen.queryByTestId("repository-access-legacy")).toBeNull();
    expect(screen.getByTestId("repository-access-revision").textContent).toContain("3");
    fireEvent.change(screen.getByLabelText("Project path"), { target: { value: "g/q" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => {
      const call = vi.mocked(api).mock.calls.find((c) => (c[1] as { method?: string } | undefined)?.method === "PUT");
      expect((call?.[1] as { body: { expected_revision: number } }).body.expected_revision).toBe(3);
    });
  });
  it("shows and edits the authoritative GET result even when the prop never receives it", async () => {
    mockApi(undefined, { revision: 3, repositories: [repo] });
    render(<AgentRepositoryAccess agent={agent()} canEdit />); // static prop without repository_access
    await waitFor(() => expect((screen.getByLabelText("Project path") as HTMLInputElement).value).toBe("g/p"));
    expect(screen.queryByTestId("repository-access-legacy")).toBeNull();
    expect(screen.getByTestId("repository-access-revision").textContent).toContain("3");
  });

  it("locks the editor while a save is in flight, so input accepted during the request is never overwritten by its response", async () => {
    let release!: (v: unknown) => void;
    const pending = new Promise((r) => { release = r; });
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string }) => {
      if (path.includes("/repository-access/history")) return Promise.resolve(history);
      if (path.endsWith("/repository-access") && opts?.method === "PUT") return pending;
      if (path === "/api/v1/agents/a1") return Promise.resolve(agent({ revision: 1, repositories: [repo] }));
      return Promise.reject(new Error(`unexpected ${path}`));
    });
    render(<AgentRepositoryAccess agent={agent({ revision: 1, repositories: [repo] })} canEdit />);
    fireEvent.change(await screen.findByLabelText("Project path"), { target: { value: "g/submitted" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => expect((screen.getByLabelText("Project path") as HTMLInputElement).disabled).toBe(true));
    expect((screen.getByLabelText("Host") as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByText("Add repository").closest("button") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByLabelText("Remove repository") as HTMLButtonElement).disabled).toBe(true);
    await act(async () => { release({ revision: 2, repositories: [{ ...repo, project_path: "g/submitted" }] }); });
    await waitFor(() => expect((screen.getByLabelText("Project path") as HTMLInputElement).disabled).toBe(false));
    expect((screen.getByLabelText("Project path") as HTMLInputElement).value).toBe("g/submitted");
  });

  it("after save, closing and reopening the dialog shows the server's current value, not the store's stale copy of the saved one", async () => {
    let server: NonNullable<RA> = { revision: 1, repositories: [repo] };
    vi.mocked(api).mockImplementation((path: string, opts?: { method?: string; body?: unknown }) => {
      if (path.includes("/repository-access/history")) return Promise.resolve(history);
      if (path.endsWith("/repository-access") && opts?.method === "PUT") {
        server = { revision: server.revision + 1, repositories: (opts.body as { repositories: NonNullable<RA>["repositories"] }).repositories };
        return Promise.resolve(server);
      }
      if (path === "/api/v1/agents/a1") return Promise.resolve(agent(server));
      return Promise.reject(new Error(`unexpected ${path}`));
    });
    useAgentStore.setState({ agents: [agent(null)] });
    const first = render(<Live />);
    fireEvent.change(await screen.findByLabelText("Project path"), { target: { value: "g/saved" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => expect(screen.getByTestId("repository-access-revision").textContent).toContain("2"));
    first.unmount();

    // The store still holds revision 2 / g/saved; meanwhile another admin changed the access on the server.
    expect(useAgentStore.getState().agents[0]?.repository_access).toEqual({ revision: 2, repositories: [{ ...repo, project_path: "g/saved" }] });
    server = { revision: 5, repositories: [{ ...repo, project_path: "g/other-admin" }] };

    render(<Live />);
    await waitFor(() => expect((screen.getByLabelText("Project path") as HTMLInputElement).value).toBe("g/other-admin"));
    expect(screen.getByTestId("repository-access-revision").textContent).toContain("5");
    expect(screen.queryByDisplayValue("g/saved")).toBeNull();
  });
});
