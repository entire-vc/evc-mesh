import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { InvitedAgentDialog } from "@/components/invited-agent-dialog";
import { useAgentWorkspaceGrantStore } from "@/stores/agent-workspace-grant";
import type { OrgChartAgentNode } from "@/types";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";

const example = JSON.parse(
  readFileSync(resolve(__dirname, "../../../../docs/api/agent-runtime.example.json"), "utf8").split('"preferred"').join('"main"'),
);
const b = example.bindings["worker-b"].binding;
const RECEIVING_WS = b.workspace_id;

const invited = {
  id: b.agent_id,
  name: "Test",
  role: "worker",
  status: "online",
  short_tag: "",
  children: [],
} as unknown as OrgChartAgentNode;

const envelope = {
  mode: "managed",
  revision: 4,
  digest: "deadbeefcafe0123",
  enabled: true,
  drain_requested: false,
  config: example,
  controllers: [],
};

function mockApi(runtime: unknown | Error) {
  vi.mocked(api).mockImplementation((path: string) => {
    if (path === `/api/v1/workspaces/${RECEIVING_WS}/agent-grants`) {
      return Promise.resolve({
        agent_grants: [{ id: "g1", agent_id: b.agent_id, role: "member", api_key_prefix: "evc_ab12" }],
        count: 1,
      });
    }
    if (path === `/api/v1/workspaces/${RECEIVING_WS}/runtime`) {
      return runtime instanceof Error ? Promise.reject(runtime) : Promise.resolve(runtime);
    }
    return Promise.reject(new Error(`unexpected call: ${path}`));
  });
}

const renderDialog = (onClose: () => void = vi.fn()) =>
  render(
    <InvitedAgentDialog
      open
      onClose={onClose}
      onChanged={vi.fn()}
      workspaceId={RECEIVING_WS}
      agent={invited}
      canManage
    />,
  );

describe("InvitedAgentDialog — receiving workspace runtime", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    useAgentWorkspaceGrantStore.setState({ workspaceAgentGrants: [] });
  });

  it("shows the four tabs and the routes/preferred/preview of the RECEIVING workspace", async () => {
    mockApi(envelope);
    renderDialog();
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual([
      "Profile", "Workspace access", "Execution & routes", "Observed state",
    ]);
    fireEvent.click(screen.getByRole("tab", { name: "Execution & routes" }));
    expect(await screen.findByText("Allowed routes, in order")).toBeInTheDocument();
    expect(screen.getByText("preferred is usable")).toBeInTheDocument();
    expect(screen.getByText(/advisory/i)).toBeInTheDocument();
    // the runtime was read for the receiving workspace, never for any other
    for (const [path] of vi.mocked(api).mock.calls) {
      expect(String(path)).toContain(RECEIVING_WS);
    }
  });

  it("keeps the foreign agent's home data out: only role, status and the grant are shown", async () => {
    mockApi(envelope);
    renderDialog();
    expect(screen.getByText("worker")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Workspace access" }));
    expect(await screen.findByText("evc_ab12…")).toBeInTheDocument();
    expect(screen.queryByText(/responsibility/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/heartbeat/i)).not.toBeInTheDocument();
  });

  it("forbidden runtime for this user shows a note, not routes (negative control)", async () => {
    mockApi(Object.assign(new Error("x"), { code: "forbidden", status: 403 }));
    renderDialog();
    fireEvent.click(screen.getByRole("tab", { name: "Execution & routes" }));
    await waitFor(() => expect(screen.queryByText("Allowed routes, in order")).not.toBeInTheDocument());
    expect(await screen.findByRole("status")).toBeInTheDocument();
  });

  it("runtime load error shows the retry state and the dialog stays usable", async () => {
    mockApi(Object.assign(new Error("x"), { code: "e", status: 500 }));
    renderDialog();
    fireEvent.click(screen.getByRole("tab", { name: "Observed state" }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Close" })).toBeEnabled();
  });

  it("Retry after a load error reloads the runtime and shows the routes", async () => {
    let fail = true;
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === `/api/v1/workspaces/${RECEIVING_WS}/agent-grants`) {
        return Promise.resolve({ agent_grants: [], count: 0 });
      }
      if (path === `/api/v1/workspaces/${RECEIVING_WS}/runtime`) {
        return fail
          ? Promise.reject(Object.assign(new Error("x"), { code: "e", status: 500 }))
          : Promise.resolve(envelope);
      }
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });
    renderDialog();
    fireEvent.click(screen.getByRole("tab", { name: "Execution & routes" }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Allowed routes, in order")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("a workspace without a managed runtime shows the empty note, not routes", async () => {
    mockApi({ ...envelope, mode: "direct" });
    renderDialog();
    fireEvent.click(screen.getByRole("tab", { name: "Execution & routes" }));
    expect(await screen.findByRole("status")).toBeInTheDocument();
    expect(screen.queryByText("Allowed routes, in order")).not.toBeInTheDocument();
  });

  it("Close calls onClose", async () => {
    mockApi(envelope);
    const onClose = vi.fn();
    renderDialog(onClose);
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });
});
