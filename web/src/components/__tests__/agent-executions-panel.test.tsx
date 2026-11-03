import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";
import { AgentRuntimeTabs } from "@/components/agent-runtime-tabs";
import { useProjectStore } from "@/stores/project";
import type { Project } from "@/types";
import { AGENT_1, attempt, envelope, FIXTURES, LONG_ID, PROJ_A, PROJ_B, view, WS } from "@/lib/__tests__/fixtures/agent-executions";

const serve = (items: unknown[] | Error) =>
  vi.mocked(api).mockImplementation((path: string) => {
    if (path.includes("/agent-executions")) return items instanceof Error ? Promise.reject(items) : Promise.resolve(envelope(items));
    return Promise.reject(Object.assign(new Error("n/a"), { status: 404 }));
  });

const serveDeferred = () => {
  let release: (items: unknown[]) => void = () => {};
  const gate = new Promise<unknown[]>((resolve) => { release = resolve; });
  vi.mocked(api).mockImplementation((path: string) =>
    path.includes("/agent-executions") ? gate.then((items) => envelope(items)) : Promise.reject(Object.assign(new Error("n/a"), { status: 404 })));
  return release;
};

const open = async () => {
  render(<AgentRuntimeTabs agent={{ id: AGENT_1 }} workspaceId={WS} profile={<p>profile-content</p>} access={<p>grants</p>} />);
  fireEvent.click(screen.getByRole("tab", { name: "Route & runs" }));
};

describe("AgentRuntimeTabs — Route & runs", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    useProjectStore.setState({ projects: [{ id: PROJ_A, name: "Mesh" }, { id: PROJ_B, name: "Spark" }] as Project[] });
  });

  it("queries the projection for this agent only", async () => {
    serve([FIXTURES.managedCurrent]);
    await open();
    await screen.findByTestId("exec-configured");
    const path = vi.mocked(api).mock.calls.map((c) => c[0] as string).find((p) => p.includes("/agent-executions"))!;
    expect(path).toContain(`agent_id=${AGENT_1}`);
  });

  it("shows configured policy with revision apart from the observed run, same identity as the chart", async () => {
    serve([FIXTURES.managedCurrent]);
    await open();
    const cfg = await screen.findByTestId("exec-configured");
    expect(cfg).toHaveTextContent("Policy r17");
    expect(cfg).toHaveTextContent("gpt-6.1-sol");
    const att = screen.getByTestId("exec-attempt");
    expect(att).toHaveTextContent("gpt-6.1-sol · high");
    expect(att).toHaveTextContent("runtime_policy r17");
    expect(att).toHaveTextContent("att-1");
  });

  it("shows loading for a delayed projection, no configured/observed data until it arrives", async () => {
    const release = serveDeferred();
    await open();
    expect(await screen.findByLabelText("Loading runs")).toHaveAttribute("aria-busy", "true");
    expect(screen.queryByTestId("exec-configured")).not.toBeInTheDocument();
    expect(screen.queryByTestId("exec-attempt")).not.toBeInTheDocument();
    release([FIXTURES.managedCurrent]);
    const cfg = await screen.findByTestId("exec-configured");
    expect(screen.queryByLabelText("Loading runs")).not.toBeInTheDocument();
    expect(cfg).toHaveTextContent("Policy r17");
    expect(screen.getByTestId("exec-attempt")).toHaveTextContent("gpt-6.1-sol · high");
  });

  it("lists both concurrent attempts", async () => {
    serve([FIXTURES.twoAttemptsTwoModels]);
    await open();
    await screen.findByTestId("exec-rows");
    const rows = screen.getAllByTestId("exec-attempt");
    expect(rows).toHaveLength(2);
    expect(rows.map((r) => r.textContent).join(" ")).toMatch(/gpt-6\.1-sol.*opus-5/);
  });

  it("flags a run that selected an older policy revision", async () => {
    serve([FIXTURES.policyMismatch]);
    await open();
    expect(await screen.findByText(/This run selected policy r17; the current policy is r18/)).toBeInTheDocument();
  });

  it("no usage: model is unknown, never the configured or profile one", async () => {
    serve([FIXTURES.managedModelUnknown]);
    await open();
    const att = await screen.findByTestId("exec-attempt");
    expect(att).toHaveTextContent("Model unknown");
    expect(att).not.toHaveTextContent("sonnet");
    serve([FIXTURES.managedNoUsage]);
  });

  it("keeps the observation source visible when the timestamp is missing", async () => {
    serve([view({ attempts: [attempt({ observed: { harness: "codex", model: null, effort: null, source: "adapter", observed_at: null }, freshness: "unknown" })] })]);
    await open();
    expect((await screen.findAllByText(/not observed for this turn \(source adapter\)/)).length).toBeGreaterThan(0);
  });

  it("separates history from active and marks offline", async () => {
    serve([view({ attempts: [attempt({ attempt_id: "live" }), attempt({ attempt_id: "done", state: "completed" }), attempt({ attempt_id: "gone", freshness: "offline" })] })]);
    await open();
    const rows = await screen.findByTestId("exec-rows");
    expect(within(rows).getByText("Active (1)")).toBeInTheDocument();
    expect(within(rows).getByText("History (2)")).toBeInTheDocument();
    expect(within(rows).getByText("offline")).toBeInTheDocument();
  });

  it("unmanaged/disabled show the profile with its source and no route", async () => {
    serve([FIXTURES.unmanaged]);
    await open();
    const box = await screen.findByTestId("exec-unmanaged");
    expect(box).toHaveTextContent("agent_profile");
    expect(box).toHaveTextContent("claude · sonnet");
    expect(screen.queryByTestId("exec-configured")).not.toBeInTheDocument();
  });

  it("error is an error with retry, not unmanaged", async () => {
    serve(Object.assign(new Error("x"), { status: 500 }));
    await open();
    expect(await screen.findByRole("alert")).toHaveTextContent("Run data unavailable");
    expect(screen.queryByTestId("exec-unmanaged")).not.toBeInTheDocument();
    serve([FIXTURES.managedCurrent]);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByTestId("exec-configured")).toBeInTheDocument();
  });

  it("long ids wrap and are copyable", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    serve([view({ attempts: [attempt({ attempt_id: LONG_ID })] })]);
    await open();
    const id = await screen.findByText(LONG_ID);
    expect(id.className).toContain("break-all");
    fireEvent.click(screen.getByRole("button", { name: "Copy run" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(LONG_ID));
  });

  it(">20 runs page by 20 and the last one is reachable; filters narrow the list", async () => {
    const many = Array.from({ length: 45 }, (_, i) => attempt({ attempt_id: `run-${String(i).padStart(2, "0")}`, heartbeat_at: new Date(Date.UTC(2026, 9, 30, 12, 0, 45 - i)).toISOString() }));
    serve([view({ project_id: PROJ_A, attempts: many }), view({ project_id: PROJ_B, attempts: [attempt({ attempt_id: "spark-run" })] })]);
    await open();
    await screen.findByTestId("exec-rows");
    expect(screen.getAllByTestId("exec-attempt")).toHaveLength(20);
    fireEvent.click(screen.getByRole("button", { name: "Show 20 more" }));
    fireEvent.click(screen.getByRole("button", { name: "Show 6 more" }));
    expect(screen.getAllByTestId("exec-attempt")).toHaveLength(46);
    expect(screen.getByText("run-44")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Project"), { target: { value: PROJ_B } });
    expect(screen.getAllByTestId("exec-attempt")).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("Project"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Search by run, task, controller or model"), { target: { value: "spark-run" } });
    expect(screen.getAllByTestId("exec-attempt")).toHaveLength(1);
  });

  it("profile tab explains that editing does not change a managed route", async () => {
    serve([FIXTURES.managedCurrent]);
    render(<AgentRuntimeTabs agent={{ id: AGENT_1 }} workspaceId={WS} profile={<p>profile-content</p>} access={<p>grants</p>} />);
    expect(screen.getByText(/does not change a managed route/)).toBeInTheDocument();
    expect(screen.getByText("profile-content")).toBeInTheDocument();
  });
});
