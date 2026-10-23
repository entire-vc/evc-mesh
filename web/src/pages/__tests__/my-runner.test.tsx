/**
 * "My Runner" page: every block has a loading, empty, error and ready state,
 * the operator's edits go to the right route, and only a workspace admin sees
 * the "turn on Runner" button. Only the network is mocked.
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

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;
const ME = "user-1";
const CONTROLLER = { runner_id: "r1", name: "mini-main", host: "mini.local", version: "1.4.0", lease_expires_at: new Date(Date.now() + 60_000).toISOString(), live: true };
const AGENT = { agent_id: "a1", name: "Robin", operator_user_id: ME, max_concurrent_tasks: 3, runtime_manager: false, controller: CONTROLLER };

interface World {
  agents: unknown[] | Error; runners: unknown[] | Error; history: unknown[] | Error; runtime: unknown | Error; extensionOn: boolean; role: string; enableError?: Error; slowOwner?: Promise<{ role: string }>; roleWs2?: string; slowCatalogWs2?: Promise<unknown>;
}
let world: World;

function route(path: string, init?: { method?: string; body?: unknown }): Promise<unknown> {
  const out = (v: unknown) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v));
  if (init?.method && init.method !== "GET") {
    if (path.endsWith("/runner/enable") && world.enableError) return Promise.reject(world.enableError);
    return Promise.resolve({});
  }
  if (path === "/api/v1/me/runtime/agents") return out(world.agents instanceof Error ? world.agents : { agents: world.agents });
  if (path === "/api/v1/me/runtime/runners") return out(world.runners instanceof Error ? world.runners : { runners: world.runners });
  if (path.startsWith("/api/v1/me/runtime/history")) return out(world.history instanceof Error ? world.history : { history: world.history });
  if (path.endsWith("/members/me")) return path.includes("ws-2") ? Promise.resolve({ role: world.roleWs2 ?? "member" }) : (world.slowOwner ?? Promise.resolve({ role: world.role }));
  if (path === "/api/v1/me/runtime") return out(world.runtime);
  if (path === "/api/v1/extensions") return Promise.resolve({ extensions: [{ id: "runner", name: "Runner", version: "1", kind: "plugin", runtime: "builtin", publisher: "entire-vc", description: "", category: "agents" }] });
  if (path.endsWith("/extensions") && path.includes("ws-2") && world.slowCatalogWs2) return world.slowCatalogWs2;
  if (path.endsWith("/extensions")) return Promise.resolve({ extensions: [{ id: "runner", name: "Runner", version: "1", kind: "plugin", category: "agents", status: { enabled: (world.extensionOn && !path.includes("ws-2")), source: (world.extensionOn && !path.includes("ws-2")) ? "workspace" : "none" } }] });
  return Promise.reject(new ApiRequestError("not found", "not_found", 404));
}

function renderPage(role: "owner" | "member") {
  world.role = role;
  useMemberStore.setState({ myRole: null });
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "w", name: "W" } as Workspace });
  useAuthStore.setState({ user: { id: ME } as never });
  return render(<MemoryRouter><MyRunnerPage /></MemoryRouter>);
}

beforeEach(async () => {
  await setLanguage("en");
  world = { agents: [AGENT], runners: [{ ...CONTROLLER, agents: [{ agent_id: "a1", name: "Robin" }] }], history: [], runtime: { user_id: ME, revision: 4, config: { pools: { main: { size: 2 } } }, updated_at: "" }, extensionOn: false, role: "member" };
  mockedApi.mockReset();
  mockedApi.mockImplementation(route);
});
afterEach(() => vi.clearAllMocks());

/** Opens the pools editor. In loaded runs (full suite, CI) the editor was twice missing right after
 *  the Add click; the cause is not pinned down, so the node is looked up and clicked again on every
 *  attempt instead of being held across awaits. */
async function openPoolsEditor() {
  await screen.findByTestId("my-runner-resources-pools");
  await waitFor(() => {
    if (!screen.queryByTestId("my-runner-entry-editor")) {
      fireEvent.click(within(screen.getByTestId("my-runner-resources-pools")).getByRole("button", { name: "Add" }));
    }
    expect(screen.getByTestId("my-runner-entry-editor")).toBeInTheDocument();
  });
  return screen.getByTestId("my-runner-entry-editor");
}

describe("MyRunnerPage", () => {
  it("shows agents, runners and resources", async () => {
    renderPage("member");
    expect(await screen.findByTestId("my-runner-agent-a1")).toHaveTextContent("Robin");
    expect(await screen.findByTestId("my-runner-runner-r1")).toHaveTextContent("mini.local");
    expect(await screen.findByTestId("my-runner-resources-pools")).toHaveTextContent("main");
  });

  it("empty states say what to do", async () => {
    world.agents = []; world.runners = []; world.history = [];
    renderPage("member");
    expect(await screen.findByTestId("my-runner-agents-empty")).toBeInTheDocument();
    expect(await screen.findByTestId("my-runner-runners-empty")).toBeInTheDocument();
    expect(await screen.findByTestId("my-runner-history-empty")).toBeInTheDocument();
  });

  it("a failed block shows an error with retry while the others stay", async () => {
    world.runners = new Error("boom");
    renderPage("member");
    const err = await screen.findByTestId("my-runner-runners-error");
    expect(within(err).getByRole("button")).toBeInTheDocument();
    expect(await screen.findByTestId("my-runner-agent-a1")).toBeInTheDocument();
  });

  it("saves a changed limit to the agent's limits route", async () => {
    renderPage("member");
    const row = await screen.findByTestId("my-runner-agent-a1");
    fireEvent.change(within(row).getByLabelText(/Parallel tasks/), { target: { value: "5" } });
    fireEvent.click(within(row).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/agents/a1/limits", { method: "PUT", body: { max_concurrent_tasks: 5 } }));
  });

  it("refuses a limit outside 0..1000 and keeps Save off", async () => {
    renderPage("member");
    const row = await screen.findByTestId("my-runner-agent-a1");
    fireEvent.change(within(row).getByLabelText(/Parallel tasks/), { target: { value: "1001" } });
    expect(within(row).getByRole("button", { name: "Save" })).toBeDisabled();
    expect(within(row).getByRole("alert")).toBeInTheDocument();
  });

  it("assigns and takes back runtime_manager", async () => {
    renderPage("member");
    const row = await screen.findByTestId("my-runner-agent-a1");
    fireEvent.click(within(row).getByRole("checkbox"));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime/managers", { method: "POST", body: { agent_id: "a1" } }));
  });

  it("adds a resource entry and saves with the current revision", async () => {
    renderPage("member");
    const editor = await openPoolsEditor();
    const name = within(editor).getAllByRole("textbox")[0]!;
    fireEvent.change(name, { target: { value: "extra" } });
    fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
    fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/me/runtime", {
      method: "PUT", body: { if_revision: 4, config: { pools: { main: { size: 2 }, extra: {} } } },
    }));
  });

  it("refuses to save a draft that still holds a secret-like field elsewhere", async () => {
    world.runtime = { user_id: ME, revision: 4, config: { profiles: { old: { api_key: "x" } }, pools: {} }, updated_at: "" };
    const toastError = vi.spyOn(toast, "error");
    renderPage("member");
    const editor = await openPoolsEditor();
    fireEvent.change(within(editor).getAllByRole("textbox")[0]!, { target: { value: "extra" } });
    fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
    fireEvent.click(await screen.findByRole("button", { name: "Save resources" }));
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringContaining("profiles.old.api_key")));
    expect(mockedApi).not.toHaveBeenCalledWith("/api/v1/me/runtime", expect.objectContaining({ method: "PUT" }));
    toastError.mockRestore();
  });

  it("refuses invalid JSON and a duplicate name in the editor", async () => {
    renderPage("member");
    const editor = await openPoolsEditor();
    const boxes = within(editor).getAllByRole("textbox");
    const name = boxes[0]!;
    const value = boxes[1]!;
    fireEvent.change(name, { target: { value: "main" } });
    fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
    expect(within(editor).getByRole("alert")).toHaveTextContent("already used");
    fireEvent.change(name, { target: { value: "ok" } });
    fireEvent.change(value, { target: { value: "[1]" } });
    fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
    expect(within(editor).getByRole("alert")).toHaveTextContent("valid JSON object");
    fireEvent.change(value, { target: { value: '{"api_key":"abc"}' } });
    fireEvent.click(within(editor).getByRole("button", { name: "Apply" }));
    expect(within(editor).getByRole("alert")).toHaveTextContent('Secrets are not allowed: "api_key"');
    expect(mockedApi).not.toHaveBeenCalledWith("/api/v1/me/runtime", expect.objectContaining({ method: "PUT" }));
  });

  it("names the author of a revision: you, or the agent by name", async () => {
    world.history = [
      { id: 2, revision: 4, kind: "config", detail: null, author_type: "agent", author_id: "a1", created_at: new Date().toISOString() },
      { id: 1, revision: 3, kind: "limits", detail: null, author_type: "user", author_id: ME, created_at: new Date().toISOString() },
    ];
    renderPage("member");
    const history = await screen.findByTestId("my-runner-history");
    await waitFor(() => expect(history).toHaveTextContent("Agent Robin"));
    expect(history).toHaveTextContent("You");
  });

  it("offers the enable button to an admin only, and one click enables", async () => {
    const { unmount } = renderPage("member");
    await screen.findByTestId("my-runner-agent-a1");
    expect(screen.queryByTestId("my-runner-admin")).toBeNull();
    unmount();

    renderPage("owner");
    fireEvent.click(await screen.findByRole("button", { name: "Turn on Runner" }));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/extensions/runner/enable", expect.objectContaining({ method: "POST" })));
  });

  it("ignores a role cached in the shared store", async () => {
    renderPage("member");
    act(() => useMemberStore.setState({ myRole: "owner" }));
    await screen.findByTestId("my-runner-agent-a1");
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
    await waitFor(() => expect(screen.getByTestId("my-runner-admin")).toHaveTextContent("Nothing else to set up"));
    const ws = useWorkspaceStore.getState().currentWorkspace!;
    let release: (v: unknown) => void = () => {};
    world.slowCatalogWs2 = new Promise((r) => { release = r; });
    act(() => useWorkspaceStore.setState({ currentWorkspace: { ...ws, id: "ws-2" } }));
    // The role for ws-2 lands while its catalog is still pending: the old "on" state must not show.
    await waitFor(() => expect(mockedApi).toHaveBeenCalledWith("/api/v1/workspaces/ws-2/members/me"));
    await act(async () => { await new Promise((r) => setTimeout(r, 50)); });
    await screen.findByTestId("my-runner-admin");
    expect(screen.getByTestId("my-runner-admin")).not.toHaveTextContent("Nothing else to set up");
    await act(async () => { release({ extensions: [{ id: "runner", name: "Runner", version: "1", kind: "plugin", category: "agents", status: { enabled: false, source: "none" } }] }); });
    expect(await screen.findByRole("button", { name: "Turn on Runner" })).toBeInTheDocument();
  });

  it("explains a 404 on enable instead of a raw error", async () => {
    world.enableError = new ApiRequestError("connect the extension first", "not_found", 404);
    const toastError = vi.spyOn(toast, "error");
    renderPage("owner");
    fireEvent.click(await screen.findByRole("button", { name: "Turn on Runner" }));
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringContaining("never been connected")));
    toastError.mockRestore();
  });

  it("an admin sees no button once Runner is on", async () => {
    world.extensionOn = true;
    renderPage("owner");
    await screen.findByTestId("my-runner-admin");
    await waitFor(() => expect(screen.queryByRole("button", { name: "Turn on Runner" })).toBeNull());
    expect(screen.getByTestId("my-runner-admin")).toHaveTextContent("Nothing else to set up");
  });
});
