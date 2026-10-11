import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { AgentRuntimeTabs } from "@/components/agent-runtime-tabs";
import type { Agent } from "@/types";
import { orderQuotaEdges } from "@/components/agent-runtime-policy-editor";

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

  describe("router decision preview", () => {
    const previewBody = {
      selected_profile_id: "main-sub", selected_account_ref: "main", preferred_account_ref: "main",
      reason: "preferred_available", trace: [{ profile_id: "reserve", reason: "not_needed" }],
      revision: 4, launch_authorized: false, qa_mode: "strict", qa_compromise: false,
    };
    const ask = async (previewResponse: unknown) => {
      vi.mocked(api).mockImplementation(async (url: string) => {
        if (String(url).includes("/preview")) {
          if (previewResponse instanceof Error) throw previewResponse;
          return previewResponse;
        }
        return envelope();
      });
      renderTabs();
      open("Execution & routes");
      fireEvent.click(await screen.findByRole("button", { name: "Explain routing now" }));
    };

    it("POSTs the preview for this binding and shows selected profile, account and reason", async () => {
      await ask(previewBody);
      const box = await screen.findByTestId("runtime-preview");
      expect(box.textContent).toContain("main-sub");
      expect(box.textContent).toContain("preferred_available");
      expect(box.textContent).toContain("reserve: not_needed");
      const call = vi.mocked(api).mock.calls.find((c) => String(c[0]).includes("/preview"))!;
      expect(String(call[0])).toContain(`/runtime/bindings/worker-b/preview?resource_owner_workspace_id=${b.workspace_id}`);
      expect((call[1] as { method: string }).method).toBe("POST");
    });

    it("403 is a refusal, not a result", async () => {
      await ask(apiErr(403));
      expect(await screen.findByText(/not allowed to see/)).toBeInTheDocument();
      expect(screen.queryByTestId("runtime-preview")).toBeNull();
    });

    it("409 asks to reload instead of showing a stale decision", async () => {
      await ask(apiErr(409));
      expect(await screen.findByText(/configuration changed meanwhile/)).toBeInTheDocument();
      expect(screen.queryByTestId("runtime-preview")).toBeNull();
    });

    it("a body that is not the contract shape is not shown as success", async () => {
      await ask({ ok: true });
      expect(await screen.findByText(/could not be loaded/)).toBeInTheDocument();
      expect(screen.queryByTestId("runtime-preview")).toBeNull();
    });

    it("a partial body (reason + trace only) is not shown as success", async () => {
      await ask({ reason: "profile_available", trace: [] });
      expect(await screen.findByText(/could not be loaded/)).toBeInTheDocument();
      expect(screen.queryByTestId("runtime-preview")).toBeNull();
    });

    it("a preview claiming launch_authorized=true is not shown as success", async () => {
      await ask({ ...previewBody, launch_authorized: true });
      expect(await screen.findByText(/could not be loaded/)).toBeInTheDocument();
      expect(screen.queryByTestId("runtime-preview")).toBeNull();
    });

    it("a malformed trace entry rejects the whole body", async () => {
      await ask({ ...previewBody, trace: [{ profile_id: "reserve" }] });
      expect(await screen.findByText(/could not be loaded/)).toBeInTheDocument();
      expect(screen.queryByTestId("runtime-preview")).toBeNull();
    });

    it("labels the preferred account also when the router picked it", async () => {
      await ask(previewBody);
      expect((await screen.findByTestId("runtime-preview-preferred")).textContent).toContain("preferred account");
    });

    it("shows the preferred account when it differs from the selected one", async () => {
      await ask({ ...previewBody, preferred_account_ref: "other" });
      expect((await screen.findByTestId("runtime-preview-preferred")).textContent).toContain("preferred: other");
    });
  });
  describe("policy editor", () => {
    const edit = async (putResponse: unknown) => {
      vi.mocked(api).mockImplementation(async (_url: string, opts?: { method?: string }) => {
        if (opts?.method === "PUT") {
          if (putResponse instanceof Error) throw putResponse;
          return putResponse;
        }
        return envelope();
      });
      renderTabs();
      open("Execution & routes");
      fireEvent.click(await screen.findByRole("button", { name: "Edit routes" }));
      await screen.findByRole("form", { name: "Edit routes" });
    };
    const put = () => vi.mocked(api).mock.calls.find((c) => (c[1] as { method?: string } | undefined)?.method === "PUT")!;
    const save = () => fireEvent.click(screen.getByRole("button", { name: "Save routes" }));

    it("PUTs only the policy with if_revision from the loaded catalog, and no credential data", async () => {
      await edit({ revision: 5, digest: "d" });
      fireEvent.change(screen.getByLabelText("QA mode"), { target: { value: "prefer" } });
      save();
      await screen.findByText("Saved as revision 5.");
      const [url, opts] = put();
      expect(String(url)).toContain(`/runtime/bindings/worker-b/policy?resource_owner_workspace_id=${b.workspace_id}`);
      const body = (opts as { body: Record<string, unknown> }).body;
      expect(body.if_revision).toBe(4);
      expect((body.policy as { qa: { mode: string } }).qa.mode).toBe("prefer");
      expect(JSON.stringify(body)).not.toContain("credential_ref");
    });

    it("API reserve is off unless explicitly ticked", async () => {
      await edit({ revision: 5 });
      save();
      await screen.findByText("Saved as revision 5.");
      expect(((put()[1] as { body: { policy: { api_reserve_providers: string[] } } }).body.policy.api_reserve_providers)).toEqual([]);
    });

    it("409 is an honest conflict: nothing saved, reload offered", async () => {
      await edit(apiErr(409));
      save();
      expect((await screen.findByRole("alert")).textContent).toContain("Nothing was saved");
      expect(screen.queryByText(/Saved as revision/)).toBeNull();
      expect(screen.queryByRole("button", { name: "Save routes" })).toBeNull();
      expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    });

    it("403 is a refusal, not a success", async () => {
      await edit(apiErr(403));
      save();
      expect((await screen.findByRole("alert")).textContent).toContain("owner or admin");
      expect(screen.queryByText(/Saved as revision/)).toBeNull();
    });

    it("a 200 without a numeric revision is not shown as saved", async () => {
      await edit({ ok: true });
      save();
      expect((await screen.findByRole("alert")).textContent).toContain("Nothing was changed");
      expect(screen.queryByText(/Saved as revision/)).toBeNull();
    });

    it("reordering sends the new order and primary follows permitted order", async () => {
      await edit({ revision: 5 });
      fireEvent.click(screen.getByRole("button", { name: "Move reserve up" }));
      save();
      await screen.findByText("Saved as revision 5.");
      const body = (put()[1] as { body: { permitted_profiles: string[] } }).body;
      expect(body.permitted_profiles).toEqual(["reserve", "main"]);
    });

    it("explains that Primary runs first and the list orders only one route's reserves", async () => {
      await edit({ revision: 5 });
      expect(screen.getByText(/Primary routes run first\. This list orders only the reserves of one route/)).toBeInTheDocument();
    });

    it("reserve order follows the edited order: quota_edges are reordered and pruned", async () => {
      expect(orderQuotaEdges({ a: ["r1", "r2", "gone"], gone: ["r1"] }, ["r2", "a", "r1"])).toEqual({ a: ["r2", "r1"] });
      await edit({ revision: 5 });
      fireEvent.click(screen.getByRole("button", { name: "Remove reserve" }));
      save();
      await screen.findByText("Saved as revision 5.");
      const body = (put()[1] as { body: { policy: { quota_edges: Record<string, string[]> } } }).body;
      expect(body.policy.quota_edges).toEqual({ main: [] });
    });

    it("a non-admin (403 on load) gets no editor", async () => {
      vi.mocked(api).mockRejectedValue(apiErr(403));
      renderTabs();
      open("Execution & routes");
      await screen.findByText(/admins only/);
      expect(screen.queryByRole("button", { name: "Edit routes" })).toBeNull();
    });
  });
});
