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

  describe("quota windows", () => {
    const obs = (over: Record<string, unknown>) => ({
      pool: "shared-window", provider: "claude", window: "5h", used_fraction: 0.07, observed_at: "2026-10-11T15:29:36Z",
      stale: false, status: "available", ...over,
    });
    const serve = (observations: unknown, quotaErr?: Error) =>
      vi.mocked(api).mockImplementation(async (url: string) => {
        if (url.endsWith("/quota-observations")) {
          if (quotaErr) throw quotaErr;
          return { controller_ref: "runner-a", observations };
        }
        return env([ctl()]);
      });

    it("shows raw used_fraction per window and calls the controller endpoint", async () => {
      serve([obs({}), obs({ window: "7d", used_fraction: 0.53 })]);
      render(<RuntimeResourcesCard workspaceId="ws1" />);
      const list = await screen.findByRole("list", { name: "Usage of shared-window" });
      expect(list).toHaveTextContent("5h: 7 %");
      expect(list).toHaveTextContent("7d: 53 %");
      expect(vi.mocked(api)).toHaveBeenCalledWith("/api/v1/workspaces/ws1/runtime/controllers/runner-a/quota-observations");
    });

    it("null is unknown, never 0 %; a real 0 stays 0 %", async () => {
      serve([obs({ used_fraction: null, status: "unknown" }), obs({ window: "7d", used_fraction: 0 })]);
      render(<RuntimeResourcesCard workspaceId="ws1" />);
      const list = await screen.findByRole("list", { name: "Usage of shared-window" });
      expect(list).toHaveTextContent("5h: unknown");
      expect(list).toHaveTextContent("7d: 0 %");
      expect(list.textContent).not.toMatch(/5h: 0/);
    });

    it("marks a stale reading and never presents it as current", async () => {
      serve([obs({ stale: true })]);
      render(<RuntimeResourcesCard workspaceId="ws1" />);
      expect(await screen.findByRole("list", { name: "Usage of shared-window" })).toHaveTextContent("7 % (stale)");
    });

    it("a stale unknown reading keeps the stale marker", async () => {
      serve([obs({ used_fraction: null, stale: true, status: "unknown" })]);
      render(<RuntimeResourcesCard workspaceId="ws1" />);
      expect(await screen.findByRole("list", { name: "Usage of shared-window" })).toHaveTextContent("5h: unknown (stale)");
    });

    it("403 on quota-observations says usage is admin-only, not silence", async () => {
      serve([], Object.assign(new Error("forbidden"), { status: 403 }));
      render(<RuntimeResourcesCard workspaceId="ws1" />);
      expect((await screen.findAllByText(/Usage is visible to workspace admins only/)).length).toBeGreaterThan(0);
    });

    it.each([{ provider: undefined }, { status: undefined }, { status: "bogus" }, { status: "unknown", used_fraction: 0 }, { observed_at: "not-a-date" }, { observed_at: "2026" }, { observed_at: "2026-10-11" }, { observed_at: "2026-02-30T10:00:00Z" }, { observed_at: "2026-10-11T25:00:00Z" }])(
      "a record violating the contract (%o) makes usage unavailable",
      async (bad) => {
        serve([obs(bad)]);
        render(<RuntimeResourcesCard workspaceId="ws1" />);
        expect((await screen.findAllByText(/Usage unavailable/)).length).toBeGreaterThan(0);
        expect(screen.queryByRole("list", { name: /Usage of/ })).not.toBeInTheDocument();
      },
    );

    it("an offset timestamp is shown in UTC, not mislabeled with Z", async () => {
      serve([obs({ observed_at: "2026-10-11T15:29:36+02:00" })]);
      render(<RuntimeResourcesCard workspaceId="ws1" />);
      expect((await screen.findAllByText("13:29Z")).length).toBeGreaterThan(0);
    });

    it("endpoint failure and malformed records are explicit, not zeros", async () => {
      serve([], new Error("boom"));
      const a = render(<RuntimeResourcesCard workspaceId="ws1" />);
      expect((await screen.findAllByText(/Usage unavailable/)).length).toBeGreaterThan(0);
      a.unmount();
      serve([obs({ used_fraction: 7 })]);
      render(<RuntimeResourcesCard workspaceId="ws1" />);
      expect((await screen.findAllByText(/Usage unavailable/)).length).toBeGreaterThan(0);
      expect(screen.queryByRole("list", { name: /Usage of/ })).not.toBeInTheDocument();
    });

    it("a pool without readings says so", async () => {
      serve([obs({ pool: "long-window" })]);
      render(<RuntimeResourcesCard workspaceId="ws1" />);
      expect((await screen.findAllByText("No usage readings.")).length).toBeGreaterThan(0);
    });
  });
});
