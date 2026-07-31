import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";
import { useMemberStore } from "@/stores/member";
import type { WorkspaceMemberWithUser } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const WS = "11111111-1111-1111-1111-111111111111";
const USER = "22222222-2222-2222-2222-222222222222";

function memberFixture(overrides: Partial<WorkspaceMemberWithUser> = {}): WorkspaceMemberWithUser {
  return {
    id: "33333333-3333-3333-3333-333333333333",
    workspace_id: WS,
    user_id: USER,
    role: "member",
    invited_by: null,
    created_at: "2026-07-31T00:00:00Z",
    updated_at: "2026-07-31T00:00:00Z",
    user: {
      id: USER,
      email: "guest@example.com",
      name: "Guest Person",
      username: "guest",
      avatar_url: "",
    },
    ...overrides,
  } as WorkspaceMemberWithUser;
}

beforeEach(() => {
  useMemberStore.setState({
    workspaceMembers: [],
    userSearchResults: [],
    isSearching: false,
  });
  mockedApi.mockReset();
});
afterEach(() => vi.clearAllMocks());

describe("searchUsers", () => {
  // The bug that made "add an existing user" look impossible: the endpoint
  // answers {users, count}, the store typed it as a bare array and stored the
  // envelope, so `userSearchResults.length` was undefined and the dropdown —
  // guarded on `length > 0` — never rendered once.
  it("unwraps the {users, count} envelope the endpoint actually returns", async () => {
    mockedApi.mockResolvedValueOnce({
      users: [
        { id: USER, email: "guest@example.com", name: "Guest Person", username: "guest", avatar_url: "", is_member: false },
      ],
      count: 1,
    });

    await useMemberStore.getState().searchUsers(WS, "guest@example.com");

    const results = useMemberStore.getState().userSearchResults;
    expect(Array.isArray(results)).toBe(true);
    expect(results).toHaveLength(1);
    const [hit] = results;
    expect(hit?.name).toBe("Guest Person");
    expect(hit?.is_member).toBe(false);
    expect(useMemberStore.getState().isSearching).toBe(false);
  });

  it("sends the query as ?q= against the workspace search route", async () => {
    mockedApi.mockResolvedValueOnce({ users: [], count: 0 });

    await useMemberStore.getState().searchUsers(WS, "someone@example.com");

    expect(mockedApi).toHaveBeenCalledWith(
      `/api/v1/workspaces/${WS}/users/search`,
      { params: { q: "someone@example.com" } },
    );
  });

  it("short-circuits a blank query without calling the API", async () => {
    await useMemberStore.getState().searchUsers(WS, "   ");

    expect(mockedApi).not.toHaveBeenCalled();
    expect(useMemberStore.getState().userSearchResults).toEqual([]);
  });

  it("leaves an empty array (never undefined) when the request fails", async () => {
    mockedApi.mockRejectedValueOnce(new Error("boom"));

    await useMemberStore.getState().searchUsers(WS, "guest@example.com");

    expect(useMemberStore.getState().userSearchResults).toEqual([]);
    expect(useMemberStore.getState().isSearching).toBe(false);
  });
});

describe("addWorkspaceMember", () => {
  it("omits the name when none is given, so an existing account is untouched", async () => {
    mockedApi.mockResolvedValueOnce(memberFixture());

    await useMemberStore.getState().addWorkspaceMember(WS, "guest@example.com", "member");

    expect(mockedApi).toHaveBeenCalledWith(
      `/api/v1/workspaces/${WS}/members`,
      { method: "POST", body: { email: "guest@example.com", role: "member" } },
    );
  });

  // Passing the name is what stops a provisioned account from being stored with
  // its own address in the name field.
  it("sends a trimmed name when provisioning an account", async () => {
    mockedApi.mockResolvedValueOnce(memberFixture());

    await useMemberStore
      .getState()
      .addWorkspaceMember(WS, "guest@example.com", "member", "StrongP4ss", "  Guest Person  ");

    expect(mockedApi).toHaveBeenCalledWith(
      `/api/v1/workspaces/${WS}/members`,
      {
        method: "POST",
        body: {
          email: "guest@example.com",
          role: "member",
          password: "StrongP4ss",
          name: "Guest Person",
        },
      },
    );
  });

  it("appends the new member to the list", async () => {
    mockedApi.mockResolvedValueOnce(memberFixture());

    await useMemberStore.getState().addWorkspaceMember(WS, "guest@example.com", "member");

    expect(useMemberStore.getState().workspaceMembers).toHaveLength(1);
  });
});

describe("updateWorkspaceMemberRole", () => {
  // The store splices the response into its list. While the API answered
  // {"status":"updated"}, that spliced a status envelope over the member row and
  // the name, address and role rendered blank until a reload.
  it("replaces the row with the member the API returns", async () => {
    useMemberStore.setState({ workspaceMembers: [memberFixture()] });
    mockedApi.mockResolvedValueOnce(memberFixture({ role: "admin" }));

    await useMemberStore.getState().updateWorkspaceMemberRole(WS, USER, "admin");

    const [row] = useMemberStore.getState().workspaceMembers;
    expect(row?.role).toBe("admin");
    expect(row?.user.name).toBe("Guest Person");
    expect(row?.user.email).toBe("guest@example.com");
  });
});

describe("updateWorkspaceMemberName", () => {
  it("PATCHes just the name and updates the row in place", async () => {
    useMemberStore.setState({
      workspaceMembers: [
        memberFixture({
          user: { id: USER, email: "guest@example.com", name: "guest@example.com", username: "guest", avatar_url: "" },
        }),
      ],
    });
    mockedApi.mockResolvedValueOnce(memberFixture());

    await useMemberStore.getState().updateWorkspaceMemberName(WS, USER, "Guest Person");

    expect(mockedApi).toHaveBeenCalledWith(
      `/api/v1/workspaces/${WS}/members/${USER}`,
      { method: "PATCH", body: { name: "Guest Person" } },
    );
    expect(useMemberStore.getState().workspaceMembers[0]?.user.name).toBe("Guest Person");
  });

  // A 403 here is the cross-tenant guard: the member has set their own name and
  // no workspace admin may overwrite it. It must reach the caller, not be eaten.
  it("propagates a refusal instead of silently reporting success", async () => {
    mockedApi.mockRejectedValueOnce(new Error("this member set their own display name"));

    await expect(
      useMemberStore.getState().updateWorkspaceMemberName(WS, USER, "Something Else"),
    ).rejects.toThrow(/own display name/);
  });
});
