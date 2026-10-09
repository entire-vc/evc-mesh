import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { RuntimeResourcesCard } from "@/components/runtime-resources-card";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";

const example = JSON.parse(readFileSync(resolve(__dirname, "../../../../docs/api/agent-runtime.example.json"), "utf8"));
const apiErr = (status: number) => Object.assign(new Error("x"), { code: "e", status });
const ctl = (over: Record<string, unknown> = {}, current = true) => ({
  controller_ref: "runner-a",
  report: { revision: 4, digest: "dd", status: "applied", ...over },
  received_at: new Date().toISOString(),
  current,
});
const env = (controllers: unknown[]) => ({
  mode: "managed", revision: 4, digest: "dd", enabled: true, drain_requested: false, config: example, controllers,
});

describe("RuntimeResourcesCard", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
  });

  it("lists controllers, accounts and pools with desired/applied state", async () => {
    vi.mocked(api).mockResolvedValue(env([ctl()]));
    render(<RuntimeResourcesCard workspaceId="ws1" />);
    expect(await screen.findByText("applied")).toBeInTheDocument();
    expect(screen.getByText("Accounts")).toBeInTheDocument();
    expect(screen.getByText("Pools")).toBeInTheDocument();
    expect(screen.getByText(/Desired r4/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/\bvv\d/);
    expect(vi.mocked(api)).toHaveBeenCalledWith("/api/v1/workspaces/ws1/runtime");
  });

  it("never renders credential references", async () => {
    vi.mocked(api).mockResolvedValue(env([ctl()]));
    render(<RuntimeResourcesCard workspaceId="ws1" />);
    await screen.findByText("Accounts");
    expect(document.body.textContent).not.toMatch(/cred:/);
  });

  it("shows stale / error controller states with reasons", async () => {
    vi.mocked(api).mockResolvedValue(env([ctl({ status: "rejected" })]));
    render(<RuntimeResourcesCard workspaceId="ws1" />);
    expect(await screen.findByText("error")).toBeInTheDocument();
  });

  it("controller with no report is explicit, not applied", async () => {
    vi.mocked(api).mockResolvedValue(env([]));
    render(<RuntimeResourcesCard workspaceId="ws1" />);
    expect(await screen.findByText("unknown")).toBeInTheDocument();
    expect(screen.queryByText("applied")).not.toBeInTheDocument();
  });

  it("loading, error+retry, forbidden and direct-mode states", async () => {
    vi.mocked(api).mockReturnValue(new Promise(() => {}));
    const a = render(<RuntimeResourcesCard workspaceId="ws1" />);
    expect(screen.getByLabelText("Loading runtime")).toHaveAttribute("aria-busy", "true");
    a.unmount();

    vi.mocked(api).mockRejectedValueOnce(apiErr(500)).mockResolvedValue(env([ctl()]));
    const b = render(<RuntimeResourcesCard workspaceId="ws1" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be loaded");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Accounts")).toBeInTheDocument();
    b.unmount();

    vi.mocked(api).mockReset();
    vi.mocked(api).mockRejectedValue(apiErr(403));
    const c = render(<RuntimeResourcesCard workspaceId="ws1" />);
    expect(await screen.findByText(/admins only/)).toBeInTheDocument();
    c.unmount();

    vi.mocked(api).mockReset();
    vi.mocked(api).mockResolvedValue({ mode: "direct", controllers: [] });
    render(<RuntimeResourcesCard workspaceId="ws1" />);
    expect(await screen.findByText(/direct mode/)).toBeInTheDocument();
  });
});
