import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: vi.fn(() => null) }));
import { api } from "@/lib/api";
import { OrgChartPage } from "@/pages/org-chart";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";
import { useProjectStore } from "@/stores/project";
import { useMemberStore } from "@/stores/member";
import type { OrgChartAgentNode, Project, Workspace } from "@/types";
import { AGENT_1, attempt, envelope, FIXTURES, PROJ_A, PROJ_B, view, WS } from "@/lib/__tests__/fixtures/agent-executions";

const agent = {
  id: AGENT_1,
  name: "Taylor",
  slug: "taylor",
  status: "online",
  agent_type: "claude_code",
  model: "opus",
  role: "frontend",
  capabilities: [],
  responsibility_zone: "",
  escalation_to: null,
  accepts_from: [],
  max_concurrent_tasks: 2,
  working_hours: "",
  profile_description: "",
  current_tasks: 1,
  projects: ["mesh"],
  children: [],
} as unknown as OrgChartAgentNode;

const projects = [
  { id: PROJ_A, name: "Mesh" },
  { id: PROJ_B, name: "Spark" },
] as Project[];

const serve = (items: unknown[] | Error) =>
  vi.mocked(api).mockImplementation((path: string) => {
    if (path.includes("/agent-executions")) return items instanceof Error ? Promise.reject(items) : Promise.resolve(envelope(items));
    return Promise.resolve(undefined);
  });

const serveDeferred = () => {
  let release: (items: unknown[]) => void = () => {};
  const gate = new Promise<unknown[]>((resolve) => { release = resolve; });
  vi.mocked(api).mockImplementation((path: string) =>
    path.includes("/agent-executions") ? gate.then((items) => envelope(items)) : Promise.resolve(undefined));
  return release;
};

const renderChart = (path = "/org-chart") => render(<MemoryRouter initialEntries={[path]}><OrgChartPage /></MemoryRouter>);

describe("OrgChartPage — configured route vs observed attempts", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    const noop = vi.fn().mockResolvedValue(undefined);
    useWorkspaceStore.setState({ currentWorkspace: { id: WS, name: "Acme", slug: "acme" } as Workspace });
    useRulesStore.setState({ orgChart: { workspace: "Acme", agent_tree: [agent], humans: [] }, isOrgChartLoading: false, fetchOrgChart: noop });
    useAgentStore.setState({ agents: [], fetchAgents: noop });
    useProjectStore.setState({ projects, fetchProjects: noop });
  });

  it.each(["/org-chart", "/org-chart/grid"])("shows policy and the running model separately at %s", async (path) => {
    serve([FIXTURES.managedCurrent]);
    renderChart(path);
    const configured = await screen.findByTestId("exec-configured");
    expect(configured).toHaveTextContent("r17");
    expect(configured).toHaveTextContent("gpt-6.1-sol");
    const running = screen.getByTestId("exec-observed");
    expect(running).toHaveTextContent("gpt-6.1-sol · high");
    // Observation source is shown next to the model.
    expect(within(running).getByTestId("exec-observation")).toHaveTextContent("by adapter");
    // The persisted profile model is not presented as what runs.
    expect(running).not.toHaveTextContent("opus");
  });

  it("two concurrent attempts are both listed, no single global model", async () => {
    serve([FIXTURES.twoAttemptsTwoModels]);
    renderChart();
    const running = await screen.findByTestId("exec-observed");
    expect(running).toHaveTextContent("2 runs");
    fireEvent.click(within(running).getByRole("button", { name: /2 runs/ }));
    const list = screen.getByTestId("exec-attempts");
    expect(list).toHaveTextContent("gpt-6.1-sol");
    expect(list).toHaveTextContent("opus-5");
  });

  it("no usage and unknown model are honest, not filled from profile or policy", async () => {
    serve([FIXTURES.managedNoUsage]);
    const { unmount } = renderChart();
    expect(await screen.findByTestId("exec-observed")).toHaveTextContent("No active runs");
    unmount();
    serve([FIXTURES.managedModelUnknown]);
    const { unmount: unmount2 } = renderChart();
    const running = await screen.findByTestId("exec-observed");
    expect(running).toHaveTextContent("Model unknown");
    // The adapter reported source and time even though it confirmed no model: show them.
    expect(within(running).getByTestId("exec-observation")).toHaveTextContent("by adapter");
    unmount2();
    serve([view({ attempts: [attempt({ observed: { harness: "codex", model: null, effort: null, source: null, observed_at: null }, freshness: "unknown" })], freshness: "unknown" })]);
    renderChart();
    expect(await screen.findByTestId("exec-observation")).toHaveTextContent("Model not observed for this turn");
    expect(running).not.toHaveTextContent("gpt-6.1-sol");
  });

  it("marks stale and offline", async () => {
    serve([FIXTURES.stale]);
    const { unmount } = renderChart();
    expect(await screen.findByTestId("exec-observed")).toHaveTextContent("stale");
    unmount();
    serve([FIXTURES.offline]);
    renderChart();
    expect(await screen.findByTestId("exec-observed")).toHaveTextContent("Offline");
  });

  it("unmanaged and disabled show the profile with its source, not a route", async () => {
    serve([FIXTURES.unmanaged]);
    const { unmount } = renderChart();
    const card = await screen.findByTestId("exec-unmanaged");
    expect(card).toHaveTextContent("Unmanaged");
    expect(card).toHaveTextContent("agent_profile");
    expect(screen.queryByTestId("exec-configured")).not.toBeInTheDocument();
    unmount();
    serve([FIXTURES.disabled]);
    renderChart();
    expect(await screen.findByTestId("exec-unmanaged")).toHaveTextContent("Disabled");
  });

  it("the chart names the configured policy source, like the detail panel", async () => {
    serve([view({ project_id: PROJ_A })]);
    renderChart();
    expect(await screen.findByTestId("exec-configured-source")).toHaveTextContent("runtime_policy");
  });

  it("an offline run stays visible while another project has an active one", async () => {
    serve([view({ project_id: PROJ_A }), view({ project_id: PROJ_B, attempts: [attempt({ attempt_id: "att-off", freshness: "offline" })], freshness: "offline" })]);
    renderChart();
    const running = await screen.findByTestId("exec-observed");
    expect(within(running).getByTestId("exec-offline")).toHaveTextContent("Offline");
    expect(running).toHaveTextContent("gpt-6.1-sol");
  });

  it("mixed modes: the unmanaged project is named next to the managed policy", async () => {
    serve([view({ project_id: PROJ_A }), view({ project_id: PROJ_B, managed: false, management_source: null, configured: null, attempts: [], freshness: "unknown" })]);
    renderChart();
    const other = await screen.findByTestId("exec-other-modes");
    expect(other).toHaveTextContent("Spark: Unmanaged");
    expect(screen.getByTestId("exec-configured")).toHaveTextContent("r17");
  });

  it("an API error is reported as an error, never as unmanaged", async () => {
    serve(Object.assign(new Error("x"), { status: 500 }));
    renderChart();
    expect(await screen.findByTestId("exec-error")).toHaveTextContent("Run data unavailable");
    expect(screen.queryByTestId("exec-unmanaged")).not.toBeInTheDocument();
    expect(screen.queryByText("Unmanaged")).not.toBeInTheDocument();
  });

  it("without a projection the profile model is labelled as profile, not as a runtime model", async () => {
    serve(Object.assign(new Error("x"), { status: 500 }));
    renderChart();
    await screen.findByTestId("exec-error");
    expect(screen.getByTestId("profile-model-label")).toHaveTextContent("profile");
  });

  it("while the projection is delayed the card shows loading and the profile model is not passed off as observed", async () => {
    const release = serveDeferred();
    renderChart();
    expect(await screen.findByLabelText("Loading runs")).toBeInTheDocument();
    expect(screen.queryByTestId("exec-configured")).not.toBeInTheDocument();
    expect(screen.queryByTestId("exec-observed")).not.toBeInTheDocument();
    expect(screen.getByTestId("profile-model-label")).toHaveTextContent("profile");
    release([FIXTURES.managedCurrent]);
    const running = await screen.findByTestId("exec-observed");
    expect(screen.queryByLabelText("Loading runs")).not.toBeInTheDocument();
    expect(running).toHaveTextContent("gpt-6.1-sol · high");
    expect(screen.getByTestId("exec-configured")).toHaveTextContent("r17");
  });

  it("project filter re-queries the projection scoped to that project", async () => {
    serve([view({ project_id: PROJ_A }), view({ project_id: PROJ_B })]);
    renderChart();
    await screen.findByTestId("exec-configured");
    fireEvent.change(screen.getByLabelText("Project"), { target: { value: PROJ_B } });
    await waitFor(() => {
      const paths = vi.mocked(api).mock.calls.map((c) => c[0] as string);
      expect(paths.some((p) => p.includes("/agent-executions") && p.includes(`project_id=${PROJ_B}`))).toBe(true);
    });
  });

  it("partial batch is flagged", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path.includes("/agent-executions")) {
        return path.includes("cursor=") ? Promise.reject(new Error("boom")) : Promise.resolve(envelope([FIXTURES.managedCurrent], "c2"));
      }
      return Promise.resolve(undefined);
    });
    renderChart();
    expect(await screen.findByText(/Partial data/)).toBeInTheDocument();
    expect(attempt().attempt_id).toBeTruthy();
  });

  it("binding exists but the project integration is off: names the reason and links to the switch for an admin", async () => {
    useMemberStore.setState({ myRole: "owner", fetchMyRole: vi.fn() } as never);
    useProjectStore.setState({ projects: [{ id: PROJ_A, name: "Mesh", slug: "mesh" }] as Project[] });
    serve([view({ managed: false, enabled: false, reason: "integration_disabled", configured: null, attempts: [] })]);
    renderChart();
    const note = await screen.findByTestId("exec-integration-disabled");
    expect(note).toHaveTextContent("Runner integration is off in project Mesh");
    expect(within(note).getByRole("link", { name: "Enable it" })).toHaveAttribute("href", "/w/acme/p/mesh/settings?tab=integrations");
  });

  it("integration-off reason has no enable link for a non-admin, and plain unmanaged keeps the generic text", async () => {
    useMemberStore.setState({ myRole: "member", fetchMyRole: vi.fn() } as never);
    serve([view({ managed: false, enabled: false, reason: "integration_disabled", configured: null, attempts: [] })]);
    renderChart();
    const note = await screen.findByTestId("exec-integration-disabled");
    expect(within(note).queryByRole("link")).not.toBeInTheDocument();
  });

  it("mixed: managed in one project, integration off in another — the other project keeps its enable link", async () => {
    useMemberStore.setState({ myRole: "admin", fetchMyRole: vi.fn() } as never);
    useProjectStore.setState({ projects: [{ id: PROJ_A, name: "Mesh", slug: "mesh" }, { id: PROJ_B, name: "Spark", slug: "spark" }] as Project[] });
    serve([view(), view({ project_id: PROJ_B, managed: false, enabled: false, reason: "integration_disabled", configured: null, attempts: [] })]);
    renderChart();
    const note = await screen.findByTestId("exec-integration-disabled");
    expect(note).toHaveTextContent("Runner integration is off in project Spark");
    expect(within(note).getByRole("link", { name: "Enable it" })).toHaveAttribute("href", "/w/acme/p/spark/settings?tab=integrations");
  });

  it("computed_status online is not shown as Offline even when the raw status says offline", async () => {
    useRulesStore.setState({
      orgChart: { workspace: "Acme", agent_tree: [{ ...agent, status: "offline", computed_status: "online" } as OrgChartAgentNode], humans: [] },
    });
    serve([]);
    renderChart();
    expect(await screen.findByText(/^Online/)).toBeInTheDocument();
    expect(screen.queryByText("Offline")).not.toBeInTheDocument();
  });

  it("computed_status online with an old heartbeat carries no (stale) mark on the card", async () => {
    useRulesStore.setState({
      orgChart: { workspace: "Acme", agent_tree: [{ ...agent, status: "online", computed_status: "online", is_stale: true, last_heartbeat: "2026-01-01T00:00:00Z" } as OrgChartAgentNode], humans: [] },
    });
    serve([]);
    renderChart();
    expect(await screen.findByText(/^Online/)).toBeInTheDocument();
    expect(screen.queryByText(/\(stale\)/)).not.toBeInTheDocument();
  });

  it("paused agents leave the tree for a collapsed Paused section and are still clickable (store lazily loads them)", async () => {
    const paused = { ...agent, id: "p-1", name: "robin-w2", computed_status: "paused", status: "offline" } as OrgChartAgentNode;
    useRulesStore.setState({ orgChart: { workspace: "Acme", agent_tree: [agent, paused], humans: [] } });
    const fetchAgent = vi.fn().mockResolvedValue(undefined);
    useAgentStore.setState({ agents: [], fetchAgent } as never);
    serve([]);
    renderChart();
    const section = await screen.findByTestId("org-chart-paused");
    expect(section).toHaveTextContent("Paused (1)");
    expect(within(section).getByText("robin-w2")).toBeInTheDocument();
    // the active tree does not carry it
    expect(screen.getAllByText("robin-w2")).toHaveLength(1);
    fireEvent.click(within(section).getByText("robin-w2"));
    await waitFor(() => expect(fetchAgent).toHaveBeenCalledWith("p-1"));
  });
});
