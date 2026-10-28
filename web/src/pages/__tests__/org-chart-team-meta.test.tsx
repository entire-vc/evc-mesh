import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

import { OrgChartPage } from "@/pages/org-chart";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";
import type { OrgChartAgentNode, TeamDirectoryHuman, Workspace } from "@/types";

const WORKSPACE = { id: "ws-1", name: "Acme", slug: "acme" } as Workspace;

const agent: OrgChartAgentNode = {
  id: "agent-1",
  name: "Verity",
  slug: "verity",
  status: "online",
  agent_type: "claude_code",
  model: "opus",
  role: "qa",
  capabilities: ["review", "e2e", "screenshots"],
  responsibility_zone: "Acceptance of UI changes",
  escalation_to: null,
  accepts_from: [],
  max_concurrent_tasks: 2,
  working_hours: "",
  profile_description: "",
  current_tasks: 1,
  projects: ["mesh"],
  children: [],
};

const human: TeamDirectoryHuman = {
  id: "user-1",
  name: "Ann Lee",
  username: "ann",
  email: "ann@example.com",
  avatar_url: "",
  role: "owner",
  capabilities: ["product"],
  responsibility_zone: "Roadmap",
  availability: "weekdays",
  projects: ["mesh"],
};

// The Team Directory settings tab was removed; its metadata has to be on the
// Team page cards in both views or it is simply gone.
describe("OrgChartPage — Team Directory metadata on every card", () => {
  beforeEach(() => {
    const noop = vi.fn().mockResolvedValue(undefined);
    useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
    useRulesStore.setState({
      orgChart: { workspace: "Acme", agent_tree: [agent], humans: [human] },
      isOrgChartLoading: false,
      fetchOrgChart: noop,
    });
    useAgentStore.setState({ agents: [], fetchAgents: noop });
  });

  it.each(["/org-chart", "/org-chart/grid"])("shows agent and human meta at %s", (path) => {
    render(<MemoryRouter initialEntries={[path]}><OrgChartPage /></MemoryRouter>);

    expect(screen.getByText("claude_code · opus")).toBeInTheDocument();
    expect(screen.getByText("Acceptance of UI changes")).toHaveAttribute("title", "Acceptance of UI changes");
    expect(screen.getByText("review")).toBeInTheDocument();
    expect(screen.getByText("e2e")).toBeInTheDocument();
    expect(screen.queryByText("screenshots")).not.toBeInTheDocument();
    expect(screen.getByText("+1")).toBeInTheDocument();
    expect(screen.getByTitle("review, e2e, screenshots")).toBeInTheDocument();

    expect(screen.getByText("Roadmap")).toBeInTheDocument();
    expect(screen.getByText("weekdays")).toBeInTheDocument();
    expect(screen.getByText("product")).toBeInTheDocument();
  });

  it("omits empty meta instead of rendering blank rows", () => {
    useRulesStore.setState({
      orgChart: {
        workspace: "Acme",
        agent_tree: [{ ...agent, capabilities: [], responsibility_zone: "" }],
        humans: [{ ...human, capabilities: [], responsibility_zone: "", availability: "" }],
      },
    });
    render(<MemoryRouter initialEntries={["/org-chart"]}><OrgChartPage /></MemoryRouter>);
    expect(screen.queryByText("Acceptance of UI changes")).not.toBeInTheDocument();
    expect(screen.queryByText("+1")).not.toBeInTheDocument();
    expect(screen.queryByText("weekdays")).not.toBeInTheDocument();
  });
});
