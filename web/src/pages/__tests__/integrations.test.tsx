import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));

import { api } from "@/lib/api";
import { IntegrationsPage } from "@/pages/integrations";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import type { IntegrationConfig, Workspace } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const WORKSPACE: Workspace = {
  id: "ws1",
  name: "Acme",
  slug: "acme",
  owner_id: "u1",
  settings: {},
  billing_plan_id: "free",
  billing_customer_id: "",
  icon_url: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function renderPage() {
  return render(
    <MemoryRouter>
      <IntegrationsPage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockedApi.mockReset();
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE, workspaces: [WORKSPACE] });
  useMemberStore.setState({ myRole: null });
});
afterEach(() => vi.clearAllMocks());

// Route api() by URL/method, same style as notification-settings.test.tsx.
//
// fetchMyRole (called by IntegrationsPage on mount, since myRole is not
// otherwise guaranteed populated when this page is the first one visited)
// hits GET .../members/me and, on any response with no `role` field —
// including the catch-all {} this mock would otherwise return — overwrites
// myRole back to null. So this route has to answer with whatever role the
// test preset via useMemberStore.setState, or that preset gets clobbered
// out from under the assertion.
function mockApiByRoute(routes: {
  integrations?: IntegrationConfig[];
  onConfigure?: (body: unknown) => void;
  configureResponse?: object;
}) {
  let created: IntegrationConfig | null = null;
  mockedApi.mockImplementation(
    (url: string, opts?: { method?: string; body?: unknown }) => {
      if (url === "/api/v1/workspaces/ws1/members/me") {
        return Promise.resolve({ role: useMemberStore.getState().myRole });
      }
      if (url === "/api/v1/workspaces/ws1/integrations" && opts?.method === "POST") {
        routes.onConfigure?.(opts.body);
        created = {
          id: "int1",
          workspace_id: "ws1",
          provider: "telegram",
          is_active: true,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
          config: { bot_token_set: true, bot_username: "mesh_bot" },
          ...(routes.configureResponse ?? {}),
        } as IntegrationConfig;
        return Promise.resolve(created);
      }
      if (url === "/api/v1/workspaces/ws1/integrations") {
        return Promise.resolve({
          integrations: created ? [...(routes.integrations ?? []), created] : (routes.integrations ?? []),
        });
      }
      return Promise.resolve({});
    },
  );
}

describe("IntegrationsPage — Telegram role gating (AC1)", () => {
  it("hides the Telegram card entirely for a member", async () => {
    useMemberStore.setState({ myRole: "member" });
    mockApiByRoute({ integrations: [] });

    renderPage();

    await waitFor(() => expect(mockedApi).toHaveBeenCalled());
    expect(screen.queryByText("Telegram")).not.toBeInTheDocument();
  });

  it("hides the Telegram card entirely for a viewer", async () => {
    useMemberStore.setState({ myRole: "viewer" });
    mockApiByRoute({ integrations: [] });

    renderPage();

    await waitFor(() => expect(mockedApi).toHaveBeenCalled());
    expect(screen.queryByText("Telegram")).not.toBeInTheDocument();
  });

  it("shows the Telegram card for an owner", async () => {
    useMemberStore.setState({ myRole: "owner" });
    mockApiByRoute({ integrations: [] });

    renderPage();

    await waitFor(() => expect(screen.getByText("Telegram")).toBeInTheDocument());
  });

  it("shows the Telegram card for an admin", async () => {
    useMemberStore.setState({ myRole: "admin" });
    mockApiByRoute({ integrations: [] });

    renderPage();

    await waitFor(() => expect(screen.getByText("Telegram")).toBeInTheDocument());
  });
});

describe("IntegrationsPage — Telegram token flow", () => {
  it("submits the token and shows the connected bot username from the (masked) response", async () => {
    useMemberStore.setState({ myRole: "owner" });
    let posted: unknown;
    mockApiByRoute({ integrations: [], onConfigure: (body) => { posted = body; } });

    renderPage();
    await waitFor(() => expect(screen.getByText("Telegram")).toBeInTheDocument());

    const tokenInput = screen.getByPlaceholderText("123456789:AAExampleBotTokenHere");
    fireEvent.change(tokenInput, { target: { value: "123:real-token" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() =>
      expect(screen.getByText(/Connected as/)).toBeInTheDocument(),
    );
    expect(screen.getByText("@mesh_bot")).toBeInTheDocument();
    expect(posted).toMatchObject({
      provider: "telegram",
      config: { bot_token: "123:real-token" },
      is_active: true,
    });
    // The raw token must never appear back in the rendered page — only the
    // masked response (bot_token_set/bot_username) reaches the client.
    expect(screen.queryByText("123:real-token")).not.toBeInTheDocument();
  });

  it("shows a Change token affordance instead of a form once connected", async () => {
    useMemberStore.setState({ myRole: "owner" });
    mockApiByRoute({
      integrations: [
        {
          id: "int1",
          workspace_id: "ws1",
          provider: "telegram",
          is_active: true,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
          config: { bot_token_set: true, bot_username: "mesh_bot" },
        },
      ],
    });

    renderPage();

    await waitFor(() => expect(screen.getByText(/Connected as/)).toBeInTheDocument());
    expect(screen.queryByPlaceholderText("123456789:AAExampleBotTokenHere")).not.toBeInTheDocument();
    expect(screen.getByText("Change token")).toBeInTheDocument();
  });
});

// mcp is a reference-only connection card (#4a3195a5) — no handler or
// service ever reads a stored row back for behavior, so is_active was a
// switch that switched nothing. It has no Enable/Disable and no Remove, and
// — unlike before this fix — its connection snippet is NOT gated behind
// is_active, since after this change no mcp row can ever exist to be active.
describe("IntegrationsPage — mcp is reference-only (#4a3195a5)", () => {
  it("renders an Enable toggle for every configurable provider except mcp (and telegram, gated separately)", async () => {
    useMemberStore.setState({ myRole: "owner" });
    mockApiByRoute({ integrations: [] });

    renderPage();

    await waitFor(() => expect(screen.getByText("MCP Server")).toBeInTheDocument());
    // With no stored config for anyone, every provider that DOES show a
    // toggle renders it as "Enable" (isActive=false). Of the 5 providers on
    // this page (github, gitlab, spark, team_relay, mcp, telegram), telegram is excluded
    // pre-existing (no bot token set) and mcp is excluded by #4a3195a5 —
    // leaving exactly 3 (team_relay is shown read-only, #25aec021: the configure
    // endpoint rejects it). This count has stepped down twice: 5 -> 4 when mcp
    // stopped rendering a switch that switched nothing, 4 -> 3 when slack
    // left the page (#84599ead; its backend was removed in #c9f9834f). A change here must name which provider moved and why.
    expect(screen.getAllByRole("button", { name: "Enable" })).toHaveLength(3);
  });

  it("always renders the .mcp.json connection snippet, with no row and no is_active required", async () => {
    useMemberStore.setState({ myRole: "owner" });
    // Deliberately empty — proves the snippet does not depend on a stored,
    // active mcp row (the pre-fix code gated it on `isActive`, which after
    // #4a3195a5's cleanup migration can never again be true for mcp).
    mockApiByRoute({ integrations: [] });

    renderPage();

    await waitFor(() =>
      expect(screen.getByText("Claude Code / Cline — .mcp.json")).toBeInTheDocument(),
    );
  });
});

describe("IntegrationsPage — effective status (#a4d108eb)", () => {
  const eff = (over: Record<string, unknown>) => ({
    provider: "gitlab",
    effective_enabled: true,
    source: "env",
    has_workspace_row: false,
    env_available: true,
    health: "ok",
    ...over,
  });

  function mockWithEffective(effective: unknown[], onWrite?: (url: string, opts: unknown) => void) {
    mockedApi.mockImplementation((url: string, opts?: { method?: string }) => {
      if (url === "/api/v1/workspaces/ws1/members/me") {
        return Promise.resolve({ role: "admin" });
      }
      if (url === "/api/v1/workspaces/ws1/integrations" && !opts?.method) {
        return Promise.resolve({ integrations: [], effective });
      }
      if (opts?.method) onWrite?.(url, opts);
      return Promise.resolve({});
    });
  }

  it("shows a no-row GitLab that runs off env as Enabled with an instance note", async () => {
    useMemberStore.setState({ myRole: "admin" });
    mockWithEffective([eff({})]);
    renderPage();
    expect(await screen.findByTestId("env-note-gitlab")).toHaveTextContent(
      "Active (configured at the instance level)",
    );
    expect(screen.getByTestId("source-gitlab")).toHaveTextContent("Instance");
  });

  it("shows Team Relay and Telegram with the effective source badge", async () => {
    useMemberStore.setState({ myRole: "admin" });
    mockWithEffective([
      eff({ provider: "team_relay" }),
      eff({ provider: "telegram", source: "workspace", has_workspace_row: true }),
    ]);
    renderPage();
    expect(await screen.findByText("Team Relay")).toBeInTheDocument();
    expect(screen.getByTestId("source-team_relay")).toHaveTextContent("Instance");
    expect(screen.getByTestId("env-note-team_relay")).toBeInTheDocument();
    expect(screen.getByTestId("source-telegram")).toHaveTextContent("Workspace");
  });

  it("asks for confirmation before turning off an env-backed integration, and writes nothing until confirmed", async () => {
    useMemberStore.setState({ myRole: "admin" });
    const writes: Array<[string, unknown]> = [];
    mockWithEffective([eff({})], (url, opts) => writes.push([url, opts]));
    renderPage();
    const note = await screen.findByTestId("env-note-gitlab");
    const card = note.closest("[class*='rounded']")!.parentElement!;
    fireEvent.click(card.querySelector("button.bg-teal-600") as HTMLElement);
    expect(await screen.findByRole("alertdialog")).toHaveTextContent(
      "configured at the instance level",
    );
    expect(writes).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect((writes[0]![1] as { body: Record<string, unknown> }).body).toMatchObject({
      is_active: false,
      confirm_env_override: true,
    });
  });

  it("does not create an empty placeholder when Enable is clicked with no row and no env", async () => {
    useMemberStore.setState({ myRole: "admin" });
    const writes: unknown[] = [];
    mockWithEffective(
      [eff({ provider: "spark", effective_enabled: false, source: "none", env_available: false, health: "not_configured" })],
      (_u, o) => writes.push(o),
    );
    renderPage();
    await screen.findByText("Spark Agent Catalog");
    // Provider order: github, gitlab, spark — Spark is the third Enable button.
    const enableButtons = await screen.findAllByRole("button", { name: /Enable/ });
    fireEvent.click(enableButtons[2]!);
    expect(writes).toHaveLength(0);
  });

  it("refreshes effective status after saving a connection (badge flips without reload)", async () => {
    useMemberStore.setState({ myRole: "admin" });
    let saved = false;
    mockedApi.mockImplementation((url: string, opts?: { method?: string }) => {
      if (url === "/api/v1/workspaces/ws1/members/me") return Promise.resolve({ role: "admin" });
      if (url === "/api/v1/workspaces/ws1/integrations" && opts?.method === "POST") {
        saved = true;
        return Promise.resolve({
          id: "int9", workspace_id: "ws1", provider: "gitlab", is_active: true,
          created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
          config: { token_set: true },
        });
      }
      if (url === "/api/v1/workspaces/ws1/integrations") {
        return Promise.resolve({
          integrations: [],
          effective: [
            saved
              ? eff({ source: "workspace", has_workspace_row: true, env_available: false })
              : eff({ effective_enabled: false, source: "none", env_available: false, health: "not_configured" }),
          ],
        });
      }
      return Promise.resolve({});
    });
    renderPage();
    expect(screen.queryByTestId("source-gitlab")).not.toBeInTheDocument();
    fireEvent.change(await screen.findByPlaceholderText("Access token"), { target: { value: "glpat-x" } });
    const saveButtons = screen.getAllByRole("button", { name: "Save" });
    fireEvent.click(saveButtons[saveButtons.length - 1]!);
    expect(await screen.findByTestId("source-gitlab")).toHaveTextContent("Workspace");
  });
});
