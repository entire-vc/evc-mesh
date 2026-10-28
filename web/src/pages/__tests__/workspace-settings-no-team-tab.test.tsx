import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn(), getAccessToken: vi.fn(() => null) };
});

import { api } from "@/lib/api";
import { WorkspaceSettingsPage } from "@/pages/workspace-settings";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import type { Workspace } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const WORKSPACE = { id: "ws1", name: "Acme", slug: "acme", owner_id: "u1", settings: {} } as Workspace;

beforeEach(() => {
  mockedApi.mockReset();
  mockedApi.mockResolvedValue({});
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE, workspaces: [WORKSPACE] });
  useMemberStore.setState({ myRole: "owner" });
});

// Team Directory duplicated Members; its metadata now lives on the Team page
// cards (org-chart), so settings keep a single people list: Members.
describe("WorkspaceSettingsPage — one people list", () => {
  it("has a Members tab and no Team Directory tab", async () => {
    render(
      <MemoryRouter>
        <WorkspaceSettingsPage />
      </MemoryRouter>,
    );
    await waitFor(() => expect(mockedApi).toHaveBeenCalled());
    expect(screen.getByRole("button", { name: "Members" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Team Directory" })).not.toBeInTheDocument();
  });
});
