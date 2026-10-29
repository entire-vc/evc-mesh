/**
 * Runner section: six tabs (overview, agents, accounts, pools, profiles, history), every block
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
import { entrySchema, profileLabel, unknownFields } from "@/lib/my-runner";
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
    if (path === "/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/runtime" && init.method === "PUT") return saveCatalog(init.body as { if_revision: number; enabled: boolean; config: unknown });
    world.posts.push({ path, body: init.body });
    return Promise.resolve({});
  }
  if (path === "/api/v1/me/runtime/agents?workspace_id=0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02") return out(world.agents instanceof Error ? world.agents : { agents: world.agents });
  if (path === "/api/v1/me/runtime/runners?workspace_id=0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02") return out(world.runners instanceof Error ? world.runners : { runners: world.runners });
  if (path.startsWith("/api/v1/me/runtime/history")) return out(world.history instanceof Error ? world.history : { history: world.history });
  if (path.endsWith("/members/me")) return path.includes("ws-2") ? Promise.resolve({ role: world.roleWs2 ?? "member" }) : (world.slowOwner ?? Promise.resolve({ role: world.role }));
  if (path === "/api/v1/me/runtime") return out(world.runtime);
  if (path === "/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/runtime") return out(world.catalog);
  if (path.startsWith("/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/runtime/revisions")) return Promise.resolve({ managed: (world.catalog as { mode?: string })?.mode === "managed", revisions: world.revisions });
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

type Tab = "overview" | "agents" | "accounts" | "pools" | "profiles" | "history";

function renderPage(role: "owner" | "member", tab: Tab | string = "overview") {
  world.role = role;
  useMemberStore.setState({ myRole: null });
  useWorkspaceStore.setState({ currentWorkspace: { id: "0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02", slug: "w", name: "W" } as Workspace });
  useAuthStore.setState({ user: { id: ME } as never });
  return render(<MemoryRouter initialEntries={[tab === "overview" ? "/" : `/?tab=${tab}`]}><MyRunnerPage /></MemoryRouter>);
}

const TAB_NAMES: Record<Tab, string> = { overview: "Overview", agents: "Agents", accounts: "Accounts", pools: "Pools", profiles: "Profiles", history: "History" };
const openTab = (tab: Tab) => fireEvent.click(screen.getByRole("tab", { name: TAB_NAMES[tab] }));
/** Opens the agent's dialog from its row (looked up again: a save reloads the list). */
const openAgent = async (id: string) => {
  const row = await screen.findByTestId(`my-runner-agent-${id}`);
  fireEvent.click(within(row).getByRole("button", { name: /^Edit/ }));
  return screen.getByTestId("my-runner-agent-dialog");
};

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

describe("profileLabel", () => {
  const t = (k: string) => ({ "profiles.mode.subscription": "subscription", "profiles.mode.api": "API", "profiles.defaultModel": "default model" })[k] ?? k;
  const config = { profiles: {
    "claude/work-team/subscription": { account_ref: "claude/work-team", harness: "claude_code", model: "claude-default", execution_mode: "subscription" },
    "anthropic-api/acme/api": { account_ref: "anthropic-api/acme", harness: "claude_code", model: "claude-sonnet-5-5", execution_mode: "api" },
    "glm/alex/subscription": { account_ref: "glm/alex", harness: "claude_code", model: "glm-5.3", execution_mode: "subscription" },
    "codex/alex/subscription": { account_ref: "codex/alex", harness: "codex", model: "codex-default", execution_mode: "subscription" },
  } };
  it("reads as tool · mode and account · model, the shapes of the live catalog", () => {
    expect(profileLabel("claude/work-team/subscription", config, t)).toBe("Claude Code · subscription work-team · default model");
    expect(profileLabel("anthropic-api/acme/api", config, t)).toBe("Claude Code · API acme · Sonnet 5.5");
    expect(profileLabel("glm/alex/subscription", config, t)).toBe("Claude Code · subscription alex · GLM 5.3");
    expect(profileLabel("codex/alex/subscription", config, t)).toBe("Codex · subscription alex · default model");
  });
  it("a profile the catalog does not hold stays its key", () => {
    expect(profileLabel("gone/x", config, t)).toBe("gone/x");
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
    openTab("pools");
    expect(await screen.findByTestId("my-runner-resources-pools")).toHaveTextContent("main");
    expect(screen.queryByTestId("my-runner-resources-accounts")).toBeNull();
  });

  it("an old ?tab=resources link opens Accounts; there is no Resources tab", async () => {
    renderPage("member", "resources");
    expect(screen.getByRole("tab", { name: "Accounts" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByRole("tab", { name: "Resources" })).toBeNull();
    expect(await screen.findByTestId("my-runner-resources-accounts")).toBeInTheDocument();
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

  it("the row is read-only; Edit opens the agent's dialog, which saves a changed limit (Save only for a change, Enter too)", async () => {
    renderPage("member", "agents");
    const row = await screen.findByTestId("my-runner-agent-a1");
    expect(within(row).queryByLabelText(/Parallel tasks/)).toBeNull();
    expect(within(row).getByTestId("my-runner-agent-limit-a1")).toHaveTextContent("3");
    const dialog = await openAgent("a1");
    expect(dialog).toHaveAttribute("role", "dialog");
    expect(dialog).toHaveTextContent("Agent Robin");
    expect(within(dialog).queryByRole("button", { name: "Save" })).toBeNull();
    fireEvent.change(within(dialog).getByLabelText(/Parallel tasks/), { target: { value: "5" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/agents/a1/limits", { method: "PUT", body: { max_concurrent_tasks: 5 } }));
    // The save reloads the list; the dialog stays open on the agent.
    await waitFor(() => expect(mockedApi.mock.calls.filter(([p]) => p === "/api/v1/me/runtime/agents?workspace_id=0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02").length).toBeGreaterThan(1));
    const fresh = await screen.findByTestId("my-runner-agent-dialog");
    fireEvent.change(within(fresh).getByLabelText(/Parallel tasks/), { target: { value: "7" } });
    fireEvent.keyDown(within(fresh).getByLabelText(/Parallel tasks/), { key: "Enter" });
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/agents/a1/limits", { method: "PUT", body: { max_concurrent_tasks: 7 } }));
  });

  it("the agent dialog closes on Escape and on its close button", async () => {
    renderPage("member", "agents");
    fireEvent.keyDown(await openAgent("a1"), { key: "Escape" });
    expect(screen.queryByTestId("my-runner-agent-dialog")).toBeNull();
    fireEvent.click(within(await openAgent("a1")).getByRole("button", { name: "Close" }));
    expect(screen.queryByTestId("my-runner-agent-dialog")).toBeNull();
  });

  it("Escape puts the stored limit back", async () => {
    renderPage("member", "agents");
    const row = await openAgent("a1");
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
    expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/agents?workspace_id=0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02");
    // no catalog to edit and not the operator: nothing to change here
    expect(within(row).queryByRole("button", { name: /^Edit/ })).toBeNull();
    expect(within(row).getByTestId("my-runner-agent-limit-a2")).toHaveTextContent("2");
    expect(row).toHaveTextContent("No one");
    expect(screen.getByTestId("my-runner-agent-a1")).toHaveTextContent("You");
    expect(within(await openAgent("a1")).getByLabelText(/Parallel tasks/)).toBeInTheDocument();
  });

  it("refuses a limit outside 0..1000 and offers no Save", async () => {
    renderPage("member", "agents");
    const row = await openAgent("a1");
    fireEvent.change(within(row).getByLabelText(/Parallel tasks/), { target: { value: "1001" } });
    expect(within(row).queryByRole("button", { name: "Save" })).toBeNull();
    expect(within(row).getByRole("alert")).toBeInTheDocument();
  });

  it("assigns and takes back runtime_manager", async () => {
    renderPage("member", "agents");
    const row = await openAgent("a1");
    fireEvent.click(within(row).getByRole("checkbox"));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/managers", { method: "POST", body: { agent_id: "a1" } }));
  });

  it("the personal document is shown read-only: no Edit, no Add, no JSON", async () => {
    renderPage("member", "pools");
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
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/extensions/runner/enable", expect.objectContaining({ method: "POST" })));
  });

  it("ignores a role cached in the shared store", async () => {
    renderPage("member");
    act(() => useMemberStore.setState({ myRole: "owner" }));
    await screen.findByTestId("my-runner-runner-r1");
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/members/me"));
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
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/extensions/runner/disable", { method: "POST", body: { confirm: true } }));
  });

  it("while the status loads, the card claims neither on nor off", async () => {
    let release: (v: unknown) => void = () => {};
    const slow = new Promise((r) => { release = r; });
    mockedApi.mockImplementation((path: string, init?: { method?: string }) =>
      path.endsWith("/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/extensions") && !init?.method ? slow : route(path, init));
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
    for (const tab of ["agents", "accounts", "pools", "profiles", "history"] as const) {
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
        path: "/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/extensions/agent-runner/connect",
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

    it("blocked without a stated reason still says so: the card is never a bare title", async () => {
      world.program = "connected";
      world.programExtra = { blocked: true, blocked_reason: "" };
      renderPage("owner");
      expect(await screen.findByTestId("my-runner-program-not-allowed")).toHaveTextContent("Blocked on this instance");
    });

    it("installed and connected: version, state and address are shown", async () => {
      world.program = "connected";
      renderPage("owner");
      const card = await screen.findByTestId("my-runner-program");
      await waitFor(() => expect(card).toHaveTextContent("Version 0.1.0"));
      expect(within(card).getByTestId("my-runner-program-health")).toHaveTextContent("not answering");
      expect(within(card).getByLabelText("Runner address")).toHaveValue("https://runner.example.com");
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
    const SECTIONS = ["accounts", "pools", "profiles"] as const;
    const VERA = CATALOG.bindings.vera.binding.agent_id;
    const OPUS = "Claude Code · subscription alex · Opus";
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

    it("shows the workspace catalog, not the personal config, one tab per section, and its revisions", async () => {
      renderPage("owner", "accounts");
      expect(await screen.findByTestId("my-runner-resources-accounts")).toHaveTextContent("claude/alex");
      openTab("pools");
      expect(screen.getByTestId("my-runner-resources-pools")).toHaveTextContent("claude-max");
      openTab("profiles");
      expect(screen.getByTestId("my-runner-resources-profiles")).toHaveTextContent("claude-opus");
      expect(screen.getByTestId("my-runner-resources")).not.toHaveTextContent("Nothing here yet");
      // per-agent settings live on the Agents tab only
      expect(screen.queryByTestId("my-runner-resources-bindings")).toBeNull();
      expect(screen.getByTestId("my-runner-resources")).not.toHaveTextContent("Agent settings");
      openTab("history");
      const history = await screen.findByTestId("my-runner-history");
      await waitFor(() => expect(history).toHaveTextContent("Revision 37"));
      expect(history).toHaveTextContent("Fleet Admin");
      expect(mockedApi).not.toHaveBeenCalledWith("/api/v1/me/runtime");
    });

    it("an edited field is saved with the revision and lands in the history", async () => {
      renderPage("owner", "pools");
      const editor = await edit("pools", "claude-max");
      fireEvent.change(within(editor).getByLabelText(/Concurrent runs, at most/), { target: { value: "6" } });
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(world.puts).toHaveLength(1));
      expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/runtime", {
        method: "PUT",
        body: { if_revision: 37, config: { ...CATALOG, pools: { ...CATALOG.pools, "claude-max": { ...CATALOG.pools["claude-max"], max_concurrency: 6 } } } },
      });
      await waitFor(() => expect(screen.getByTestId("my-runner-resources")).toHaveTextContent("revision 38"));
      openTab("history");
      const history = await screen.findByTestId("my-runner-history");
      await waitFor(() => expect(history).toHaveTextContent("Revision 38"));
      expect(history).toHaveTextContent("You");
    });

    it("marks as required only a field that needs a value: an empty list of other names is a valid answer", async () => {
      renderPage("owner", "pools");
      const editor = await edit("pools", "claude-max");
      const label = (text: string) => within(editor).getByText(text, { selector: "label" });
      expect(label("Concurrent runs, at most")).toHaveTextContent("*");
      expect(label("Other names")).not.toHaveTextContent("*");
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      expect(await screen.findByRole("button", { name: "Save resources" })).toBeInTheDocument();
    });

    it("a long pool name breaks between its segments, never inside one", async () => {
      world.catalog = { ...(world.catalog as object), config: { ...CATALOG, pools: { "provider-api/team-a/spend": CATALOG.pools["claude-max"] } } };
      renderPage("owner", "pools");
      const pools = await screen.findByTestId("my-runner-resources-pools");
      const name = within(pools).getByText((_, el) => el?.textContent === "provider-api/team-a/spend" && el.tagName === "SPAN");
      expect(name).not.toHaveClass("break-all");
      expect(name.querySelectorAll("wbr")).toHaveLength(2);
      expect(name).toHaveAttribute("title", "provider-api/team-a/spend");
    });

    it("a stale revision reloads the latest catalog", async () => {
      renderPage("owner", "pools");
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
      world.agents = [AGENT, { ...AGENT, agent_id: VERA, name: "Vera" }];
      renderPage("owner", "accounts");
      for (const section of SECTIONS) {
        openTab(section);
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
      openTab("agents");
      const dialog = await openAgent(VERA);
      expect(within(dialog).getByTestId("my-runner-entry-editor")).toBeInTheDocument();
      expect(document.querySelector("textarea")).toBeNull();
    });

    it("a stored credential reference stays hidden and is kept; a new one must be a cred: reference", async () => {
      renderPage("owner", "accounts");
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
      renderPage("owner", "accounts");
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
      renderPage("owner", "accounts");
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

    it("Edit in the agent's row: profiles are picked by readable name, first choice only from the allowed ones, saved at once", async () => {
      world.agents = [AGENT, { ...AGENT, agent_id: VERA, name: "Vera" }];
      renderPage("owner", "agents");
      const dialog = await openAgent(VERA);
      expect(dialog).toHaveTextContent("A profile is a launch recipe");
      const editor = within(dialog).getByTestId("my-runner-entry-editor");
      const allowed = within(editor).getByRole("group", { name: "Allowed profiles" });
      expect(editor).toHaveTextContent("Allow several so it can switch");
      expect(within(allowed).getByLabelText(`${OPUS} (claude-opus)`)).toBeChecked();
      const first = () => within(editor).getByRole("group", { name: "First choice" });
      expect(within(first()).queryByLabelText(/\(claude-sonnet\)/)).toBeNull();
      fireEvent.click(within(allowed).getByLabelText(/\(claude-sonnet\)/));
      fireEvent.click(within(first()).getByLabelText(/\(claude-sonnet\)/));
      fireEvent.click(within(editor).getByRole("button", { name: "Save settings" }));
      await waitFor(() => expect(world.puts).toHaveLength(1));
      await waitFor(() => expect(screen.queryByTestId("my-runner-agent-dialog")).toBeNull());
      expect(toast.success).toHaveBeenCalledWith("Agent settings saved");
      const sent = (world.puts[0] as { config: typeof CATALOG }).config.bindings.vera;
      expect(sent.permitted_profiles).toEqual(["claude-opus", "claude-sonnet"]);
      expect(sent.policy.primary_profiles).toEqual(["claude-opus", "claude-sonnet"]);
      expect(sent.policy.qa).toEqual({ mode: "prefer", quota_fallback: false });
    });

    it("another member's agent: the dialog shows its limit and management read-only and says who sets them", async () => {
      world.agents = [AGENT, { ...AGENT, agent_id: VERA, name: "Vera", operator_user_id: "someone-else", max_concurrent_tasks: 4, runtime_manager: true }];
      renderPage("owner", "agents");
      const dialog = await openAgent(VERA);
      const work = within(dialog).getByTestId("my-runner-agent-work-readonly");
      expect(work).toHaveTextContent("Parallel tasks");
      expect(work).toHaveTextContent("4");
      expect(work).toHaveTextContent("Managesyes");
      expect(work).toHaveTextContent("Another member operates this agent and sets its limits.");
      expect(within(dialog).queryByLabelText(/Parallel tasks/)).toBeNull();
      expect(within(dialog).getByTestId("my-runner-entry-editor")).toBeInTheDocument();
    });

    it("own agent: the management switch says what the agent may read and edit", async () => {
      renderPage("owner", "agents");
      const dialog = await openAgent("a1");
      expect(within(dialog).queryByTestId("my-runner-agent-work-readonly")).toBeNull();
      expect(dialog).toHaveTextContent("The agent reads and edits your runner settings, limits, and the runner catalog of workspaces where you are an admin, with its own key");
    });

    it("a profile pointing at a removed account shows it as not found and is not applied", async () => {
      const config = { ...CATALOG, profiles: { ...CATALOG.profiles, "claude-opus": { ...CATALOG.profiles["claude-opus"], account_ref: "gone" } } };
      (world.catalog as { config: unknown }).config = config;
      renderPage("owner", "profiles");
      const editor = await edit("profiles", "claude-opus");
      expect(within(editor).getByLabelText(/^Account/)).toHaveDisplayValue("gone (not found)");
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      expect(await within(editor).findByText("Pick one of the options")).toBeInTheDocument();
    });

    it("the server's reason per field reopens the entry with the reason at that field", async () => {
      world.refuse = new ApiRequestError("invalid runtime catalog", "validation_failed", 400, { "config.pools.claude-max.max_concurrency": "invalid" });
      renderPage("owner", "pools");
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
      renderPage("owner", "pools");
      const pools = await screen.findByTestId("my-runner-resources-pools");
      fireEvent.click(within(within(pools).getByText("codex-plus").closest("li") as HTMLElement).getByRole("button", { name: "Remove" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      const editor = await screen.findByTestId("my-runner-entry-editor");
      const listed = within(editor).getByTestId("my-runner-entry-server-errors");
      expect(listed).toHaveTextContent("burst");
      expect(listed).toHaveTextContent("This field is not part of the catalog");
    });

    it("an entry name already used is refused before anything is applied", async () => {
      renderPage("owner", "pools");
      const editor = await edit("pools", "codex-plus");
      fireEvent.change(within(editor).getByRole("textbox", { name: "Name" }), { target: { value: "claude-max" } });
      fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
      expect(await within(editor).findByText(/already used/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Save resources" })).toBeNull();
    });

    it("without the Runner form description the catalog is listed but not editable", async () => {
      world.manifest = undefined;
      renderPage("owner", "pools");
      const pools = await screen.findByTestId("my-runner-resources-pools");
      expect(pools).toHaveTextContent("claude-max");
      expect(within(pools).queryByRole("button", { name: "Edit" })).toBeNull();
      expect(screen.getByTestId("my-runner-resources-no-schema")).toBeInTheDocument();
    });

    it("tells an admin that catalog changes apply to the whole workspace, not only to them", async () => {
      renderPage("owner");
      const subtitle = await screen.findByTestId("my-runner-subtitle");
      await waitFor(() => expect(subtitle).toHaveTextContent("Changes here apply to the whole workspace."));
      expect(subtitle).not.toHaveTextContent("read-only");
    });

    it("keeps the controller version on one line", async () => {
      (world.catalog as { config: { controllers: Record<string, object> } }).config.controllers["controller-mini"] = { ...CATALOG.controllers["controller-mini"], version: "controller-1.4.0-rc1" };
      renderPage("owner");
      const line = await screen.findByTestId("my-runner-controller-controller-mini");
      expect(within(line.closest("li")!).getByText("controller-1.4.0-rc1")).toHaveClass("whitespace-nowrap");
    });

    it("a failed catalog load is an error with retry, never the personal config", async () => {
      world.catalog = new ApiRequestError("boom", "internal", 500);
      renderPage("owner", "pools");
      expect(await screen.findByTestId("my-runner-resources-error")).toBeInTheDocument();
      expect(mockedApi).not.toHaveBeenCalledWith("/api/v1/me/runtime");
    });

    it("without rights to the catalog (403) the personal config stays", async () => {
      world.catalog = new ApiRequestError("forbidden", "forbidden", 403);
      renderPage("member", "pools");
      expect(await screen.findByTestId("my-runner-resources-pools")).toHaveTextContent("main");
      expect(screen.getByTestId("my-runner-subtitle")).toHaveTextContent("shown read-only");
    });

    const manyPools = () => {
      const pools: Record<string, unknown> = { ...CATALOG.pools };
      for (let i = 0; i < 30; i++) pools[`pool-${i}`] = { ...CATALOG.pools["claude-max"], resource_ref: `pool-${i}` };
      (world.catalog as { config: unknown }).config = { ...CATALOG, pools };
    };

    it("a long list can be narrowed by a filter; the filter belongs to its tab", async () => {
      manyPools();
      renderPage("owner", "pools");
      const filter = await screen.findByRole("searchbox", { name: /Filter/ });
      fireEvent.change(filter, { target: { value: "pool-1" } });
      expect(within(screen.getByTestId("my-runner-resources-pools")).getAllByRole("listitem")).toHaveLength(11);
      fireEvent.change(filter, { target: { value: "nothing-like-it" } });
      expect(screen.getByTestId("my-runner-resources-pools")).toHaveTextContent("No matches");
      // a short list has no filter
      openTab("accounts");
      expect(screen.queryByRole("searchbox", { name: /Filter/ })).toBeNull();
    });

    it("an unsaved draft survives switching between the resource tabs", async () => {
      renderPage("owner", "pools");
      const pools = await screen.findByTestId("my-runner-resources-pools");
      fireEvent.click(within(within(pools).getByText("codex-plus").closest("li") as HTMLElement).getByRole("button", { name: "Remove" }));
      openTab("accounts");
      expect(screen.getByTestId("my-runner-resources-dirty")).toBeInTheDocument();
      openTab("pools");
      expect(screen.getByTestId("my-runner-resources-pools")).not.toHaveTextContent("codex-plus");
      fireEvent.click(screen.getByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(world.puts).toHaveLength(1));
      expect(Object.keys((world.puts[0] as { config: typeof CATALOG }).config.pools)).toEqual(["claude-max"]);
    });

    it("a saved draft the server refuses in another tab's entry opens that tab at the entry", async () => {
      world.refuse = new ApiRequestError("invalid runtime catalog", "validation_failed", 400, { "config.pools.claude-max.max_concurrency": "invalid" });
      renderPage("owner", "accounts");
      const accounts = await screen.findByTestId("my-runner-resources-accounts");
      fireEvent.click(within(within(accounts).getByText("claude/alex").closest("li") as HTMLElement).getByRole("button", { name: "Remove" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(screen.getByRole("tab", { name: "Pools" })).toHaveAttribute("aria-selected", "true"));
      const editor = await within(screen.getByTestId("my-runner-resources-pools")).findByTestId("my-runner-entry-editor");
      expect(within(editor).getByLabelText(/Concurrent runs, at most/)).toHaveAttribute("aria-invalid", "true");
    });

    it("a refused agent's settings are named, to be fixed under Agents", async () => {
      world.refuse = new ApiRequestError("invalid runtime catalog", "validation_failed", 400, { "config.bindings.vera.permitted_profiles": "missing_reference" });
      renderPage("owner", "profiles");
      const profiles = await screen.findByTestId("my-runner-resources-profiles");
      fireEvent.click(within(within(profiles).getByText("claude-opus").closest("li") as HTMLElement).getByRole("button", { name: "Remove" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      await waitFor(() => expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("the settings of vera need fixing")));
    });

    it("an entry the server refused is reopened even when the filter hides it", async () => {
      manyPools();
      world.refuse = new ApiRequestError("invalid runtime catalog", "validation_failed", 400, { "config.pools.claude-max.max_concurrency": "invalid" });
      renderPage("owner", "pools");
      fireEvent.change(await screen.findByRole("searchbox", { name: /Filter/ }), { target: { value: "pool-1" } });
      const list = screen.getByTestId("my-runner-resources-pools");
      fireEvent.click(within(within(list).getByText("pool-10").closest("li") as HTMLElement).getByRole("button", { name: "Remove" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
      const editor = await within(screen.getByTestId("my-runner-resources-pools")).findByTestId("my-runner-entry-editor");
      expect(within(editor).getByLabelText(/Concurrent runs, at most/)).toHaveAttribute("aria-invalid", "true");
    });
    describe("only this workspace's bindings", () => {
      const binding = (agent: string, ws: string) => ({ ...CATALOG.bindings.vera, binding: { agent_id: agent, workspace_id: ws, grant_id: `g-${agent}` } });
      beforeEach(() => {
        (world.catalog as { config: unknown }).config = {
          ...CATALOG,
          bindings: {
            own1: binding("a-own1", "0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02"), own2: binding("a-own2", "0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02"), own3: binding("a-own3", "0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02"),
            kepler: binding("a-kepler", "ws-vc"), scully: binding("a-scully", "ws-subrose"),
          },
        };
      });

      // own1..own3 name agents this list does not show, so they are the unlisted entries of the Agents tab
      it("lists 3 own settings out of 3 own + 2 foreign, and says 2 others are hidden", async () => {
        renderPage("owner", "agents");
        const unlisted = await screen.findByTestId("my-runner-agents-unlisted");
        await waitFor(() => expect(within(unlisted).getAllByRole("listitem")).toHaveLength(3));
        for (const n of ["own1", "own2", "own3"]) expect(unlisted).toHaveTextContent(n);
        const tab = screen.getByTestId("my-runner-page");
        expect(tab).not.toHaveTextContent("kepler");
        expect(tab).not.toHaveTextContent("scully");
        expect(screen.getByTestId("my-runner-agents-foreign")).toHaveTextContent("2 bindings that are not this workspace's are hidden here");
      });

      it("settings that name no workspace are not this workspace's either", async () => {
        const cfg = (world.catalog as { config: { bindings: Record<string, unknown> } }).config;
        cfg.bindings = { ...cfg.bindings, nameless: { ...CATALOG.bindings.vera, binding: { agent_id: "a-nameless", grant_id: "g-n" } } };
        renderPage("owner", "agents");
        const unlisted = await screen.findByTestId("my-runner-agents-unlisted");
        await waitFor(() => expect(within(unlisted).getAllByRole("listitem")).toHaveLength(3));
        expect(unlisted).not.toHaveTextContent("nameless");
        expect(screen.getByTestId("my-runner-agents-foreign")).toHaveTextContent("3 bindings");
      });

      it("a save keeps the hidden foreign settings as they are", async () => {
        renderPage("owner", "agents");
        // the form checks ids are UUIDs: own2 gets real ones, the hidden entries stay as they are
        const cfg = (world.catalog as { config: { bindings: Record<string, unknown> } }).config;
        cfg.bindings = { ...cfg.bindings, own2: CATALOG.bindings.vera };
        fireEvent.click(within(await screen.findByTestId("my-runner-agents-unlisted")).getByRole("button", { name: "own2" }));
        const dialog = screen.getByTestId("my-runner-agent-dialog");
        fireEvent.change(within(dialog).getByLabelText(/Attempts, at most/), { target: { value: "7" } });
        fireEvent.click(within(dialog).getByRole("button", { name: "Save settings" }));
        await waitFor(() => expect(world.puts).toHaveLength(1));
        const sent = (world.puts[0] as { config: { bindings: Record<string, unknown> } }).config.bindings;
        expect(Object.keys(sent).sort()).toEqual(["kepler", "own1", "own2", "own3", "scully"]);
        expect(sent.kepler).toEqual(binding("a-kepler", "ws-vc"));
      });

      it("with no agents listed, the catalog's own entries, the hidden count and reasons still show", async () => {
        world.agents = [];
        world.catalog = { ...(world.catalog as object), excluded: [{ ref: "own1", reason: "runner_off" }] };
        renderPage("owner", "agents");
        const unlisted = await screen.findByTestId("my-runner-agents-unlisted");
        expect(within(unlisted).getAllByRole("listitem")).toHaveLength(3);
        expect(screen.getByTestId("my-runner-agents-empty")).toBeInTheDocument();
        expect(screen.getByTestId("my-runner-agents-foreign")).toHaveTextContent("2 bindings");
        expect(screen.getByTestId("my-runner-binding-excluded-own1")).toHaveTextContent("Not run: Runner is off in its workspace");
      });

      it("without the Runner form the catalog's entries, hidden count and reasons are still shown, read-only", async () => {
        world.manifest = undefined;
        world.catalog = { ...(world.catalog as object), excluded: [{ ref: "own1", reason: "runner_off" }] };
        renderPage("owner", "agents");
        const unlisted = await screen.findByTestId("my-runner-agents-unlisted");
        expect(within(unlisted).getAllByRole("listitem")).toHaveLength(3);
        expect(within(unlisted).queryByRole("button")).toBeNull();
        expect(screen.getByTestId("my-runner-agents-foreign")).toHaveTextContent("2 bindings");
        expect(screen.getByTestId("my-runner-binding-excluded-own1")).toHaveTextContent("Not run: Runner is off in its workspace");
      });

      it("no foreign settings, no warning", async () => {
        (world.catalog as { config: unknown }).config = CATALOG;
        renderPage("owner", "agents");
        await screen.findByTestId("my-runner-agents-unlisted");
        expect(screen.queryByTestId("my-runner-agents-foreign")).toBeNull();
      });

      it("shows why the server does not run an agent's settings, when it says so", async () => {
        const cfg = (world.catalog as { config: { bindings: Record<string, unknown> } }).config;
        cfg.bindings = { ...cfg.bindings, robin: binding("a1", "0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02") };
        world.catalog = { ...(world.catalog as object), excluded: [{ ref: "own1", reason: "runner_off" }, { ref: "own2", reason: "something_new" }, { ref: "robin", reason: "program_not_connected" }] };
        renderPage("owner", "agents");
        expect(await screen.findByTestId("my-runner-binding-excluded-own1")).toHaveTextContent("Not run: Runner is off in its workspace");
        expect(screen.getByTestId("my-runner-binding-excluded-own2")).toHaveTextContent("Not run: something_new");
        expect(screen.queryByTestId("my-runner-binding-excluded-own3")).toBeNull();
        expect(screen.getByTestId("my-runner-agent-excluded-a1")).toHaveTextContent("Not run: the runner program is not connected");
      });

      it("a foreign binding of the same agent adds no profiles to the agents table", async () => {
        (world.catalog as { config: unknown }).config = {
          ...CATALOG,
          profiles: { ...CATALOG.profiles, alien: { ...CATALOG.profiles["claude-opus"], model: "haiku" } },
          bindings: { mine: binding("a1", "0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02"), theirs: { ...binding("a1", "ws-vc"), permitted_profiles: ["alien"] } },
        };
        renderPage("owner", "agents");
        await waitFor(() => expect(screen.getByTestId("my-runner-agent-a1")).toHaveTextContent("Opus"));
        expect(screen.getByTestId("my-runner-agent-a1")).not.toHaveTextContent("Haiku");
      });
    });

    it("a catalog controller that leads nobody is marked off or silent, never live", async () => {
      world.runners = [];
      const ctl = (enabled: boolean) => ({ host: "h", version: "1", reporter_grant_id: "g", capabilities: [], heartbeat_max_age_seconds: 120, enabled });
      const config = { ...CATALOG, controllers: { "ctl-off": ctl(false), "ctl-silent": ctl(true), "ctl-idle": ctl(true) } };
      world.catalog = { ...(world.catalog as object), config, controllers: [{ controller_ref: "ctl-idle", report: { revision: 37, digest: "c".repeat(64), status: "applied" }, received_at: new Date().toISOString(), current: true }] };
      renderPage("owner");
      expect(await screen.findByTestId("my-runner-runner-state-ctl-off")).toHaveTextContent("off in the catalog");
      expect(screen.getByTestId("my-runner-runner-state-ctl-silent")).toHaveTextContent("no report");
      expect(screen.getByTestId("my-runner-runner-state-ctl-idle")).toHaveTextContent("leads no agents");
      expect(screen.getByTestId("my-runner-runners")).not.toHaveTextContent(/\blive\b/);
    });

    it("a live lease does not make an off or silent catalog controller look live", async () => {
      world.runners = [
        { ...CONTROLLER, runner_id: "ctl-off", name: "ctl-off", agents: [] },
        { ...CONTROLLER, runner_id: "ctl-silent", name: "ctl-silent", agents: [] },
        { ...CONTROLLER, runner_id: "ctl-ok", name: "ctl-ok", agents: [{ agent_id: "a1", name: "Robin" }] },
        { ...CONTROLLER, runner_id: "ctl-idle", name: "ctl-idle", agents: [] },
      ];
      const ctl = (enabled: boolean) => ({ host: "h", version: "1", reporter_grant_id: "g", capabilities: [], heartbeat_max_age_seconds: 120, enabled });
      const config = { ...CATALOG, controllers: { "ctl-off": ctl(false), "ctl-silent": ctl(true), "ctl-ok": ctl(true), "ctl-idle": ctl(true) } };
      const report = (ref: string) => ({ controller_ref: ref, report: { revision: 37, digest: "c".repeat(64), status: "applied" }, received_at: new Date().toISOString(), current: true });
      world.catalog = { ...(world.catalog as object), config, controllers: [report("ctl-off"), report("ctl-ok"), report("ctl-idle")] };
      renderPage("owner");
      expect(await screen.findByTestId("my-runner-runner-state-ctl-off")).toHaveTextContent("off in the catalog");
      expect(screen.getByTestId("my-runner-runner-state-ctl-silent")).toHaveTextContent("no report");
      expect(screen.getByTestId("my-runner-runner-state-ctl-ok")).toHaveTextContent(/\blive\b/);
      expect(screen.getByTestId("my-runner-runner-state-ctl-idle")).toHaveTextContent("leads no agents");
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
      const runners = screen.getAllByTestId(/^my-runner-runner-(?!state-)/);
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
      expect(screen.getAllByTestId(/^my-runner-runner-(?!state-)/)).toHaveLength(1);
    });

    it("a controller behind the catalog or silent says so", async () => {
      world.catalog = { ...(world.catalog as object), controllers: [{ controller_ref: "controller-mini", report: { revision: 35, digest: "b".repeat(64), status: "applied" }, received_at: new Date().toISOString(), current: false }] };
      renderPage("owner");
      const row = await screen.findByTestId("my-runner-controller-controller-mini");
      expect(row).toHaveTextContent("behind");
      expect(row).toHaveTextContent("applied r35 · desired r37");
    });

    it("the agents table shows each agent's profiles from the catalog bindings, by readable name", async () => {
      world.agents = [AGENT, { ...AGENT, agent_id: VERA, name: "Vera" }];
      renderPage("owner", "agents");
      await waitFor(() => expect(screen.getByTestId(`my-runner-agent-${VERA}`)).toHaveTextContent(OPUS));
      expect(screen.getByTestId("my-runner-agent-a1")).not.toHaveTextContent("Opus");
    });

    it("the profiles tab explains what a profile is and names each one readably, key beside it", async () => {
      renderPage("owner", "profiles");
      expect(await screen.findByTestId("my-runner-profiles-hint")).toHaveTextContent("A profile is a launch recipe");
      const row = within(screen.getByTestId("my-runner-resources-profiles")).getByText(OPUS).closest("li") as HTMLElement;
      expect(row).toHaveTextContent("claude-opus");
    });

    it("an agent turned off in the catalog says so in its row", async () => {
      (world.catalog as { config: unknown }).config = { ...CATALOG, bindings: { vera: { ...CATALOG.bindings.vera, enabled: false } } };
      world.agents = [AGENT, { ...AGENT, agent_id: VERA, name: "Vera" }];
      renderPage("owner", "agents");
      await waitFor(() => expect(screen.getByTestId(`my-runner-agent-${VERA}`)).toHaveTextContent("off"));
      expect(within(screen.getByTestId("my-runner-agent-a1")).queryByText("off")).toBeNull();
    });

    it("an agent with no runner settings yet gets a new entry for itself in this workspace", async () => {
      renderPage("owner", "agents");
      const dialog = await openAgent("a1");
      const editor = within(dialog).getByTestId("my-runner-entry-editor");
      expect(within(editor).getByRole("textbox", { name: "Name" })).toHaveValue("robin");
      expect(within(editor).getByLabelText(/^Agent/)).toHaveValue("a1");
      // the workspace follows the access and is shown by name: this workspace's id resolves to "W"
      expect(within(editor).getByTestId("schema-form-workspace")).toHaveTextContent("W");
    });

    it("settings for an agent the list does not show stay reachable", async () => {
      renderPage("owner", "agents");
      const unlisted = await screen.findByTestId("my-runner-agents-unlisted");
      fireEvent.click(within(unlisted).getByRole("button", { name: "vera" }));
      const dialog = screen.getByTestId("my-runner-agent-dialog");
      expect(dialog).toHaveTextContent("Agent vera");
      expect(within(dialog).queryByLabelText(/Parallel tasks/)).toBeNull();
      expect(within(dialog).getByTestId("my-runner-entry-editor")).toBeInTheDocument();
    });

    it("a refused agent save keeps what was sent and puts the reason at its field", async () => {
      world.agents = [AGENT, { ...AGENT, agent_id: VERA, name: "Vera" }];
      world.refuse = new ApiRequestError("invalid runtime catalog", "validation_failed", 400, { "config.bindings.vera.policy.max_attempts": "invalid" });
      renderPage("owner", "agents");
      const dialog = await openAgent(VERA);
      fireEvent.change(within(dialog).getByLabelText(/Attempts, at most/), { target: { value: "7" } });
      fireEvent.click(within(dialog).getByRole("button", { name: "Save settings" }));
      await waitFor(() => expect(within(screen.getByTestId("my-runner-agent-dialog")).getByLabelText(/Attempts, at most/)).toHaveAttribute("aria-invalid", "true"));
      expect(within(screen.getByTestId("my-runner-agent-dialog")).getByLabelText(/Attempts, at most/)).toHaveValue(7);
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
      expect(mockedApi).not.toHaveBeenCalledWith("/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/runtime/restore", expect.anything());
      fireEvent.click(within(ask).getByRole("button", { name: "Restore" }));
      await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/runtime/restore", { method: "POST", body: { if_revision: 39, revision: 37 } }));
    });

    it("a member, refused the runtime route, sees no controller block", async () => {
      world.catalog = new ApiRequestError("forbidden", "forbidden", 403);
      renderPage("member");
      await screen.findByTestId("my-runner-runner-r1");
      await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/0b3c5a52-34d6-4a8e-9a8f-1f6b2e6c1d02/runtime"));
      await act(async () => {});
      expect(screen.queryByTestId("my-runner-controllers")).toBeNull();
      expect(screen.queryByText("Controllers")).toBeNull();
    });
  });
});
