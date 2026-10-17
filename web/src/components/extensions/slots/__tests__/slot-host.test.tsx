/**
 * UI slots (W2.7): a connected, enabled extension that declares a slot in its
 * manifest fills it; a disabled one, or one that does not declare it, does not.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";

const apiMock = vi.fn();
vi.mock("@/lib/api", () => ({ api: (...a: unknown[]) => apiMock(...a) }));

import { saveProjectExtension } from "@/lib/extension-settings";
import { parseSlotExtensions, resetSlotRegistryForTests } from "@/lib/extension-slots";
import { useWorkspaceStore } from "@/stores/workspace";
import { SlotActionsMenu, SlotBadges, SlotPanelBody, usePanelTabs, useSlotOwnedVcsProviders, SLOT_TIMEOUT_MS } from "../slot-host";
import type { Task } from "@/types";

const GITLAB_UI = ["task.panel", "task.badge", "task.actions"];
const TASK = { id: "t1", vcs_link_count: 1, vcs_providers: ["gitlab"] } as unknown as Task;

function workspaceExtensions(opts: { enabled: boolean; ui?: unknown[] }) {
  return {
    extensions: [
      {
        id: "gitlab",
        name: "GitLab",
        manifest: { capabilities: { ui: opts.ui ?? GITLAB_UI } },
        status: { enabled: opts.enabled },
      },
    ],
  };
}

function mockApi(ext: ReturnType<typeof workspaceExtensions>) {
  apiMock.mockImplementation((path: string) => {
    if (path.endsWith("/extensions")) return Promise.resolve(ext);
    if (path.includes("/vcs-links")) return Promise.resolve({ vcs_links: [] });
    return Promise.resolve({});
  });
}

function Tabs() {
  const tabs = usePanelTabs();
  return <ul>{tabs.map((t) => <li key={t.extensionId}>{t.name}</li>)}</ul>;
}

beforeEach(() => {
  apiMock.mockReset();
  resetSlotRegistryForTests();
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as never });
});
afterEach(() => vi.useRealTimers());

describe("parseSlotExtensions", () => {
  it("keeps enabled extensions, reads string and object slots, ignores unknown slots", () => {
    const out = parseSlotExtensions([
      { id: "a", name: "A", status: { enabled: true }, manifest: { capabilities: { ui: ["task.badge", { slot: "task.panel" }, "project.settings"] } } },
      { id: "b", name: "B", status: { enabled: false }, manifest: { capabilities: { ui: ["task.panel"] } } },
      { id: "c", name: "C", status: { enabled: true }, manifest: { capabilities: { ui: [] } } },
    ]);
    expect(out).toEqual([{ id: "a", name: "A", slots: ["task.badge", "task.panel"] }]);
  });

  it("an extension enabled through the instance level (no workspace connection row) is kept", () => {
    const out = parseSlotExtensions([
      { id: "g", name: "G", connection: null, status: { enabled: true, source: "instance" }, manifest: { capabilities: { ui: ["task.badge"] } } },
    ] as never);
    expect(out).toEqual([{ id: "g", name: "G", slots: ["task.badge"] }]);
  });
});

describe("task.panel", () => {
  it("adds a tab named after the extension when enabled", async () => {
    mockApi(workspaceExtensions({ enabled: true }));
    render(<Tabs />);
    expect(await screen.findByText("GitLab")).toBeInTheDocument();
  });

  it("adds no tab when the extension is disabled (red control)", async () => {
    mockApi(workspaceExtensions({ enabled: false }));
    render(<Tabs />);
    await waitFor(() => expect(apiMock).toHaveBeenCalled());
    await act(async () => {});
    expect(screen.queryByText("GitLab")).toBeNull();
  });

  it("adds no tab when the manifest does not declare the slot", async () => {
    mockApi(workspaceExtensions({ enabled: true, ui: ["task.badge"] }));
    render(<Tabs />);
    await waitFor(() => expect(apiMock).toHaveBeenCalled());
    await act(async () => {});
    expect(screen.queryByText("GitLab")).toBeNull();
  });

  it("renders the lazy GitLab panel body", async () => {
    mockApi(workspaceExtensions({ enabled: true }));
    render(<SlotPanelBody extensionId="gitlab" name="GitLab" task={TASK} />);
    expect(await screen.findByTestId("slot-panel-gitlab", {}, { timeout: 8000 })).toBeInTheDocument();
  });

  it("shows 'unavailable' for an extension with no component, without waiting", () => {
    render(<SlotPanelBody extensionId="ghost" name="Ghost" task={TASK} />);
    expect(screen.getByTestId("slot-unavailable")).toBeInTheDocument();
  });

  it("falls back to 'unavailable' after the 2 s timeout while a panel is still loading", async () => {
    vi.useFakeTimers();
    vi.doMock("../gitlab-panel", () => ({ default: () => new Promise(() => {}) }));
    vi.resetModules();
    const { SlotPanelBody: Fresh } = await import("../slot-host");
    render(<Fresh extensionId="gitlab" name="GitLab" task={TASK} />);
    expect(screen.getByTestId("slot-pending")).toBeInTheDocument();
    await act(async () => { vi.advanceTimersByTime(SLOT_TIMEOUT_MS + 50); });
    expect(screen.getByTestId("slot-unavailable")).toBeInTheDocument();
    vi.doUnmock("../gitlab-panel");
  });

  it("a card switched in place restarts readiness and the 2 s timeout for the new task", async () => {
    vi.useFakeTimers();
    vi.doMock("../gitlab-panel", () => ({ default: () => new Promise(() => {}) }));
    vi.resetModules();
    const { SlotPanelBody: Fresh } = await import("../slot-host");
    const other = { id: "t2", vcs_link_count: 1, vcs_providers: ["gitlab"] } as unknown as Task;
    const { rerender } = render(<Fresh extensionId="gitlab" name="GitLab" task={TASK} />);
    await act(async () => { vi.advanceTimersByTime(SLOT_TIMEOUT_MS - 500); });
    await act(async () => { rerender(<Fresh extensionId="gitlab" name="GitLab" task={other} />); });
    // 1 s after the switch: the old task's timer would have fired by now, the new task's has not
    await act(async () => { vi.advanceTimersByTime(1000); });
    expect(screen.getByTestId("slot-pending")).toBeInTheDocument();
    expect(screen.queryByTestId("slot-unavailable")).toBeNull();
    await act(async () => { vi.advanceTimersByTime(SLOT_TIMEOUT_MS); });
    expect(screen.getByTestId("slot-unavailable")).toBeInTheDocument();
    vi.doUnmock("../gitlab-panel");
  });

  it("falls back to 'unavailable' when the module loads but the panel's data request stalls", async () => {
    apiMock.mockImplementation((path: string) =>
      path.includes("/vcs-links") ? new Promise(() => {}) : Promise.resolve(workspaceExtensions({ enabled: true })),
    );
    render(<SlotPanelBody extensionId="gitlab" name="GitLab" task={TASK} />);
    expect(await screen.findByTestId("slot-panel-gitlab", {}, { timeout: 8000 })).toBeInTheDocument();
    expect(screen.queryByTestId("slot-unavailable")).toBeNull();
    await new Promise((r) => setTimeout(r, SLOT_TIMEOUT_MS + 100));
    expect(await screen.findByTestId("slot-unavailable")).toBeInTheDocument();
  }, 15000);
});

describe("slot-owned VCS providers (generic list keeps links when the slot is off)", () => {
  function Owned({ slot }: { slot: "task.panel" | "task.badge" }) {
    return <span data-testid="owned">{useSlotOwnedVcsProviders(slot).join(",")}</span>;
  }
  it("owns gitlab while its panel and badge are active", async () => {
    mockApi(workspaceExtensions({ enabled: true }));
    render(<><Owned slot="task.panel" /><Owned slot="task.badge" /></>);
    await waitFor(() => expect(screen.getAllByTestId("owned").map((e) => e.textContent)).toEqual(["gitlab", "gitlab"]));
  });
  it("owns nothing when GitLab is disabled (red control)", async () => {
    mockApi(workspaceExtensions({ enabled: false }));
    render(<Owned slot="task.panel" />);
    await waitFor(() => expect(apiMock).toHaveBeenCalled());
    expect(screen.getByTestId("owned").textContent).toBe("");
  });
});

describe("task.badge", () => {
  it("shows the GitLab badge on a task with GitLab links when enabled", async () => {
    mockApi(workspaceExtensions({ enabled: true }));
    render(<SlotBadges task={TASK} />);
    expect(await screen.findByTestId("slot-badge-gitlab")).toBeInTheDocument();
  });

  it.each(["pending", "running", "success", "failed", "canceled", "skipped", "manual"])(
    "shows the %s pipeline state on the GitLab badge",
    async (status) => {
      mockApi(workspaceExtensions({ enabled: true }));
      render(<SlotBadges task={{ ...TASK, vcs_pipeline_status: status } as Task} />);
      const badge = await screen.findByTestId("slot-badge-gitlab");
      expect(badge.getAttribute("data-pipeline-status")).toBe(status);
    },
  );

  it.each(["from-the-future", "constructor", "toString", "__proto__"])(
    "keeps the plain MR icon for the unknown state %s",
    async (status) => {
      mockApi(workspaceExtensions({ enabled: true }));
      render(<SlotBadges task={{ ...TASK, vcs_pipeline_status: status } as Task} />);
      const badge = await screen.findByTestId("slot-badge-gitlab");
      expect(badge.getAttribute("data-pipeline-status")).toBeNull();
    },
  );

  it("shows nothing for a task without GitLab links", async () => {
    mockApi(workspaceExtensions({ enabled: true }));
    render(<SlotBadges task={{ ...TASK, vcs_providers: ["github"] } as Task} />);
    await waitFor(() => expect(apiMock).toHaveBeenCalled());
    await act(async () => {});
    expect(screen.queryByTestId("slot-badge-gitlab")).toBeNull();
  });

  it("shows nothing when GitLab is disabled (red control)", async () => {
    mockApi(workspaceExtensions({ enabled: false }));
    render(<SlotBadges task={TASK} />);
    await waitFor(() => expect(apiMock).toHaveBeenCalled());
    await act(async () => {});
    expect(screen.queryByTestId("slot-badge-gitlab")).toBeNull();
  });

  it("fetches the registry once for many cards", async () => {
    mockApi(workspaceExtensions({ enabled: true }));
    render(<><SlotBadges task={TASK} /><SlotBadges task={TASK} /><SlotBadges task={TASK} /></>);
    await waitFor(() => expect(screen.getAllByTestId("slot-badge-gitlab")).toHaveLength(3));
    expect(apiMock.mock.calls.filter(([p]) => String(p).endsWith("/extensions"))).toHaveLength(1);
  });
});

describe("task.actions", () => {
  it("lists the extension action and switches to its panel", async () => {
    mockApi(workspaceExtensions({ enabled: true }));
    const openPanel = vi.fn();
    render(<SlotActionsMenu task={TASK} openPanel={openPanel} />);
    fireEvent.click(await screen.findByTestId("slot-actions-trigger"));
    fireEvent.click(await screen.findByText(/merge requests/i));
    expect(openPanel).toHaveBeenCalledWith("gitlab");
  });

  it("renders no menu when GitLab is disabled", async () => {
    mockApi(workspaceExtensions({ enabled: false }));
    render(<SlotActionsMenu task={TASK} openPanel={vi.fn()} />);
    await waitFor(() => expect(apiMock).toHaveBeenCalled());
    await act(async () => {});
    expect(screen.queryByTestId("slot-actions-trigger")).toBeNull();
  });
});

describe("registry follows enable/disable without a reload", () => {
  it("a settings save that disables GitLab drops its badge on the already-mounted board", async () => {
    let enabled = true;
    apiMock.mockImplementation((path: string, opts?: { method?: string }) => {
      if (path.endsWith("/extensions") && !opts?.method) return Promise.resolve(workspaceExtensions({ enabled }));
      if (opts?.method === "PUT") { enabled = false; return Promise.resolve({}); }
      return Promise.resolve({ vcs_links: [] });
    });
    render(<SlotBadges task={TASK} />);
    await waitFor(() => expect(screen.queryAllByTestId(/badge/i).length).toBeGreaterThan(0));
    await act(async () => { await saveProjectExtension("p1", "gitlab", { enabled: false, settings: {} }); });
    await waitFor(() => expect(screen.queryAllByTestId(/badge/i)).toHaveLength(0));
  });

  it("a save that lands while the registry is still loading triggers one more fetch", async () => {
    let release!: () => void;
    let calls = 0;
    apiMock.mockImplementation((path: string, opts?: { method?: string }) => {
      if (path.endsWith("/extensions") && !opts?.method) {
        calls++;
        return calls === 1 ? new Promise((r) => { release = () => r(workspaceExtensions({ enabled: true })); }) : Promise.resolve(workspaceExtensions({ enabled: false }));
      }
      return Promise.resolve({});
    });
    render(<SlotBadges task={TASK} />);
    await waitFor(() => expect(calls).toBe(1));
    await act(async () => { await saveProjectExtension("p1", "gitlab", { enabled: false, settings: {} }); });
    await act(async () => { release(); });
    await waitFor(() => expect(calls).toBe(2));
    expect(screen.queryAllByTestId(/badge/i)).toHaveLength(0);
  });
});
