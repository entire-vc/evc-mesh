import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { act } from "react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

import { OrgChartPage } from "@/pages/org-chart";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";
import type { Agent, OrgChartAgentNode, Workspace } from "@/types";

const WORKSPACE = { id: "ws-1", name: "Acme", slug: "acme" } as Workspace;

const node: OrgChartAgentNode = {
  id: "agent-1",
  name: "Verity",
  slug: "verity",
  status: "offline",
  agent_type: "claude_code",
  model: null,
  role: "lead",
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
};

const agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  name: "Verity",
  agent_type: "claude_code",
  status: "offline",
  role: "lead",
  model: null,
} as unknown as Agent;

describe("OrgChartPage — cards follow edits made in the detail dialog", () => {
  beforeEach(() => {
    const noop = vi.fn().mockResolvedValue(undefined);
    useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
    useRulesStore.setState({
      orgChart: { workspace: "Acme", agent_tree: [node], humans: [] },
      isOrgChartLoading: false,
      fetchOrgChart: noop,
    });
    useAgentStore.setState({ agents: [agent], fetchAgents: noop });
  });

  it("shows the saved model on the card without a reload, and drops the line when it is cleared", () => {
    render(
      <MemoryRouter>
        <OrgChartPage />
      </MemoryRouter>,
    );
    expect(screen.queryByText("gpt-6.1-sol")).toBeNull();

    // What updateAgent does on a successful PATCH: replaces the agent in `agents` only.
    act(() => {
      useAgentStore.setState({ agents: [{ ...agent, model: "gpt-6.1-sol" } as Agent] });
    });
    expect(screen.getByText("gpt-6.1-sol")).toBeTruthy();

    act(() => {
      useAgentStore.setState({ agents: [{ ...agent, model: null } as unknown as Agent] });
    });
    expect(screen.queryByText("gpt-6.1-sol")).toBeNull();
  });
  it.each(["/org-chart", "/org-chart/grid"])("adds and clears a saved tag at %s without using the role", (path) => {
    render(<MemoryRouter initialEntries={[path]}><OrgChartPage /></MemoryRouter>);
    expect(screen.getByTitle("Verity")).toHaveTextContent("Verity");
    act(() => { useAgentStore.setState({ agents: [{ ...agent, short_tag: "mesh-dev" }] }); });
    const label = screen.getByTitle("Verity · mesh-dev");
    expect(label).toHaveTextContent("Verity · mesh-dev");
    expect(label.querySelector(".text-muted-foreground.font-normal")).toBeTruthy();
    act(() => { useAgentStore.setState({ agents: [{ ...agent, short_tag: null }] }); });
    expect(screen.getByTitle("Verity").textContent).toBe("Verity");
  });

});
