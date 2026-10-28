import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn(), getAccessToken: vi.fn(() => null) };
});

import { api, ApiRequestError } from "@/lib/api";
import { WorkspaceSettingsPage } from "@/pages/workspace-settings";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import { useAuthStore } from "@/stores/auth";
import type { User, Workspace, WorkspaceMemberWithUser, WorkspaceRole } from "@/types";

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

const ME: User = {
  id: "u1",
  email: "me@example.com",
  name: "Me",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function member(userId: string, role: WorkspaceRole, email: string, name: string): WorkspaceMemberWithUser {
  return {
    id: `m-${userId}`,
    workspace_id: "ws1",
    user_id: userId,
    role,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    user: { id: userId, email, name, username: name.toLowerCase(), avatar_url: "" },
  } as WorkspaceMemberWithUser;
}

const MEMBERS = [
  member("u1", "admin", "me@example.com", "Me"),
  member("u2", "owner", "boss@example.com", "Boss"),
  member("u3", "member", "fleet@example.com", "Fleet"),
];

let patches: Array<{ url: string; body: unknown }> = [];

function mockApi(role: WorkspaceRole, patchError?: Error) {
  mockedApi.mockImplementation((url: string, opts?: { method?: string; body?: unknown }) => {
    if (url === "/api/v1/workspaces/ws1/members/me") return Promise.resolve({ role });
    if (url === "/api/v1/workspaces/ws1/members") {
      return Promise.resolve({ members: MEMBERS, count: MEMBERS.length });
    }
    if (opts?.method === "PATCH" && url.startsWith("/api/v1/workspaces/ws1/members/")) {
      patches.push({ url, body: opts.body });
      if (patchError) return Promise.reject(patchError);
      return Promise.resolve(MEMBERS.find((m) => url.endsWith(m.user_id)));
    }
    return Promise.resolve({});
  });
}

async function openMembers() {
  render(
    <MemoryRouter>
      <WorkspaceSettingsPage />
    </MemoryRouter>,
  );
  fireEvent.click(await screen.findByRole("button", { name: /members/i }));
  await screen.findByText("Fleet");
}

beforeEach(() => {
  patches = [];
  mockedApi.mockReset();
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE, workspaces: [WORKSPACE] });
  useAuthStore.setState({ user: ME, isAuthenticated: true, isLoading: false });
  useMemberStore.setState({ workspaceMembers: [], myRole: null });
});
afterEach(() => vi.clearAllMocks());

describe("workspace settings — edit member account", () => {
  it("an admin gets the edit control on a member, not on an owner or on themselves", async () => {
    mockApi("admin");
    await openMembers();
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Edit member" })).toHaveLength(1));
  });

  it("an owner sets a new password and email; only the changed fields are sent", async () => {
    mockApi("owner");
    useMemberStore.setState({ myRole: "owner" });
    await openMembers();
    const edits = await screen.findAllByRole("button", { name: "Edit member" });
    // owner sees boss (another owner) and fleet
    expect(edits).toHaveLength(2);
    fireEvent.click(edits[1]!);

    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "New@Example.com" } });
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "NewPass456" } });
    fireEvent.change(screen.getByLabelText("Repeat password"), { target: { value: "NewPass456" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patches).toHaveLength(1));
    expect(patches[0]).toEqual({
      url: "/api/v1/workspaces/ws1/members/u3",
      body: { email: "new@example.com", password: "NewPass456" },
    });
    await waitFor(() => expect(screen.queryByLabelText("New password")).toBeNull());
  });

  it("mismatched passwords are caught before anything is sent", async () => {
    mockApi("owner");
    await openMembers();
    const edits = await screen.findAllByRole("button", { name: "Edit member" });
    fireEvent.click(edits[edits.length - 1]!);

    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "NewPass456" } });
    fireEvent.change(screen.getByLabelText("Repeat password"), { target: { value: "NewPass457" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("Passwords do not match")).toBeTruthy();
    expect(patches).toHaveLength(0);
  });

  it("a server refusal stays in the dialog", async () => {
    mockApi("owner", new ApiRequestError("this person also belongs to a workspace you do not administer", "FORBIDDEN", 403));
    await openMembers();
    const edits = await screen.findAllByRole("button", { name: "Edit member" });
    fireEvent.click(edits[edits.length - 1]!);

    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "NewPass456" } });
    fireEvent.change(screen.getByLabelText("Repeat password"), { target: { value: "NewPass456" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText(/workspace you don.t administer/)).toBeTruthy();
    expect(screen.getByLabelText("New password")).toBeTruthy();
  });
});
