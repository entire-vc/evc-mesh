/**
 * Runner section: four tabs (overview, agents, resources, history), every block
 * has a loading, empty, error and ready state, the operator's edits go to the
 * right route, and only a workspace admin sees the on/off switch, which is the
 * one place Runner is turned on or off. Only the network is mocked.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", async () => ({
  ...(await vi.importActual<typeof import("@/lib/api")>("@/lib/api")),
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));
vi.mock("@/components/ui/toast", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { api, ApiRequestError } from "@/lib/api";
import { setLanguage } from "@/i18n";
import { toast } from "@/components/ui/toast";
import { MyRunnerPage } from "@/pages/my-runner";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import type { Workspace } from "@/types";
import runnerManifest from "../../../../internal/extensions/runner/mesh-extension.json";
import r39Shape from "@/components/my-runner/__fixtures__/catalog-r39-shape.json";
import { entrySchema, unknownFields } from "@/lib/my-runner";
import type { ExtensionManifest } from "@/hooks/useExtensionCatalog";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;
const ME = "user-1";
const CONTROLLER = { runner_id: "r1", name: "mini-main", host: "mini.local", version: "1.4.0", lease_expires_at: new Date(Date.now() + 60_000).toISOString(), live: true };
const PROGRAM = { id: "agent-runner", name: "Agent Runner", version: "0.1.0", kind: "plugin", runtime: "external", publisher: "entire-vc", description: "", category: "agents", source: "catalog", trust_level: "official", blocked: false, blocked_reason: "", allowed: true,
  manifest: { capabilities: { api: ["tasks:read"], events: ["task.assigned"] }, settings: { workspace: { type: "object", properties: { runner_url: { type: "string" } } } } } };
const AGENT = { agent_id: "a1", name: "Robin", operator_user_id: ME, max_concurrent_tasks: 3, runtime_manager: false, controller: CONTROLLER };

interface World {
  agents: unknown[] | Error; runners: unknown[] | Error; history: unknown[] | Error; runtime: unknown | Error; extensionOn: boolean; role: string; enableError?: Error; slowOwner?: Promise<{ role: string }>; roleWs2?: string; slowCatalogWs2?: Promise<unknown>;
  catalog: unknown | Error; revisions: unknown[]; puts: unknown[];
  /** What the next catalog PUT is refused with (once). */
  refuse?: Error; manifest?: unknown;
  program?: "catalog" | "installed" | "connected"; programCaps?: object; programExtra?: object; posts: { path: string; body: unknown }[];
}
let world: World;
const program = () => ({ ...PROGRAM, ...world.programExtra, ...(world.programCaps ? { manifest: { ...PROGRAM.manifest, capabilities: world.programCaps } } : {}) });

function route(path: string, init?: { method?: string; body?: unknown }): Promise<unknown> {
  const out = (v: unknown) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v));
  if (init?.method && init.method !== "GET") {
    if (path.endsWith("/runner/enable") && world.enableError) return Promise.reject(world.enableError);
    if (path === "/api/v1/workspaces/ws-1/runtime" && init.method === "PUT") return saveCatalog(init.body as { if_revision: number; enabled: boolean; config: unknown });
    world.posts.push({ path, body: init.body });
    return Promise.resolve({});
  }
  if (path === "/api/v1/me/runtime/agents?workspace_id=ws-1") return out(world.agents instanceof Error ? world.agents : { agents: world.agents });
  if (path === "/api/v1/me/runtime/runners?workspace_id=ws-1") return out(world.runners instanceof Error ? world.runners : { runners: world.runners });
  if (path.startsWith("/api/v1/me/runtime/history")) return out(world.history instanceof Error ? world.history : { history: world.history });
  if (path.endsWith("/members/me")) return path.includes("ws-2") ? Promise.resolve({ role: world.roleWs2 ?? "member" }) : (world.slowOwner ?? Promise.resolve({ role: world.role }));
  if (path === "/api/v1/me/runtime") return out(world.runtime);
  if (path === "/api/v1/workspaces/ws-1/runtime") return out(world.catalog);
  if (path.startsWith("/api/v1/workspaces/ws-1/runtime/revisions")) return Promise.resolve({ managed: (world.catalog as { mode?: string })?.mode === "managed", revisions: world.revisions });
  if (path === "/api/v1/extensions") return Promise.resolve({ extensions: [{ id: "runner", name: "Runner", version: "1", kind: "plugin", runtime: "builtin", publisher: "entire-vc", description: "", category: "agents" }, ...(world.program && world.program !== "catalog" ? [program()] : [])] });
  if (path === "/api/v1/extensions/catalog" && world.program === "catalog") return Promise.resolve({ extensions: [program()] });
  if (path === "/api/v1/extensions/updates") return Promise.resolve({ updates: world.program === "connected" ? [{ extension_id: "agent-runner", name: "Agent Runner", current_version: "0.1.0", latest_version: "0.2.0", update_available: true }] : [] });
  if (path.endsWith("/extensions") && path.includes("ws-2") && world.slowCatalogWs2) return world.slowCatalogWs2;
  if (path.endsWith("/extensions")) return Promise.resolve({ extensions: [{ id: "runner", name: "Runner", version: "1", kind: "plugin", category: "agents", manifest: world.manifest, status: { enabled: (world.extensionOn && !path.includes("ws-2")), source: (world.extensionOn && !path.includes("ws-2")) ? "workspace" : "none" } },
    ...(world.program === "installed" || world.program === "connected" ? [{ id: "agent-runner", name: "Agent Runner", version: "0.1.0", kind: "plugin", category: "agents", manifest: program().manifest,
      connection: world.program === "connected" ? { settings: { runner_url: "https://runner.example.com" } } : null,
      status: world.program === "connected" ? { enabled: true, source: "workspace", health: { ok: false, message: "connection refused" } } : { enabled: false, source: "none" } }] : [])] });
  return Promise.reject(new ApiRequestError("not found", "not_found", 404));
}

/** The server's CAS: a stale revision gets 409, a current one becomes the next revision by "Alex". */
function saveCatalog(body: { if_revision: number; enabled: boolean; config: unknown }): Promise<unknown> {
  world.puts.push(body);
  if (world.refuse) {
    const e = world.refuse;
    world.refuse = undefined;
    return Promise.reject(e);
  }
  const current = world.catalog as { revision: number };
  if (body.if_revision !== current.revision) return Promise.reject(new ApiRequestError("runtime desired revision changed", "conflict", 409));
  const revision = current.revision + 1;
  world.catalog = { ...current, revision, config: body.config };
  world.revisions = [{ revision, digest: "d".repeat(64), enabled: body.enabled, author_id: ME, author_name: "Alex", created_at: new Date().toISOString() }, ...world.revisions];
  return Promise.resolve(world.catalog);
}

type Tab = "overview" | "agents" | "resources" | "history";

function renderPage(role: "owner" | "member", tab: Tab | string = "overview") {
  world.role = role;
  useMemberStore.setState({ myRole: null });
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "w", name: "W" } as Workspace });
  useAuthStore.setState({ user: { id: ME } as never });
  return render(<MemoryRouter initialEntries={[tab === "overview" ? "/" : `/?tab=${tab}`]}><MyRunnerPage /></MemoryRouter>);
}

const TAB_NAMES: Record<Tab, string> = { overview: "Overview", agents: "Agents", resources: "Resources", history: "History" };
const openTab = (tab: Tab) => fireEvent.click(screen.getByRole("tab", { name: TAB_NAMES[tab] }));

beforeEach(async () => {
  await setLanguage("en");
  world = { agents: [AGENT], runners: [{ ...CONTROLLER, agents: [{ agent_id: "a1", name: "Robin" }] }], history: [], runtime: { user_id: ME, revision: 4, config: { pools: { main: { size: 2 } } }, updated_at: "" }, extensionOn: false, role: "member", catalog: { mode: "direct", controllers: [] }, revisions: [], puts: [], posts: [], manifest: runnerManifest };
  mockedApi.mockReset();
  mockedApi.mockImplementation(route);
});
afterEach(() => vi.clearAllMocks());

describe("Runner entry schema", () => {
  const manifest = runnerManifest as unknown as ExtensionManifest;
  it("covers every field the catalog of revision 39 holds, in every section", () => {
    expect(r39Shape.revision).toBe(39);
    for (const section of ["accounts", "pools", "profiles", "bindings"] as const) {
      const schema = entrySchema(manifest, section);
      expect(schema, section).not.toBeNull();
      expect(unknownFields(schema!, r39Shape[section] as Record<string, unknown>), section).toEqual([]);
    }
  });
  it("a field the schema lacks is reported (positive control)", () => {
    expect(unknownFields(entrySchema(manifest, "pools")!, { ...r39Shape.pools, burst: 1 })).toEqual(["burst"]);
  });
});

describe("MyRunnerPage", () => {
  it("each tab shows its own block: runners, agents, resources", async () => {
    renderPage("member");
    expect(await screen.findByTestId("my-runner-runner-r1")).toHaveTextContent("mini.local");
    expect(screen.queryByTestId("my-runner-agents")).toBeNull();
    openTab("agents");
    expect(screen.getByRole("tab", { name: "Agents" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByTestId("my-runner-agent-a1")).toHaveTextContent("Robin");
    expect(screen.queryByTestId("my-runner-runners")).toBeNull();
    openTab("resources");
    expect(await screen.findByTestId("my-runner-resources-pools")).toHaveTextContent("main");
  });

  it("?tab= opens that tab, an unknown one falls back to the overview, arrows move between tabs", async () => {
    const { unmount } = renderPage("member", "history");
    expect(screen.getByRole("tab", { name: "History" })).toHaveAttribute("aria-selected", "true");
    await screen.findByTestId("my-runner-history");
    fireEvent.keyDown(screen.getByRole("tablist"), { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByTestId("my-runner-runner-r1")).toBeInTheDocument();
    unmount();
    renderPage("member", "nonsense");
    expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true");
  });

  it("every aria-controls points at a panel in the document, labelled by its tab", async () => {
    renderPage("member", "agents");
    await screen.findByTestId("my-runner-agent-a1");
    for (const tab of screen.getAllByRole("tab")) {
      const target = tab.getAttribute("aria-controls");
      if (tab.getAttribute("aria-selected") !== "true") { expect(target).toBeNull(); continue; }
      const panel = document.getElementById(target ?? "");
      expect(panel).toHaveAttribute("role", "tabpanel");
      expect(panel).toHaveAttribute("aria-labelledby", tab.id);
    }
  });

  it("empty states say what to do", async () => {
    world.agents = []; world.runners = []; world.history = [];
    renderPage("member");
    expect(await screen.findByTestId("my-runner-runners-empty")).toBeInTheDocument();
    openTab("agents");
    expect(await screen.findByTestId("my-runner-agents-empty")).toBeInTheDocument();
    openTab("history");
    expect(await screen.findByTestId("my-runner-history-empty")).toBeInTheDocument();
  });

  it("a failed block shows an error with retry while the others stay", async () => {
    world.runners = new Error("boom");
    renderPage("member");
    const err = await screen.findByTestId("my-runner-runners-error");
    expect(within(err).getByRole("button")).toBeInTheDocument();
    openTab("agents");
    expect(await screen.findByTestId("my-runner-agent-a1")).toBeInTheDocument();
  });

  it("saves a changed limit in its row: Save appears only for a change, Enter saves too", async () => {
    renderPage("member", "agents");
    const row = await screen.findByTestId("my-runner-agent-a1");
    expect(within(row).queryByRole("button", { name: "Save" })).toBeNull();
    fireEvent.change(within(row).getByLabelText(/Parallel tasks/), { target: { value: "5" } });
    fireEvent.click(within(row).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/agents/a1/limits", { method: "PUT", body: { max_concurrent_tasks: 5 } }));
    // The save reloads the list, so the row is a new node.
    await waitFor(() => expect(mockedApi.mock.calls.filter(([p]) => p === "/api/v1/me/runtime/agents?workspace_id=ws-1").length).toBeGreaterThan(1));
    const fresh = await screen.findByTestId("my-runner-agent-a1");
    fireEvent.change(within(fresh).getByLabelText(/Parallel tasks/), { target: { value: "7" } });
    fireEvent.keyDown(within(fresh).getByLabelText(/Parallel tasks/), { key: "Enter" });
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/agents/a1/limits", { method: "PUT", body: { max_concurrent_tasks: 7 } }));
  });

  it("Escape puts the stored limit back", async () => {
    renderPage("member", "agents");
    const row = await screen.findByTestId("my-runner-agent-a1");
    const input = within(row).getByLabelText(/Parallel tasks/);
    fireEvent.change(input, { target: { value: "9" } });
    fireEvent.keyDown(input, { key: "Escape" });
    expect(input).toHaveValue("3");
    expect(within(row).queryByRole("button", { name: "Save" })).toBeNull();
  });

  it("lists only the open workspace's agents and shows runner-led ones read-only", async () => {
    world.agents = [AGENT, { ...AGENT, agent_id: "a2", name: "Kidcash", operator_user_id: null, max_concurrent_tasks: 2 }];
    renderPage("owner", "agents");
    const row = await screen.findByTestId("my-runner-agent-a2");
    expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/agents?workspace_id=ws-1");
    expect(within(row).queryByLabelText(/Parallel tasks/)).toBeNull();
    expect(within(row).queryByRole("checkbox")).toBeNull();
    expect(within(row).getByTestId("my-runner-agent-readonly-a2")).toHaveTextContent("2");
    expect(row).toHaveTextContent("No one");
    expect(screen.getByTestId("my-runner-agent-a1")).toHaveTextContent("You");
    expect(within(screen.getByTestId("my-runner-agent-a1")).getByLabelText(/Parallel tasks/)).toBeInTheDocument();
  });

  it("refuses a limit outside 0..1000 and offers no Save", async () => {
    renderPage("member", "agents");
    const row = await screen.findByTestId("my-runner-agent-a1");
    fireEvent.change(within(row).getByLabelText(/Parallel tasks/), { target: { value: "1001" } });
    expect(within(row).queryByRole("button", { name: "Save" })).toBeNull();
    expect(within(row).getByRole("alert")).toBeInTheDocument();
  });

  it("assigns and takes back runtime_manager", async () => {
    renderPage("member", "agents");
    const row = await screen.findByTestId("my-runner-agent-a1");
    fireEvent.click(within(row).getByRole("checkbox"));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/managers", { method: "POST", body: { agent_id: "a1" } }));
  });

  it("the personal document is shown read-only: no Edit, no Add, no JSON", async () => {
    renderPage("member", "resources");
    const pools = await screen.findByTestId("my-runner-resources-pools");
    expect(pools).toHaveTextContent("main");
    expect(within(pools).queryByRole("button", { name: "Edit" })).toBeNull();
    expect(within(pools).queryByRole("button", { name: "Add" })).toBeNull();
    expect(screen.getByTestId("my-runner-resources")).toHaveTextContent("read-only here");
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("names the author of a revision: you, or the agent by name", async () => {
    world.history = [
      { id: 2, revision: 4, kind: "config", detail: null, author_type: "agent", author_id: "a1", created_at: new Date().toISOString() },
      { id: 1, revision: 3, kind: "limits", detail: null, author_type: "user", author_id: ME, created_at: new Date().toISOString() },
    ];
    renderPage("member", "history");
    const history = await screen.findByTestId("my-runner-history");
    await waitFor(() => expect(history).toHaveTextContent("Agent Robin"));
    expect(history).toHaveTextContent("You");
  });

  it("offers the enable button to an admin only, and one click enables", async () => {
    const { unmount } = renderPage("member");
    await screen.findByTestId("my-runner-runner-r1");
    expect(screen.queryByTestId("my-runner-admin")).toBeNull();
    unmount();

    renderPage("owner");
    fireEvent.click(await screen.findByRole("button", { name: "Turn on Runner" }));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/extensions/runner/enable", expect.objectContaining({ method: "POST" })));
  });

  it("ignores a role cached in the shared store", async () => {
    renderPage("member");
    act(() => useMemberStore.setState({ myRole: "owner" }));
    await screen.findByTestId("my-runner-runner-r1");
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/members/me"));
    expect(screen.queryByTestId("my-runner-admin")).toBeNull();
  });

  it("a late owner answer for the old workspace cannot unlock the new one", async () => {
    let release: (v: { role: string }) => void = () => {};
    world.slowOwner = new Promise((r) => { release = r; });
    renderPage("owner");
    const ws = useWorkspaceStore.getState().currentWorkspace!;
    act(() => useWorkspaceStore.setState({ currentWorkspace: { ...ws, id: "ws-2" } }));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-2/members/me"));
    await act(async () => { release({ role: "owner" }); await Promise.resolve(); });
    expect(screen.queryByTestId("my-runner-admin")).toBeNull();
  });

  it("does not carry the previous workspace's extension status over a switch", async () => {
    world.extensionOn = true;
    world.roleWs2 = "owner";
    renderPage("owner");
    await waitFor(() => expect(screen.getByTestId("my-runner-admin")).toHaveTextContent("Turning it off"));
    const ws = useWorkspaceStore.getState().currentWorkspace!;
    let release: (v: unknown) => void = () => {};
    world.slowCatalogWs2 = new Promise((r) => { release = r; });
    act(() => useWorkspaceStore.setState({ currentWorkspace: { ...ws, id: "ws-2" } }));
    // The role for ws-2 lands while its catalog is still pending: the old "on" state must not show.
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-2/members/me"));
    await act(async () => { await new Promise((r) => setTimeout(r, 50)); });
    await screen.findByTestId("my-runner-admin");
    expect(screen.getByTestId("my-runner-admin")).not.toHaveTextContent("Turning it off");
    await act(async () => { release({ extensions: [{ id: "runner", name: "Runner", version: "1", kind: "plugin", category: "agents", status: { enabled: false, source: "none" } }] }); });
    expect(await screen.findByRole("button", { name: "Turn on Runner" })).toBeInTheDocument();
  });

  it("a failed enable shows the server's message", async () => {
    world.enableError = new ApiRequestError("connect the extension first", "not_found", 404);
    const toastError = vi.spyOn(toast, "error");
    renderPage("owner");
    fireEvent.click(await screen.findByRole("button", { name: "Turn on Runner" }));
    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(screen.getByRole("button", { name: "Turn on Runner" })).toBeEnabled();
    toastError.mockRestore();
  });

  it("once Runner is on, the same switch turns it off after an inline confirm", async () => {
    world.extensionOn = true;
    renderPage("owner");
    fireEvent.click(await screen.findByRole("button", { name: "Turn off Runner" }));
    expect(screen.queryByRole("button", { name: "Turn on Runner" })).toBeNull();
    const ask = screen.getByTestId("my-runner-disable-confirm");
    expect(ask).toHaveTextContent("take no new ones");
    expect(mockedApi).not.toHaveBeenCalledWith(expect.stringMatching(/\/disable$/), expect.anything());
    fireEvent.click(within(ask).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByTestId("my-runner-disable-confirm")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Turn off Runner" }));
    fireEvent.click(within(screen.getByTestId("my-runner-disable-confirm")).getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/extensions/runner/disable", { method: "POST", body: { confirm: true } }));
  });

  it("while the status loads, the card claims neither on nor off", async () => {
    let release: (v: unknown) => void = () => {};
    const slow = new Promise((r) => { release = r; });
    mockedApi.mockImplementation((path: string, init?: { method?: string }) =>
      path.endsWith("/workspaces/ws-1/extensions") && !init?.method ? slow : route(path, init));
    renderPage("owner");
    const card = await screen.findByTestId("my-runner-admin");
    expect(card).not.toHaveTextContent("Runner is off");
    expect(card).not.toHaveTextContent("Runner is on");
    await act(async () => { release({ extensions: [{ id: "runner", name: "Runner", version: "1", kind: "plugin", category: "agents", status: { enabled: true, source: "workspace" } }] }); });
    await waitFor(() => expect(card).toHaveTextContent("Turning it off"));
  });

  it("the switch lives on the overview only", async () => {
    world.extensionOn = true;
    renderPage("owner");
    await screen.findByRole("button", { name: "Turn off Runner" });
    for (const tab of ["agents", "resources", "history"] as const) {
      openTab(tab);
      expect(screen.queryByTestId("my-runner-admin")).toBeNull();
    }
  });
  describe("runner program", () => {
    it("not installed yet: an admin is sent to its catalog entry", async () => {
      world.program = "catalog";
      renderPage("owner");
      const card = await screen.findByTestId("my-runner-program-missing");
      expect(within(card).getByRole("link", { name: "Open in the catalog" })).toHaveAttribute("href", "/w/w/extensions/agent-runner");
    });

    it("installed: connecting takes the address and the consent, here", async () => {
      world.program = "installed";
      renderPage("owner");
      const form = await screen.findByTestId("my-runner-program-address");
      const connect = within(form).getByRole("button", { name: "Connect" });
      fireEvent.change(within(form).getByLabelText("Runner address"), { target: { value: "runner.local" } });
      fireEvent.click(within(form).getByRole("checkbox"));
      fireEvent.click(connect);
      expect(await within(form).findByRole("alert")).toHaveTextContent("http:// or https://");
      expect(world.posts).toHaveLength(0);
      fireEvent.change(within(form).getByLabelText("Runner address"), { target: { value: "https://runner.example.com" } });
      fireEvent.click(connect);
      await waitFor(() => expect(world.posts).toContainEqual({
        path: "/api/v1/workspaces/ws-1/extensions/agent-runner/connect",
        body: { settings: { runner_url: "https://runner.example.com" }, consent: { capabilities: ["api:tasks:read", "events:task.assigned"] } },
      }));
    });

    it("no consent, no connect", async () => {
      world.program = "installed";
      renderPage("owner");
      const form = await screen.findByTestId("my-runner-program-address");
      fireEvent.change(within(form).getByLabelText("Runner address"), { target: { value: "https://runner.example.com" } });
      expect(within(form).getByRole("button", { name: "Connect" })).toBeDisabled();
    });

    it("a runner that declares only network domains still shows them and asks for consent", async () => {
      world.program = "installed";
      world.programCaps = { network: { allowed_domains: ["runner.example.com"] } };
      renderPage("owner");
      const form = await screen.findByTestId("my-runner-program-address");
      expect(within(form).getByTestId("my-runner-program-domains")).toHaveTextContent("runner.example.com");
      fireEvent.change(within(form).getByLabelText("Runner address"), { target: { value: "https://runner.example.com" } });
      const connect = within(form).getByRole("button", { name: "Connect" });
      expect(connect).toBeDisabled();
      fireEvent.click(within(form).getByRole("checkbox"));
      expect(connect).toBeEnabled();
    });

    it("connected: health, the address and the catalog update are on this screen; no second switch", async () => {
      world.program = "connected";
      renderPage("owner");
      expect(await screen.findByTestId("my-runner-program-health")).toHaveTextContent(/not answering.*connection refused/);
      expect(screen.getByLabelText("Runner address")).toHaveValue("https://runner.example.com");
      expect(within(screen.getByTestId("my-runner-program")).queryByRole("switch")).toBeNull();
      const update = await screen.findByTestId("my-runner-program-update");
      expect(update).toHaveTextContent("Update 0.2.0 available");
      expect(within(update).getByRole("link")).toHaveAttribute("href", "/w/w/extensions?tab=updates");
    });

    it("connected but blocked: the reason and the update stay; no address edit", async () => {
      world.program = "connected";
      world.programExtra = { blocked: true, blocked_reason: "CVE-2026-1" };
      renderPage("owner");
      expect(await screen.findByTestId("my-runner-program-not-allowed")).toHaveTextContent("CVE-2026-1");
      expect(screen.queryByTestId("my-runner-program-address")).toBeNull();
      expect(await screen.findByTestId("my-runner-program-update")).toBeInTheDocument();
    });

    it("connected but no longer allowed: the address stays editable", async () => {
      world.program = "connected";
      world.programExtra = { allowed: false };
      renderPage("owner");
      expect(await screen.findByTestId("my-runner-program-not-allowed")).toBeInTheDocument();
      expect(screen.getByLabelText("Runner address")).toHaveValue("https://runner.example.com");
    });

    it("a member sees no program card", async () => {
      world.program = "connected";
      renderPage("member");
      await screen.findByTestId("my-runner-runner-r1");
      await act(async () => {});
      expect(screen.queryByTestId("my-runner-program")).toBeNull();
    });
  });

  describe("workspace runner catalog", () => {
    const CATALOG = {
      schema_version: 2,
      controllers: { "controller-mini": { host: "controller.example.invalid", reporter_grant_id: "g0", capabilities: ["code"], heartbeat_max_age_seconds: 120 } },
      accounts: { "claude/alex": { provider: "anthropic", credential_ref: "", quota_pools_by_mode: { subscription: ["claude-max"] } } },
      pools: {
        "claude-max": { provider: "anthropic", resource_ref: "claude-max", aliases: [], max_concurrency: 4 },
        "codex-plus": { provider: "openai", resource_ref: "codex-plus", aliases: [], max_concurrency: 2 },
      },
      profiles: {
        "claude-opus": { controller_ref: "controller-mini", account_ref: "claude/alex", harness: "claude_code", model: "opus", model_developer: "anthropic", model_family: "claude", execution_mode: "subscription", capabilities: ["code"], enabled: true },
        "claude-sonnet": { controller_ref: "controller-mini", account_ref: "claude/alex", harness: "claude_code", model: "sonnet", model_developer: "anthropic", model_family: "claude", execution_mode: "subscription", capabilities: ["code"], enabled: true },
      },
      bindings: {
        vera: {
          binding: { agent_id: "0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d01", workspace_id: "0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02", grant_id: "0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d03" },
          permitted_profiles: ["claude-opus"],
          policy: { primary_profiles: ["claude-opus"], quota_edges: {}, preferred_accounts: {}, api_reserve_providers: [], max_attempts: 2, evidence_max_age_seconds: 1800, qa: { mode: "prefer", quota_fallback: false } },
          enabled: true,
        },
      },
    };
    const SECTIONS = ["accounts", "pools", "profiles", "bindings"] as const;
    // The row is looked up again on every attempt: a save reloads the catalog and replaces the list.
    const edit = async (section: string, name: string) => {
      await screen.findByTestId(`my-runner-resources-${section}`);
      await waitFor(() => {
        if (!screen.queryByTestId("my-runner-entry-editor")) {
          const row = within(screen.getByTestId(`my-runner-resources-${section}`)).getByText(name).closest("li") as HTMLElement;
          fireEvent.click(within(row).getByRole("button", { name: "Edit" }));
        }
        expect(screen.getByTestId("my-runner-entry-editor")).toBeInTheDocument();
      });
      return screen.getByTestId("my-runner-entry-editor");
    };
    beforeEach(() => {
      world.catalog = { mode: "managed", revision: 37, digest: "c".repeat(64), enabled: true, drain_requested: false, config: CATALOG, controllers: [] };
      world.revisions = [{ revision: 37, digest: "c".repeat(64), enabled: true, author_id: "u-fleet", author_name: "Fleet Admin", created_at: new Date().toISOString() }];
    });

    it("shows the workspace catalog, not the personal config, and its revisions", async () => {
      renderPage("owner", "resources");
      expect(await screen.findByTestId("my-runner-resources-accounts")).toHaveTextContent("claude/alex");
      expect(screen.getByTestId("my-runner-resources-pools")).toHaveTextContent("claude-max");
      expect(screen.getByTestId("my-runner-resources-profiles")).toHaveTextContent("claude-opus");
      expect(screen.getByTestId("my-runner-resources-bindings")).toHaveTextContent("vera");
      expect(screen.getByTestId("my-runner-resources")).not.toHaveTextContent("Nothing here yet");
      openTab("history");
      const history = await screen.findByTestId("my-runner-history");
      await waitFor(() => expect(history).toHaveTextContent("Revision 37"));
      expect(history).toHaveTextContent("Fleet Admin");
      expect(mockedApi).not.toHaveBeenCalledWith("/api/v1/me/runtime");
    });

    it("an edited field is saved with the revision and lands in the history", async () => {
      renderPage("owner", "resources");
      const editor = await edit("pools", "claude-max");
      fireEvent.change(within(editor).getByLabelText(/Concurrent runs, at most/), { target: { value: "6" } });
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(world.puts).toHaveLength(1));
      expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/runtime", {
        method: "PUT",
        body: { if_revision: 37, config: { ...CATALOG, pools: { ...CATALOG.pools, "claude-max": { ...CATALOG.pools["claude-max"], max_concurrency: 6 } } } },
      });
      await waitFor(() => expect(screen.getByTestId("my-runner-resources")).toHaveTextContent("revision 38"));
      openTab("history");
      const history = await screen.findByTestId("my-runner-history");
      await waitFor(() => expect(history).toHaveTextContent("Revision 38"));
      expect(history).toHaveTextContent("You");
    });

    it("a stale revision reloads the latest catalog", async () => {
      renderPage("owner", "resources");
      const pools = await screen.findByTestId("my-runner-resources-pools");
      (world.catalog as { revision: number }).revision = 40;
      fireEvent.click(within(within(pools).getByText("claude-max").closest("li") as HTMLElement).getByRole("button", { name: "Remove" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Someone changed this")));
      await waitFor(() => expect(screen.getByTestId("my-runner-resources-pools")).toHaveTextContent("claude-max"));
      expect(screen.getByTestId("my-runner-resources")).toHaveTextContent("revision 40");
    });

    it("a managed catalog with no saved revision shows an empty catalog history, not the personal one", async () => {
      world.revisions = [];
      world.history = [{ id: 1, revision: 3, kind: "limits", detail: null, author_type: "user", author_id: ME, created_at: new Date().toISOString() }];
      renderPage("owner", "history");
      expect(await screen.findByTestId("my-runner-history-empty")).toBeInTheDocument();
      expect(mockedApi).not.toHaveBeenCalledWith(expect.stringMatching(/^\/api\/v1\/me\/runtime\/history/));
    });

    it("no entry of any section is edited as JSON: every editor is a form, with no textarea", async () => {
      renderPage("owner", "resources");
      for (const section of SECTIONS) {
        const name = Object.keys(CATALOG[section]).sort()[0]!;
        const editor = await edit(section, name);
        expect(within(editor).getByRole("button", { name: "Apply" })).toBeInTheDocument();
        expect(editor.querySelector("textarea")).toBeNull();
        expect(within(editor).queryByText(/JSON/)).toBeNull();
        fireEvent.click(within(editor).getByRole("button", { name: "Cancel" }));
        fireEvent.click(within(screen.getByTestId(`my-runner-resources-${section}`)).getByRole("button", { name: "Add" }));
        const fresh = await screen.findByTestId("my-runner-entry-editor");
        expect(fresh.querySelector("textarea")).toBeNull();
        fireEvent.click(within(fresh).getByRole("button", { name: "Cancel" }));
      }
      expect(document.querySelector("textarea")).toBeNull();
    });

    it("a stored credential reference stays hidden and is kept; a new one must be a cred: reference", async () => {
      renderPage("owner", "resources");
      let editor = await edit("accounts", "claude/alex");
      expect(within(editor).getByText("Set")).toBeInTheDocument();
      expect(editor.querySelector("input[type=password]")).toBeNull();
      // pools are picked from the account's provider only
      const pools = within(editor).getByRole("group", { name: "Subscription mode" });
      expect(within(pools).getByLabelText("claude-max")).toBeChecked();
      expect(within(pools).queryByLabelText("codex-plus")).toBeNull();
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(world.puts).toHaveLength(1));
      const sent = (world.puts[0] as { config: typeof CATALOG }).config.accounts["claude/alex"];
      expect(sent).toEqual({ provider: "anthropic", credential_ref: "", quota_pools_by_mode: { subscription: ["claude-max"] } });

      editor = await edit("accounts", "claude/alex");
      fireEvent.click(within(editor).getByRole("button", { name: /Change/ }));
      fireEvent.change(editor.querySelector("input[type=password]")!, { target: { value: "sk-live-123" } });
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      expect(await within(editor).findByText("Doesn't match the expected pattern")).toBeInTheDocument();
      expect(screen.getByTestId("my-runner-entry-editor")).toBeInTheDocument();
    });

    it("a renamed account asks for its credential reference again instead of dropping it", async () => {
      renderPage("owner", "resources");
      const editor = await edit("accounts", "claude/alex");
      fireEvent.change(within(editor).getByRole("textbox", { name: "Name" }), { target: { value: "claude/alex-2" } });
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      expect(await within(editor).findByText(/does not carry the hidden reference over/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Save resources" })).toBeNull();

      fireEvent.click(within(editor).getByRole("button", { name: /Change/ }));
      fireEvent.change(editor.querySelector("input[type=password]")!, { target: { value: "cred:claude-alex-2" } });
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(world.puts).toHaveLength(1));
      const accounts = (world.puts[0] as { config: typeof CATALOG }).config.accounts as Record<string, { credential_ref: string }>;
      expect(Object.keys(accounts)).toEqual(["claude/alex-2"]);
      expect(accounts["claude/alex-2"]!.credential_ref).toBe("cred:claude-alex-2");
    });

    it("an account added in this draft keeps its typed reference when edited again", async () => {
      renderPage("owner", "resources");
      fireEvent.click(within(await screen.findByTestId("my-runner-resources-accounts")).getByRole("button", { name: "Add" }));
      let editor = await screen.findByTestId("my-runner-entry-editor");
      fireEvent.change(within(editor).getByRole("textbox", { name: "Name" }), { target: { value: "openai/new" } });
      fireEvent.change(within(editor).getByLabelText(/^Provider/), { target: { value: "openai" } });
      fireEvent.change(editor.querySelector("input[type=password]")!, { target: { value: "cred:openai-new" } });
      fireEvent.click(within(within(editor).getByRole("group", { name: "Subscription mode" })).getByLabelText("codex-plus"));
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      editor = await edit("accounts", "openai/new");
      fireEvent.change(within(editor).getByRole("textbox", { name: "Name" }), { target: { value: "openai/renamed" } });
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(world.puts).toHaveLength(1));
      const accounts = (world.puts[0] as { config: typeof CATALOG }).config.accounts as Record<string, { credential_ref: string }>;
      expect(accounts["openai/renamed"]!.credential_ref).toBe("cred:openai-new");
      expect(accounts["openai/new"]).toBeUndefined();
    });

    it("a reference is picked from what exists, and first choice only from the allowed profiles", async () => {
      renderPage("owner", "resources");
      const editor = await edit("bindings", "vera");
      const allowed = within(editor).getByRole("group", { name: "Allowed profiles" });
      const first = () => within(editor).getByRole("group", { name: "First choice" });
      expect(within(first()).queryByLabelText("claude-sonnet")).toBeNull();
      fireEvent.click(within(allowed).getByLabelText("claude-sonnet"));
      fireEvent.click(within(first()).getByLabelText("claude-sonnet"));
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(world.puts).toHaveLength(1));
      const sent = (world.puts[0] as { config: typeof CATALOG }).config.bindings.vera;
      expect(sent.permitted_profiles).toEqual(["claude-opus", "claude-sonnet"]);
      expect(sent.policy.primary_profiles).toEqual(["claude-opus", "claude-sonnet"]);
      expect(sent.policy.qa).toEqual({ mode: "prefer", quota_fallback: false });
    });

    it("a profile pointing at a removed account shows it as not found and is not applied", async () => {
      const config = { ...CATALOG, profiles: { ...CATALOG.profiles, "claude-opus": { ...CATALOG.profiles["claude-opus"], account_ref: "gone" } } };
      (world.catalog as { config: unknown }).config = config;
      renderPage("owner", "resources");
      const editor = await edit("profiles", "claude-opus");
      expect(within(editor).getByLabelText(/^Account/)).toHaveDisplayValue("gone (not found)");
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      expect(await within(editor).findByText("Pick one of the options")).toBeInTheDocument();
    });

    it("the server's reason per field reopens the entry with the reason at that field", async () => {
      world.refuse = new ApiRequestError("invalid runtime catalog", "validation_failed", 400, { "config.pools.claude-max.max_concurrency": "invalid" });
      renderPage("owner", "resources");
      const pools = await screen.findByTestId("my-runner-resources-pools");
      fireEvent.click(within(within(pools).getByText("codex-plus").closest("li") as HTMLElement).getByRole("button", { name: "Remove" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      const editor = await screen.findByTestId("my-runner-entry-editor");
      const field = within(editor).getByLabelText(/Concurrent runs, at most/);
      expect(field).toHaveAttribute("aria-invalid", "true");
      expect(within(editor).getByText("This value is not accepted")).toBeInTheDocument();
      expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("one entry needs fixing"));
    });

    it("the server's reason for a field the form lacks is shown in the reopened entry, next to its path", async () => {
      world.refuse = new ApiRequestError("invalid runtime catalog", "validation_failed", 400, { "config.pools.claude-max.burst": "unknown_field" });
      renderPage("owner", "resources");
      const pools = await screen.findByTestId("my-runner-resources-pools");
      fireEvent.click(within(within(pools).getByText("codex-plus").closest("li") as HTMLElement).getByRole("button", { name: "Remove" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      const editor = await screen.findByTestId("my-runner-entry-editor");
      const listed = within(editor).getByTestId("my-runner-entry-server-errors");
      expect(listed).toHaveTextContent("burst");
      expect(listed).toHaveTextContent("This field is not part of the catalog");
    });

    it("an entry name already used is refused before anything is applied", async () => {
      renderPage("owner", "resources");
      const editor = await edit("pools", "codex-plus");
      fireEvent.change(within(editor).getByRole("textbox", { name: "Name" }), { target: { value: "claude-max" } });
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      expect(await within(editor).findByText(/already used/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Save resources" })).toBeNull();
    });

    it("without the Runner form description the catalog is listed but not editable", async () => {
      world.manifest = undefined;
      renderPage("owner", "resources");
      const pools = await screen.findByTestId("my-runner-resources-pools");
      expect(pools).toHaveTextContent("claude-max");
      expect(within(pools).queryByRole("button", { name: "Edit" })).toBeNull();
      expect(screen.getByTestId("my-runner-resources-no-schema")).toBeInTheDocument();
    });

    it("tells an admin that catalog changes apply to the whole workspace, not only to them", async () => {
      renderPage("owner");
      const subtitle = await screen.findByTestId("my-runner-subtitle");
      await waitFor(() => expect(subtitle).toHaveTextContent("Catalog changes apply to the whole workspace."));
      expect(subtitle).not.toHaveTextContent("only you");
    });

    it("keeps the controller version on one line", async () => {
      (world.catalog as { config: { controllers: Record<string, object> } }).config.controllers["controller-mini"] = { ...CATALOG.controllers["controller-mini"], version: "controller-1.4.0-rc1" };
      renderPage("owner");
      const line = await screen.findByTestId("my-runner-controller-controller-mini");
      expect(within(line.closest("li")!).getByText("controller-1.4.0-rc1")).toHaveClass("whitespace-nowrap");
    });

    it("a failed catalog load is an error with retry, never the personal config", async () => {
      world.catalog = new ApiRequestError("boom", "internal", 500);
      renderPage("owner", "resources");
      expect(await screen.findByTestId("my-runner-resources-error")).toBeInTheDocument();
      expect(mockedApi).not.toHaveBeenCalledWith("/api/v1/me/runtime");
    });

    it("without rights to the catalog (403) the personal config stays", async () => {
      world.catalog = new ApiRequestError("forbidden", "forbidden", 403);
      renderPage("member", "resources");
      expect(await screen.findByTestId("my-runner-resources-pools")).toHaveTextContent("main");
      expect(screen.getByTestId("my-runner-subtitle")).toHaveTextContent("Changes affect only you.");
    });

    it("a long catalog can be narrowed by a filter", async () => {
      const bindings: Record<string, unknown> = {};
      for (let i = 0; i < 30; i++) bindings[`agent-${i}`] = { ...CATALOG.bindings.vera, binding: { agent_id: `a${i}`, workspace_id: "ws-1", grant_id: `g${i}` } };
      (world.catalog as { config: unknown }).config = { ...CATALOG, bindings };
      renderPage("owner", "resources");
      const filter = await screen.findByRole("searchbox", { name: /Filter/ });
      fireEvent.change(filter, { target: { value: "agent-1" } });
      const list = screen.getByTestId("my-runner-resources-bindings");
      expect(within(list).getAllByRole("listitem")).toHaveLength(11);
      expect(screen.getByTestId("my-runner-resources-pools")).toHaveTextContent("No matches");
    });

    it("an entry the server refused is reopened even when the filter hides it", async () => {
      const bindings: Record<string, unknown> = {};
      for (let i = 0; i < 30; i++) bindings[`agent-${i}`] = { ...CATALOG.bindings.vera, binding: { agent_id: `a${i}`, workspace_id: "ws-1", grant_id: `g${i}` } };
      (world.catalog as { config: unknown }).config = { ...CATALOG, bindings };
      world.refuse = new ApiRequestError("invalid runtime catalog", "validation_failed", 400, { "config.pools.claude-max.max_concurrency": "invalid" });
      renderPage("owner", "resources");
      fireEvent.change(await screen.findByRole("searchbox", { name: /Filter/ }), { target: { value: "agent-1" } });
      const list = screen.getByTestId("my-runner-resources-bindings");
      fireEvent.click(within(within(list).getByText("agent-10").closest("li") as HTMLElement).getByRole("button", { name: "Remove" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      const editor = await within(screen.getByTestId("my-runner-resources-pools")).findByTestId("my-runner-entry-editor");
      expect(within(editor).getByLabelText(/Concurrent runs, at most/)).toHaveAttribute("aria-invalid", "true");
    });
    it("an admin sees each controller with its applied revision and heartbeat", async () => {
      world.catalog = { ...(world.catalog as object), controllers: [{ controller_ref: "controller-mini", report: { revision: 37, digest: "c".repeat(64), status: "applied" }, received_at: new Date(Date.now() - 30_000).toISOString(), current: true }] };
      renderPage("owner");
      const row = await screen.findByTestId("my-runner-controller-controller-mini");
      expect(row).toHaveTextContent("applied");
      expect(row).toHaveTextContent("applied r37 · desired r37");
      expect(row).toHaveTextContent(/heartbeat .*ago/);
      expect(screen.getByTestId("my-runner-runner-controller-mini")).toHaveTextContent("controller.example.invalid");
    });

    it("a runner that leads agents and the controller it reports as are one row, not two lists", async () => {
      world.runners = [{ ...CONTROLLER, runner_id: "controller-mini", name: "controller-mini", agents: [{ agent_id: "a1", name: "Robin" }] }];
      world.catalog = { ...(world.catalog as object), controllers: [{ controller_ref: "controller-mini", report: { revision: 37, digest: "c".repeat(64), status: "applied" }, received_at: new Date().toISOString(), current: true }] };
      renderPage("owner");
      const ctl = await screen.findByTestId("my-runner-controller-controller-mini");
      const runners = screen.getAllByTestId(/^my-runner-runner-/);
      expect(runners).toHaveLength(1);
      expect(runners[0]).toContainElement(ctl);
      expect(runners[0]).toHaveTextContent("Robin");
      expect(screen.queryByTestId("my-runner-controllers")).toBeNull();
    });

    it("an announcing runner is matched to its controller by name", async () => {
      const config = { ...CATALOG, controllers: { "mini-main": { host: "mini.local", reporter_grant_id: "g0", capabilities: [], heartbeat_max_age_seconds: 120, enabled: true } } };
      world.catalog = { ...(world.catalog as object), config, controllers: [{ controller_ref: "mini-main", report: { revision: 37, digest: "c".repeat(64), status: "applied" }, received_at: new Date().toISOString(), current: true }] };
      renderPage("owner");
      const ctl = await screen.findByTestId("my-runner-controller-mini-main");
      expect(screen.getByTestId("my-runner-runner-r1")).toContainElement(ctl);
      expect(screen.getAllByTestId(/^my-runner-runner-/)).toHaveLength(1);
    });

    it("a controller behind the catalog or silent says so", async () => {
      world.catalog = { ...(world.catalog as object), controllers: [{ controller_ref: "controller-mini", report: { revision: 35, digest: "b".repeat(64), status: "applied" }, received_at: new Date().toISOString(), current: false }] };
      renderPage("owner");
      const row = await screen.findByTestId("my-runner-controller-controller-mini");
      expect(row).toHaveTextContent("behind");
      expect(row).toHaveTextContent("applied r35 · desired r37");
    });

    it("the agents table shows each agent's profiles from the catalog bindings", async () => {
      const vera = CATALOG.bindings.vera.binding.agent_id;
      world.agents = [AGENT, { ...AGENT, agent_id: vera, name: "Vera" }];
      renderPage("owner", "agents");
      await waitFor(() => expect(screen.getByTestId(`my-runner-agent-${vera}`)).toHaveTextContent("claude-opus"));
      expect(screen.getByTestId("my-runner-agent-a1")).not.toHaveTextContent("claude-opus");
    });

    it("history tells a switch from a save and restores an earlier revision after a confirm", async () => {
      const at = new Date().toISOString();
      world.revisions = [
        { revision: 39, digest: "e".repeat(64), enabled: true, author_id: ME, author_name: "Alex", created_at: at },
        { revision: 38, digest: "c".repeat(64), enabled: false, author_id: ME, author_name: "Alex", created_at: at },
        { revision: 37, digest: "c".repeat(64), enabled: true, author_id: "u-fleet", author_name: "Fleet Admin", created_at: at },
      ];
      renderPage("owner", "history");
      const history = await screen.findByTestId("my-runner-history");
      await waitFor(() => expect(history.querySelector('[data-revision="38"]')).toHaveTextContent("Runner turned off"));
      expect(history.querySelector('[data-revision="39"]')).toHaveTextContent("Runner catalog saved");
      // The newest revision is what runs: nothing to restore there.
      expect(within(history.querySelector('[data-revision="39"]') as HTMLElement).queryByRole("button", { name: "Restore" })).toBeNull();
      fireEvent.click(within(history.querySelector('[data-revision="37"]') as HTMLElement).getByRole("button", { name: "Restore" }));
      const ask = screen.getByTestId("my-runner-restore-37");
      expect(ask).toHaveTextContent("as revision 40");
      expect(mockedApi).not.toHaveBeenCalledWith("/api/v1/workspaces/ws-1/runtime/restore", expect.anything());
      fireEvent.click(within(ask).getByRole("button", { name: "Restore" }));
      await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/runtime/restore", { method: "POST", body: { if_revision: 39, revision: 37 } }));
    });

    it("a member, refused the runtime route, sees no controller block", async () => {
      world.catalog = new ApiRequestError("forbidden", "forbidden", 403);
      renderPage("member");
      await screen.findByTestId("my-runner-runner-r1");
      await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/runtime"));
      await act(async () => {});
      expect(screen.queryByTestId("my-runner-controllers")).toBeNull();
      expect(screen.queryByText("Controllers")).toBeNull();
    });
  });
});
