import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => vi.fn() };
});

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

import { api } from "@/lib/api";
import { WorkspaceSettingsPage } from "@/pages/workspace-settings";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import { useAgentStore } from "@/stores/agent";
import { useAgentWorkspaceGrantStore } from "@/stores/agent-workspace-grant";
import type { AgentWorkspaceGrantWithAgent, User, Workspace, WorkspaceRole } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const WORKSPACE: Workspace = {
  id: "ws1",
  name: "Acme",
  slug: "acme",
  owner_id: "u1",
  settings: {},
  billing_plan_id: "free",
  billing_customer_id: "",
  icon_url: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const USER: User = {
  id: "u1",
  email: "owner@example.com",
  name: "Owner",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const HOME_GRANT: AgentWorkspaceGrantWithAgent = {
  id: "grant-home",
  agent_id: "agent-home",
  workspace_id: "ws1",
  role: "admin",
  api_key_prefix: "agk_ws1_aaaa",
  created_at: "2026-01-01T00:00:00Z",
  agent: { id: "agent-home", name: "HomeBot", slug: "home-bot" },
};

const GUEST_GRANT: AgentWorkspaceGrantWithAgent = {
  id: "grant-guest",
  agent_id: "agent-guest",
  workspace_id: "ws1",
  role: "member",
  api_key_prefix: "agk_ws1_bbbb",
  created_at: "2026-01-02T00:00:00Z",
  agent: { id: "agent-guest", name: "GuestBot", slug: "guest-bot" },
};

function renderPage() {
  return render(
    <MemoryRouter>
      <WorkspaceSettingsPage />
    </MemoryRouter>,
  );
}

// Same "route table" shape as workspace-settings-danger-zone.test.tsx: this
// page fires several fetches unconditionally on mount, every one of which
// already catches its own errors, so a blanket {} default is harmless
// except for the routes a given test cares about.
function mockApiByRoute(
  role: WorkspaceRole,
  overrides: Record<string, unknown> = {},
) {
  mockedApi.mockImplementation((url: string) => {
    if (url === "/api/v1/workspaces/ws1/members/me") {
      return Promise.resolve({ role });
    }
    if (url in overrides) {
      return Promise.resolve(overrides[url]);
    }
    return Promise.resolve({});
  });
}

beforeEach(() => {
  mockedApi.mockReset();
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE, workspaces: [WORKSPACE] });
  useAuthStore.setState({ user: USER, isAuthenticated: true, isLoading: false });
  useAgentStore.setState({ agents: [], isLoading: false });
  useAgentWorkspaceGrantStore.setState({
    workspaceAgentGrants: [],
    isLoadingWorkspaceAgentGrants: false,
    agentWorkspaces: [],
    isLoadingAgentWorkspaces: false,
    agentWorkspacesForbidden: false,
  });
});
afterEach(() => vi.clearAllMocks());

async function openMembersTab() {
  renderPage();
  await waitFor(() => expect(mockedApi).toHaveBeenCalled());
  fireEvent.click(screen.getByRole("button", { name: "Members" }));
  await screen.findByText("Manage who has access to this workspace");
}

describe("WorkspaceSettingsPage — Invite Agent button gating (task U4 AC2)", () => {
  it("does not render Invite Agent for a member", async () => {
    mockApiByRoute("member");
    await openMembersTab();
    expect(screen.queryByRole("button", { name: "Invite Agent" })).not.toBeInTheDocument();
  });

  it("does not render Invite Agent for a viewer", async () => {
    mockApiByRoute("viewer");
    await openMembersTab();
    expect(screen.queryByRole("button", { name: "Invite Agent" })).not.toBeInTheDocument();
  });

  it("renders Invite Agent for an admin", async () => {
    mockApiByRoute("admin");
    await openMembersTab();
    expect(screen.getByRole("button", { name: "Invite Agent" })).toBeInTheDocument();
  });

  it("renders Invite Agent for an owner", async () => {
    mockApiByRoute("owner");
    await openMembersTab();
    expect(screen.getByRole("button", { name: "Invite Agent" })).toBeInTheDocument();
  });
});

describe("WorkspaceSettingsPage — connected agents list (task U4 AC1/AC2)", () => {
  it("shows a Home badge with no revoke button on the home row, and a revoke button on the guest row", async () => {
    mockApiByRoute("owner", {
      "/api/v1/workspaces/ws1/agents": { items: [{ id: "agent-home", name: "HomeBot" }], total: 1 },
      "/api/v1/workspaces/ws1/agent-grants": {
        agent_grants: [HOME_GRANT, GUEST_GRANT],
        count: 2,
      },
    });

    await openMembersTab();

    await screen.findByText("HomeBot");
    expect(screen.getByText("GuestBot")).toBeInTheDocument();
    expect(screen.getByText("Home")).toBeInTheDocument();

    // Exactly one revoke control among the two agent rows — the home row's
    // "Home" badge stands in its place, matching the member list's own
    // "can't remove the last owner" pattern of omitting the destructive
    // control rather than rendering it disabled.
    const revokeButtons = screen.getAllByTitle("Revoke connection");
    expect(revokeButtons).toHaveLength(1);
  });

  it("opens a confirmation naming the consequence before revoking a guest connection, and removes the row on confirm", async () => {
    mockApiByRoute("owner", {
      "/api/v1/workspaces/ws1/agents": { items: [{ id: "agent-home", name: "HomeBot" }], total: 1 },
      "/api/v1/workspaces/ws1/agent-grants": {
        agent_grants: [HOME_GRANT, GUEST_GRANT],
        count: 2,
      },
    });

    await openMembersTab();
    await screen.findByText("GuestBot");

    fireEvent.click(screen.getByTitle("Revoke connection"));

    await screen.findByText(/GuestBot.*stop working immediately/i);

    mockedApi.mockImplementation((url: string, opts?: { method?: string }) => {
      if (url === "/api/v1/workspaces/ws1/agent-grants/grant-guest" && opts?.method === "DELETE") {
        return Promise.resolve(undefined);
      }
      if (url === "/api/v1/workspaces/ws1/members/me") {
        return Promise.resolve({ role: "owner" });
      }
      return Promise.resolve({});
    });

    fireEvent.click(screen.getByRole("button", { name: "Revoke Connection" }));

    await waitFor(() => expect(screen.queryByText("GuestBot")).not.toBeInTheDocument());
    // Home row is unaffected.
    expect(screen.getByText("HomeBot")).toBeInTheDocument();
  });
});
