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
  mockedApi.mockImplementation(
    (url: string, opts?: { method?: string; body?: unknown }) => {
      if (url === "/api/v1/workspaces/ws1/members/me") {
        return Promise.resolve({ role: useMemberStore.getState().myRole });
      }
      if (url === "/api/v1/workspaces/ws1/integrations" && opts?.method === "POST") {
        routes.onConfigure?.(opts.body);
        return Promise.resolve({
          id: "int1",
          workspace_id: "ws1",
          provider: "telegram",
          is_active: true,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
          config: { bot_token_set: true, bot_username: "mesh_bot" },
          ...(routes.configureResponse ?? {}),
        });
      }
      if (url === "/api/v1/workspaces/ws1/integrations") {
        return Promise.resolve({ integrations: routes.integrations ?? [] });
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
    // toggle renders it as "Enable" (isActive=false). Of the 6 providers on
    // this page (github, gitlab, slack, spark, mcp, telegram), telegram is
    // excluded pre-existing (no bot token set) and mcp is excluded by this
    // fix — leaving exactly 4. Before the fix this count was 5 (mcp had a
    // switch that switched nothing); after it, mcp contributes none.
    expect(screen.getAllByRole("button", { name: "Enable" })).toHaveLength(4);
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
