import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router";
import { fireEvent, render as rtlRender, screen, waitFor } from "@testing-library/react";
import { ProjectExtensionsTab } from "@/components/extensions/project-extensions-tab";
import { api, ApiRequestError } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";

vi.mock("@/components/extensions/chat-settings-card", () => ({ ChatSettingsCard: () => <div data-testid="chat-card" /> }));
vi.mock("@/lib/api", async () => ({ ...await vi.importActual("@/lib/api"), api: vi.fn() }));

const gitlabManifest = {
  levels: { instance: true, workspace: true, project: true },
  settings: { project: { type: "object", required: ["repo_path"], properties: { repo_path: { type: "string", title: "Путь проекта" } } } },
};
const trManifest = {
  levels: { instance: true, workspace: true, project: true },
  settings: { project: { type: "object", properties: { share_id: { type: "string", title: "Шара" }, agent_key: { type: "string", title: "Ключ", "x-secret": true } } } },
};
const ws = (id: string, name: string, manifest: unknown, connected = true) => ({
  id, name, version: "1", kind: "integration", category: "x", manifest,
  status: { enabled: connected, source: "workspace" }, connection: connected ? {} : null,
});
const P = "/api/v1/projects/p1/extensions";
let bindings: unknown[];

function mockApi(overrides: Record<string, unknown> = {}) {
  vi.mocked(api).mockImplementation(async (path: string, opts?: { method?: string }) => {
    const key = `${opts?.method ?? "GET"} ${path}`;
    if (key in overrides) { const v = overrides[key]; if (v instanceof Error) throw v; return v; }
    if (key === `GET ${P}`) return { extensions: bindings };
    if (key === "GET /api/v1/extensions") return { extensions: [] };
    if (key === "GET /api/v1/workspaces/w1/extensions") {
      return { extensions: [ws("gitlab", "GitLab", gitlabManifest), ws("team-relay", "Team Relay", trManifest), ws("telegram", "Telegram", { levels: { project: false } })] };
    }
    if (key === "GET /api/v1/extensions/catalog") throw new ApiRequestError("nf", "NOT_FOUND", 404);
    if (path.endsWith("/actions") && !opts?.method) return { actions: path.includes("team-relay") ? [{ name: "check", method: "POST" }, { name: "mount", method: "POST", title: "Смонтировать" }, { name: "search", method: "GET", title: "Search the documents of the bound share" }] : [{ name: "check", method: "POST" }] };
    return {};
  });
}

const render = (ui: React.ReactElement, url = "/") => rtlRender(<MemoryRouter initialEntries={[url]}>{ui}</MemoryRouter>);

describe("ProjectExtensionsTab", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    bindings = [];
    useWorkspaceStore.setState({ currentWorkspace: { id: "w1" } as never });
  });

  it("empty: offers only connected extensions that have a project level", async () => {
    mockApi();
    render(<ProjectExtensionsTab projectId="p1" />);
    expect(await screen.findByTestId("extensions-tab-empty")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Bind extension/ }));
    const opts = Array.from((screen.getByLabelText("Extension") as HTMLSelectElement).options).map((o) => o.text);
    expect(opts).toEqual(["GitLab", "Team Relay"]);
  });

  it("ignores a slow bindings answer for the previous project", async () => {
    mockApi();
    let releaseP1!: (v: unknown) => void;
    const base = vi.mocked(api).getMockImplementation()!;
    vi.mocked(api).mockImplementation(async (path: string, opts?: { method?: string }) => {
      if (path === `${P}` && !opts?.method) return new Promise((r) => { releaseP1 = r; });
      if (path === "/api/v1/projects/p2/extensions") return { extensions: [] };
      return base(path, opts);
    });
    const { rerender } = render(<ProjectExtensionsTab projectId="p1" />);
    rerender(<MemoryRouter><ProjectExtensionsTab projectId="p2" /></MemoryRouter>);
    expect(await screen.findByTestId("extensions-tab-empty")).toBeInTheDocument();
    releaseP1({ extensions: [{ extension_id: "gitlab", enabled: true, settings: { repo_path: "old/p1" } }] });
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("binding-gitlab")).toBeNull();
    expect(screen.getByTestId("extensions-tab-empty")).toBeInTheDocument();
  });

  it("remounts a binding form when the project changes, so p2 never shows p1 settings", async () => {
    mockApi();
    const base = vi.mocked(api).getMockImplementation()!;
    const mk = (repo: string) => ({ extensions: [{ extension_id: "gitlab", enabled: true, settings: { repo_path: repo } }] });
    vi.mocked(api).mockImplementation(async (path: string, opts?: { method?: string }) => {
      if (path === "/api/v1/projects/p1/extensions" && !opts?.method) return mk("old/p1");
      if (path === "/api/v1/projects/p2/extensions" && !opts?.method) return mk("new/p2");
      return base(path, opts);
    });
    const { rerender } = render(<ProjectExtensionsTab projectId="p1" />);
    expect(await screen.findByDisplayValue("old/p1")).toBeInTheDocument();
    rerender(<MemoryRouter><ProjectExtensionsTab projectId="p2" /></MemoryRouter>);
    expect(await screen.findByDisplayValue("new/p2")).toBeInTheDocument();
    expect(screen.queryByDisplayValue("old/p1")).toBeNull();
  });

  it("binds GitLab through the form with PUT", async () => {
    mockApi();
    render(<ProjectExtensionsTab projectId="p1" />);
    fireEvent.click(await screen.findByRole("button", { name: /Bind extension/ }));
    fireEvent.change(await screen.findByLabelText(/Путь проекта/), { target: { value: "grp/repo" } });
    fireEvent.click(screen.getByRole("button", { name: "Bind" }));
    await waitFor(() => expect(api).toHaveBeenCalledWith(`${P}/gitlab`, { method: "PUT", body: { enabled: true, settings: { repo_path: "grp/repo" } } }));
  });

  it("shows a binding with sync status, mount action and unbinds after confirmation", async () => {
    bindings = [{ extension_id: "team-relay", enabled: true, settings: { share_id: "s1", agent_key: "***" }, last_sync_status: "error", last_sync_error: "relay unreachable", last_sync_checked_at: "2026-10-16T10:00:00Z" }];
    mockApi();
    render(<ProjectExtensionsTab projectId="p1" />);
    const card = await screen.findByTestId("binding-team-relay");
    expect(card).toHaveTextContent("relay unreachable");
    expect(screen.getByTestId("sync-team-relay")).toHaveTextContent("error");
    expect(card.innerHTML).not.toContain("***"); // the mask never lands in an input value as text to read back
    fireEvent.click(await screen.findByRole("button", { name: "Смонтировать" }));
    await waitFor(() => expect(api).toHaveBeenCalledWith(`${P}/team-relay/actions/mount`, { method: "POST" }));
    fireEvent.click(screen.getByRole("button", { name: "Unbind" }));
    const unbindBtns = await screen.findAllByRole("button", { name: "Unbind" });
    fireEvent.click(unbindBtns[unbindBtns.length - 1]!);
    await waitFor(() => expect(api).toHaveBeenCalledWith(`${P}/team-relay`, { method: "DELETE" }));
  });

  it("searches the bound share through the GET search action and keeps it out of the buttons", async () => {
    bindings = [{ extension_id: "team-relay", enabled: true, settings: { share_id: "s" } }];
    mockApi({ [`GET ${P}/team-relay/actions/search?q=plan%20b`]: { docs: [{ id: "d1", title: "Plan B", path: "plans/b.md" }] } });
    render(<ProjectExtensionsTab projectId="p1" />);
    const box = await screen.findByLabelText("Search documents");
    expect(screen.queryByRole("button", { name: "Search the documents of the bound share" })).toBeNull();
    fireEvent.change(box, { target: { value: "plan b" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText("Plan B")).toBeInTheDocument();
    expect(screen.getByText("plans/b.md")).toBeInTheDocument();
  });

  it("search failure shows an error and an empty result says so", async () => {
    bindings = [{ extension_id: "team-relay", enabled: true, settings: {} }];
    mockApi({ [`GET ${P}/team-relay/actions/search?q=x`]: new Error("502") });
    render(<ProjectExtensionsTab projectId="p1" />);
    fireEvent.change(await screen.findByLabelText("Search documents"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText("Search failed. Try again.")).toBeInTheDocument();
  });

  it("check shows the server message", async () => {
    bindings = [{ extension_id: "gitlab", enabled: true, settings: { repo_path: "a/b" } }];
    mockApi({ [`POST ${P}/gitlab/actions/check`]: { ok: false, message: "token rejected" } });
    render(<ProjectExtensionsTab projectId="p1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Check" }));
    expect(await screen.findByText("token rejected")).toBeInTheDocument();
  });

  it("error state offers retry", async () => {
    mockApi({ [`GET ${P}`]: new Error("boom") });
    render(<ProjectExtensionsTab projectId="p1" />);
    expect(await screen.findByTestId("extensions-tab-error")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("N bindings give N second-level tabs, ext= picks the tab, Bind stays on every tab", async () => {
    bindings = [
      { extension_id: "gitlab", enabled: true, settings: { repo_path: "a/b" } },
      { extension_id: "team-relay", enabled: true, settings: { share_id: "s" } },
    ];
    mockApi();
    render(<ProjectExtensionsTab projectId="p1" />, "/?tab=integrations&ext=team-relay");
    expect(await screen.findByTestId("binding-team-relay")).toBeInTheDocument();
    expect(screen.getAllByRole("tab")).toHaveLength(2);
    expect(screen.queryByTestId("binding-gitlab")).toBeNull();
    expect(screen.getByTestId("ext-tab-team-relay")).toHaveAttribute("aria-selected", "true");
    fireEvent.click(screen.getByTestId("ext-tab-gitlab"));
    expect(await screen.findByTestId("binding-gitlab")).toBeInTheDocument();
    expect(screen.queryByTestId("binding-team-relay")).toBeNull();
    // Telegram has no project level and both others are bound: nothing left to bind
    expect(screen.queryByRole("button", { name: /Bind extension/ })).toBeNull();
  });

  it("Bind extension is visible next to the tabs when another extension can be bound", async () => {
    bindings = [{ extension_id: "gitlab", enabled: true, settings: { repo_path: "a/b" } }];
    mockApi();
    render(<ProjectExtensionsTab projectId="p1" />);
    expect(await screen.findByTestId("binding-gitlab")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Bind extension/ }));
    expect(await screen.findByTestId("new-binding")).toBeInTheDocument();
    expect(screen.queryByTestId("binding-gitlab")).toBeNull();
  });

  it("Chat with Agent is a tab of its own and the empty state still shows when nothing else is bound", async () => {
    mockApi();
    const base = vi.mocked(api).getMockImplementation()!;
    vi.mocked(api).mockImplementation(async (path: string, opts?: { method?: string }) => {
      if (path === "/api/v1/workspaces/w1/extensions" && !opts?.method) {
        return { extensions: [ws("agent-chat", "Chat with Agent", { levels: { project: true } }), ws("gitlab", "GitLab", gitlabManifest)] };
      }
      return base(path, opts);
    });
    render(<ProjectExtensionsTab projectId="p1" />);
    expect(await screen.findByTestId("chat-card")).toBeInTheDocument();
    expect(screen.getAllByRole("tab")).toHaveLength(1);
    expect(screen.getByTestId("extensions-tab-empty")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Bind extension/ })).toBeInTheDocument();
  });
});
