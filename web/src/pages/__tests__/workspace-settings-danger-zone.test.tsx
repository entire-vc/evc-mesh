import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

import { api } from "@/lib/api";
import { WorkspaceSettingsPage } from "@/pages/workspace-settings";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import { useAuthStore } from "@/stores/auth";
import type { User, Workspace, WorkspaceRole } from "@/types";

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

const USER: User = {
  id: "u1",
  email: "owner@example.com",
  name: "Owner",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function renderPage() {
  return render(
    <MemoryRouter>
      <WorkspaceSettingsPage />
    </MemoryRouter>,
  );
}

// This page fires several fetches unconditionally on mount (members, team
// directory, assignment rules, violations, workflow templates, invites,
// myRole). Every one of them already catches its own errors internally
// (see stores/rules.ts), so the only route that has to answer with
// something specific is members/me — anything else resolving to {} is
// harmless. Answering members/me from the CURRENT store state (rather than a
// fixed value) matters: fetchMyRole runs after this test has already preset
// myRole via setState, and a naive mock would clobber it back to null the
// moment the real fetch resolves — the same trap documented in
// integrations.test.tsx for the same page shape.
function mockApiByRoute(role: WorkspaceRole | null, onDelete?: () => void) {
  mockedApi.mockImplementation((url: string, opts?: { method?: string }) => {
    if (url === "/api/v1/workspaces/ws1/members/me") {
      return Promise.resolve({ role: useMemberStore.getState().myRole ?? role });
    }
    if (url === "/api/v1/workspaces/ws1" && opts?.method === "DELETE") {
      onDelete?.();
      return Promise.resolve(undefined);
    }
    return Promise.resolve({});
  });
}

beforeEach(() => {
  mockedApi.mockReset();
  mockedNavigate.mockReset();
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE, workspaces: [WORKSPACE] });
  useAuthStore.setState({ user: USER, isAuthenticated: true, isLoading: false });
});
afterEach(() => vi.clearAllMocks());

describe("WorkspaceSettingsPage — Danger Zone role gating (AC1)", () => {
  it("hides the Danger Zone entirely for a member", async () => {
    useMemberStore.setState({ myRole: "member" });
    mockApiByRoute("member");

    renderPage();

    await waitFor(() => expect(mockedApi).toHaveBeenCalled());
    expect(screen.queryByText("Danger Zone")).not.toBeInTheDocument();
  });

  it("hides the Danger Zone entirely for a viewer", async () => {
    useMemberStore.setState({ myRole: "viewer" });
    mockApiByRoute("viewer");

    renderPage();

    await waitFor(() => expect(mockedApi).toHaveBeenCalled());
    expect(screen.queryByText("Danger Zone")).not.toBeInTheDocument();
  });

  it("shows the Danger Zone for an owner", async () => {
    useMemberStore.setState({ myRole: "owner" });
    mockApiByRoute("owner");

    renderPage();

    await waitFor(() => expect(screen.getByText("Danger Zone")).toBeInTheDocument());
  });

  it("shows the Danger Zone for an admin", async () => {
    useMemberStore.setState({ myRole: "admin" });
    mockApiByRoute("admin");

    renderPage();

    await waitFor(() => expect(screen.getByText("Danger Zone")).toBeInTheDocument());
  });
});

describe("WorkspaceSettingsPage — Delete Workspace flow", () => {
  it("requires typing the exact workspace name before Delete Workspace is enabled", async () => {
    useMemberStore.setState({ myRole: "owner" });
    mockApiByRoute("owner");

    renderPage();
    await waitFor(() => expect(screen.getByText("Danger Zone")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    const confirmButton = await screen.findByRole("button", { name: "Delete Workspace" });
    expect(confirmButton).toBeDisabled();

    fireEvent.change(screen.getByPlaceholderText("Acme"), { target: { value: "Acme" } });
    expect(confirmButton).toBeEnabled();
  });

  it("deletes the workspace and navigates to \"/\" so AppLayout picks the next destination", async () => {
    useMemberStore.setState({ myRole: "owner" });
    let deleteCalled = false;
    mockApiByRoute("owner", () => { deleteCalled = true; });

    renderPage();
    await waitFor(() => expect(screen.getByText("Danger Zone")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    fireEvent.change(await screen.findByPlaceholderText("Acme"), { target: { value: "Acme" } });
    fireEvent.click(screen.getByRole("button", { name: "Delete Workspace" }));

    await waitFor(() => expect(deleteCalled).toBe(true));
    await waitFor(() => expect(mockedNavigate).toHaveBeenCalledWith("/", { replace: true }));
    // The store's own deleteWorkspace clears currentWorkspace when it's the
    // one removed — this is what lets AppLayout's existing "no workspace
    // selected" logic take over instead of this page building its own
    // redirect-target logic.
    expect(useWorkspaceStore.getState().currentWorkspace).toBeNull();
  });

  it("shows an error and keeps the dialog open when the delete request fails", async () => {
    useMemberStore.setState({ myRole: "owner" });
    mockedApi.mockImplementation((url: string, opts?: { method?: string }) => {
      if (url === "/api/v1/workspaces/ws1/members/me") {
        return Promise.resolve({ role: "owner" });
      }
      if (url === "/api/v1/workspaces/ws1" && opts?.method === "DELETE") {
        return Promise.reject(new Error("workspace access denied"));
      }
      return Promise.resolve({});
    });

    renderPage();
    await waitFor(() => expect(screen.getByText("Danger Zone")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    fireEvent.change(await screen.findByPlaceholderText("Acme"), { target: { value: "Acme" } });
    fireEvent.click(screen.getByRole("button", { name: "Delete Workspace" }));

    await waitFor(() =>
      expect(screen.getByText("workspace access denied")).toBeInTheDocument(),
    );
    expect(mockedNavigate).not.toHaveBeenCalled();
    // Dialog stayed open — the confirm button is still on screen.
    expect(screen.getByRole("button", { name: "Delete Workspace" })).toBeInTheDocument();
  });
});
