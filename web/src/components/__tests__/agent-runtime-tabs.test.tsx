import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { AgentRuntimeTabs } from "@/components/agent-runtime-tabs";
import type { Agent } from "@/types";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";

const example = JSON.parse(
  // Fixture account is renamed from the contract's "preferred" (see agent-runtime.test.ts).
  readFileSync(resolve(__dirname, "../../../../docs/api/agent-runtime.example.json"), "utf8").split('"preferred"').join('"main"'),
);
const b = example.bindings["worker-b"].binding;
const agent = { id: b.agent_id, workspace_id: b.workspace_id, name: "Worker" } as unknown as Agent;
const NOW = Date.parse("2026-10-09T00:00:00Z");

const ctl = (over: Record<string, unknown> = {}, current = true) => ({
  controller_ref: "runner-a",
  report: { revision: 4, digest: "deadbeefcafe0123", status: "applied", ...over },
  received_at: new Date(NOW - 5000).toISOString(),
  current,
});

const envelope = (over: Record<string, unknown> = {}) => ({
  mode: "managed",
  revision: 4,
  digest: "deadbeefcafe0123",
  enabled: true,
  drain_requested: false,
  config: example,
  controllers: [ctl()],
  ...over,
});

const apiErr = (status: number) => Object.assign(new Error("x"), { code: "e", status });

function renderTabs(ws: string | null = b.workspace_id) {
  return render(
    <AgentRuntimeTabs
      agent={agent}
      workspaceId={ws}
      profile={<p>profile-content</p>}
      access={<p>grants-content</p>}
    />,
  );
}
const open = (name: string) => fireEvent.click(screen.getByRole("tab", { name }));

describe("AgentRuntimeTabs", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
  });

  it("shows profile by default and exposes all four tabs", () => {
    vi.mocked(api).mockResolvedValue(envelope());
    renderTabs();
    expect(screen.getByText("profile-content")).toBeInTheDocument();
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual([
      "Profile", "Workspace access", "Execution & routes", "Observed state",
    ]);
  });

  it("renders ranked routes, preferred/actual, QA policy and pools from the contract", async () => {
    vi.mocked(api).mockResolvedValue(envelope());
    renderTabs();
    open("Execution & routes");
    expect(await screen.findByText("Allowed routes, in order")).toBeInTheDocument();
    expect(screen.getByText("primary")).toBeInTheDocument();
    expect(screen.getByText("quota reserve")).toBeInTheDocument();
    expect(screen.getByText("preferred is usable")).toBeInTheDocument();
    expect(screen.getByText("strict")).toBeInTheDocument();
    expect(screen.getByText(/Quota-only fallback/).textContent).toContain("off");
    // threshold absent in the catalog -> "not set", never an invented percentage
    expect(screen.getAllByText(/threshold not set/).length).toBeGreaterThan(0);
    expect(vi.mocked(api)).toHaveBeenCalledWith(`/api/v1/workspaces/${b.workspace_id}/runtime`);
  });

  it("never renders credential references", async () => {
    vi.mocked(api).mockResolvedValue(envelope());
    const { container } = renderTabs();
    for (const t of ["Workspace access", "Execution & routes", "Observed state"]) open(t);
    await screen.findByText("Controllers");
    expect(container.ownerDocument.body.textContent).not.toMatch(/cred:prepared/);
  });

  it("observed state: applied vs stale controller, with the reason", async () => {
    vi.mocked(api).mockResolvedValue(envelope({ revision: 5 }));
    renderTabs();
    open("Observed state");
    expect(await screen.findByText("stale")).toBeInTheDocument();
    expect(screen.getByText("applied r4, desired r5")).toBeInTheDocument();
  });

  it("without a current workspace it shows the error state, not an endless skeleton", async () => {
    renderTabs(null);
    open("Observed state");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByLabelText("Loading runtime")).not.toBeInTheDocument();
    expect(vi.mocked(api)).not.toHaveBeenCalled();
  });

  it("digest mismatch at the same revision is not shown as applied", async () => {
    vi.mocked(api).mockResolvedValue(
      envelope({ controllers: [ctl({ digest: "other" })] }),
    );
    renderTabs();
    open("Observed state");
    expect(await screen.findByText("applied digest differs from desired")).toBeInTheDocument();
    expect(screen.queryByText("applied")).not.toBeInTheDocument();
  });

  it("loading state is announced while the request is pending", () => {
    vi.mocked(api).mockReturnValue(new Promise(() => {}));
    renderTabs();
    open("Execution & routes");
    expect(screen.getByLabelText("Loading runtime")).toHaveAttribute("aria-busy", "true");
  });

  it("error state offers retry and does not invent data; retry recovers", async () => {
    vi.mocked(api).mockRejectedValueOnce(apiErr(500)).mockResolvedValue(envelope());
    renderTabs();
    open("Execution & routes");
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be loaded");
    expect(screen.queryByText("Allowed routes, in order")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Allowed routes, in order")).toBeInTheDocument();
  });

  it("200 direct = empty direct-mode state; 403 = permission state; 404 = error, not direct", async () => {
    vi.mocked(api).mockResolvedValue({ mode: "direct", controllers: [] });
    const first = renderTabs();
    open("Observed state");
    expect(await screen.findByText(/direct mode/)).toBeInTheDocument();
    first.unmount();
    vi.mocked(api).mockRejectedValue(apiErr(403));
    renderTabs();
    open("Observed state");
    expect(await screen.findByText(/visible to its admins only/)).toBeInTheDocument();
  });

  it("404 is an error with retry, never presented as direct mode", async () => {
    vi.mocked(api).mockRejectedValue(apiErr(404));
    renderTabs();
    open("Observed state");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByText(/direct mode/)).not.toBeInTheDocument();
  });

  it("server `current=false` is shown stale even when revision and digest match", async () => {
    vi.mocked(api).mockResolvedValue(envelope({ controllers: [ctl({}, false)] }));
    renderTabs();
    open("Observed state");
    expect(await screen.findByText("stale")).toBeInTheDocument();
    expect(screen.getByText(/not current/)).toBeInTheDocument();
  });

  it("preferred/actual line does not repeat the word when an account is really named 'preferred'", async () => {
    const renamed = JSON.parse(JSON.stringify(example).split('"main"').join('"preferred"'));
    vi.mocked(api).mockResolvedValue(envelope({ config: renamed }));
    renderTabs();
    open("Execution & routes");
    const reason = await screen.findByText("preferred is usable");
    const line = reason.closest("li")?.textContent ?? "";
    expect(line).toContain("wanted account: preferred");
    expect(line).toContain("projected route:");
    expect(line).not.toMatch(/preferred\s+preferred/i);
  });

  it("states that receiving-workspace admission is not covered by the route view", async () => {
    vi.mocked(api).mockResolvedValue(envelope());
    renderTabs();
    open("Execution & routes");
    expect(await screen.findByText(/receiving workspace.s own admission is not part of this data/)).toBeInTheDocument();
  });

  it("tab row wraps instead of scrolling or clipping at narrow widths", () => {
    renderTabs();
    const list = screen.getByRole("tablist");
    expect(list.className).toContain("flex-wrap");
    expect(list.className).not.toContain("overflow-x");
    for (const t of screen.getAllByRole("tab")) expect(t.className).not.toContain("whitespace-nowrap");
  });

  it("shows threshold_ref and the observed pool state", async () => {
    const cfg = structuredClone(example);
    const pool = Object.keys(cfg.pools)[0] as string;
    cfg.pools[pool].threshold_ref = "thr-70";
    vi.mocked(api).mockResolvedValue(
      envelope({ config: cfg, controllers: [ctl({ pools: { [pool]: { state: "exhausted", observed_at: new Date(NOW).toISOString(), verified: true } } })] }),
    );
    renderTabs();
    open("Execution & routes");
    expect(await screen.findByText(/threshold thr-70/)).toBeInTheDocument();
    expect(screen.getAllByText(/exhausted/).length).toBeGreaterThan(0);
  });

  it("agent without a binding in this workspace gets an explicit empty state", async () => {
    vi.mocked(api).mockResolvedValue(envelope());
    render(
      <AgentRuntimeTabs agent={{ ...agent, id: "other" } as Agent} workspaceId={b.workspace_id} profile={null} access={null} />,
    );
    open("Execution & routes");
    expect(await screen.findByText(/no runtime binding/)).toBeInTheDocument();
  });

  it("a late response from the previous workspace does not overwrite the current one", async () => {
    let resolveOld!: (v: unknown) => void;
    vi.mocked(api)
      .mockReturnValueOnce(new Promise((r) => (resolveOld = r)))
      .mockResolvedValue(envelope({ revision: 9 }));
    const { rerender } = renderTabs("old-ws");
    rerender(
      <AgentRuntimeTabs agent={agent} workspaceId={b.workspace_id} profile={null} access={null} />,
    );
    open("Observed state");
    await screen.findByText("r9");
    resolveOld(envelope({ revision: 1 }));
    await new Promise((r) => setTimeout(r, 10));
    expect(screen.getByText("r9")).toBeInTheDocument();
    expect(screen.queryByText("r1")).not.toBeInTheDocument();
  });

  it("keyboard: arrows move focus and selection and wrap", async () => {
    vi.mocked(api).mockResolvedValue(envelope());
    renderTabs();
    const [t0, t1, , t3] = screen.getAllByRole("tab") as [HTMLElement, HTMLElement, HTMLElement, HTMLElement];
    t0.focus();
    fireEvent.keyDown(t0, { key: "ArrowRight" });
    await waitFor(() => expect(t1).toHaveAttribute("aria-selected", "true"));
    expect(document.activeElement).toBe(t1);
    fireEvent.keyDown(t1, { key: "ArrowRight" });
    fireEvent.keyDown(t1.nextElementSibling as HTMLElement, { key: "ArrowRight" });
    await waitFor(() => expect(t3).toHaveAttribute("aria-selected", "true"));
    fireEvent.keyDown(t3, { key: "ArrowRight" });
    await waitFor(() => expect(t0).toHaveAttribute("aria-selected", "true"));
    expect(t1).toHaveAttribute("tabindex", "-1");
  });
});
