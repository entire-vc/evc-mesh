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
});
