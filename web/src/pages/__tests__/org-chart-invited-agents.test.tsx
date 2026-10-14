import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

import { api } from "@/lib/api";
import { OrgChartPage } from "@/pages/org-chart";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";
import { useMemberStore } from "@/stores/member";
import { useAgentWorkspaceGrantStore } from "@/stores/agent-workspace-grant";
import type { OrgChartAgentNode, Workspace, WorkspaceRole } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const WORKSPACE = { id: "ws-1", name: "Acme", slug: "acme" } as Workspace;

function mkNode(over: Partial<OrgChartAgentNode>): OrgChartAgentNode {
  return {
    id: "a",
    name: "A",
    slug: "a",
    status: "offline",
    agent_type: "claude_code",
    model: null,
    role: "",
    capabilities: [],
    responsibility_zone: "",
    escalation_to: null,
    accepts_from: [],
    max_concurrent_tasks: 0,
    working_hours: "",
    profile_description: "",
    current_tasks: 0,
    projects: [],
    children: [],
    is_home: true,
    ...over,
  };
}

const HOME = mkNode({ id: "home-1", name: "Homebot", slug: "homebot" });
const GUEST = mkNode({
  id: "guest-1",
  name: "Kidcash",
  slug: "kidcash",
  role: "Finance helper",
  is_home: false,
});

const GRANT = {
  id: "grant-1",
  agent_id: "guest-1",
  workspace_id: "ws-1",
  role: "member",
  api_key_prefix: "agk_ws1_kc",
  created_at: "2026-10-12T00:00:00Z",
  agent: { id: "guest-1", name: "Kidcash", slug: "kidcash" },
};

const NEW_KEY = "agk_NEWKEY_visible_once";

function setup(role: WorkspaceRole, opts: { inviteFails?: boolean } = {}) {
  const noop = vi.fn().mockResolvedValue(undefined);
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
  useRulesStore.setState({
    orgChart: { workspace: "Acme", agent_tree: [HOME, GUEST], humans: [] },
    isOrgChartLoading: false,
    fetchOrgChart: noop,
  });
  useAgentStore.setState({ agents: [], fetchAgents: noop });
  useMemberStore.setState({ myRole: role, fetchMyRole: noop });
  useAgentWorkspaceGrantStore.setState({ workspaceAgentGrants: [] });

  mockedApi.mockReset();
  mockedApi.mockImplementation((url: string, init?: { method?: string }) => {
    const method = init?.method ?? "GET";
    if (url === "/api/v1/workspaces/ws-1/agent-grants" && method === "GET") {
      return Promise.resolve({ agent_grants: [GRANT], count: 1 });
    }
    if (url === "/api/v1/workspaces/ws-1/agent-grants/grant-1" && method === "DELETE") {
      return Promise.resolve(undefined);
    }
    if (url === "/api/v1/workspaces/ws-1/agent-grants" && method === "POST") {
      return opts.inviteFails
        ? Promise.reject(new Error("boom"))
        : Promise.resolve({ ...GRANT, api_key: NEW_KEY });
    }
    return Promise.resolve({});
  });
  return noop;
}

function renderPage() {
  return render(
    <MemoryRouter>
      <OrgChartPage />
    </MemoryRouter>,
  );
}

async function openGuest() {
  renderPage();
  fireEvent.click(screen.getByText("Kidcash"));
  fireEvent.click(await screen.findByRole("tab", { name: "Workspace access" }));
  await screen.findByText("agk_ws1_kc…");
}

describe("OrgChartPage — invited agents", () => {
  beforeEach(() => setup("owner"));

  it("marks only the guest card as Invited", () => {
    renderPage();
    expect(screen.getAllByText("Invited")).toHaveLength(1);
    expect(screen.getByText("Kidcash")).toBeTruthy();
    expect(screen.getByText("Homebot")).toBeTruthy();
  });

  it("shows no Invited mark when the payload has no guests (old API)", () => {
    useRulesStore.setState({
      orgChart: { workspace: "Acme", agent_tree: [{ ...HOME, is_home: undefined }], humans: [] },
    });
    renderPage();
    expect(screen.queryByText("Invited")).toBeNull();
  });

  it("opens the guest card with grant info and the manage buttons for an owner", async () => {
    await openGuest();
    expect(screen.getByRole("button", { name: /Reissue key/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Revoke$/ })).toBeTruthy();
  });

  it("revokes only after confirmation", async () => {
    await openGuest();
    fireEvent.click(screen.getByRole("button", { name: /^Revoke$/ }));
    expect(
      mockedApi.mock.calls.some(([, i]) => (i as { method?: string } | undefined)?.method === "DELETE"),
    ).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Revoke Connection" }));
    await waitFor(() =>
      expect(mockedApi).toHaveBeenCalledWith(
        "/api/v1/workspaces/ws-1/agent-grants/grant-1",
        { method: "DELETE" },
      ),
    );
  });

  it("reissue = revoke then re-invite with the same role, key shown once and not kept", async () => {
    await openGuest();
    fireEvent.click(screen.getByRole("button", { name: /Reissue key/ }));
    fireEvent.click(screen.getByRole("button", { name: "Reissue Key" }));
    expect(await screen.findByText(NEW_KEY)).toBeTruthy();

    const calls = mockedApi.mock.calls.map(([u, i]) => `${(i as { method?: string } | undefined)?.method ?? "GET"} ${u}`);
    const del = calls.indexOf("DELETE /api/v1/workspaces/ws-1/agent-grants/grant-1");
    const post = calls.indexOf("POST /api/v1/workspaces/ws-1/agent-grants");
    expect(del).toBeGreaterThan(-1);
    expect(post).toBeGreaterThan(del);
    expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/agent-grants", {
      method: "POST",
      body: { agent_id: "guest-1", role: "member" },
    });
    // The key never leaves component state: not in any URL.
    expect(calls.some((c) => c.includes(NEW_KEY))).toBe(false);
  });

  it("if re-invite fails after the revoke, says so and offers Retry", async () => {
    setup("owner", { inviteFails: true });
    await openGuest();
    fireEvent.click(screen.getByRole("button", { name: /Reissue key/ }));
    fireEvent.click(screen.getByRole("button", { name: "Reissue Key" }));
    expect(await screen.findByText(/previous key is already revoked/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});

describe.each<WorkspaceRole>(["member", "viewer"])("OrgChartPage — invited agents as %s", (role) => {
  beforeEach(() => setup(role));

  it("sees the mark and the card but no manage buttons", async () => {
    renderPage();
    expect(screen.getAllByText("Invited")).toHaveLength(1);
    fireEvent.click(screen.getByText("Kidcash"));
    fireEvent.click(await screen.findByRole("tab", { name: "Workspace access" }));
    await screen.findByText("agk_ws1_kc…");
    expect(screen.queryByRole("button", { name: /Reissue key/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Revoke$/ })).toBeNull();
  });
});
