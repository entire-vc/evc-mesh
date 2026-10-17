import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";

const apiMock = vi.fn();
vi.mock("@/lib/api", () => ({ api: (...a: unknown[]) => apiMock(...a), getAccessToken: vi.fn(() => null) }));

import { TaskCard } from "@/components/task-card";
import { resetSlotRegistryForTests } from "@/lib/extension-slots";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Task } from "@/types";

const base = { id: "t1", title: "Card", priority: "none", labels: [], vcs_link_count: 1 } as unknown as Task;

function registry(enabled: boolean) {
  apiMock.mockResolvedValue({
    extensions: [{ id: "gitlab", name: "GitLab", status: { enabled }, manifest: { capabilities: { ui: ["task.badge"] } } }],
  });
}

describe("board card VCS icon vs the GitLab task.badge slot", () => {
  beforeEach(() => {
    apiMock.mockReset();
    resetSlotRegistryForTests();
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as never });
  });

  it("GitLab-only task: the slot badge replaces the generic icon when enabled", async () => {
    registry(true);
    render(<TaskCard task={{ ...base, vcs_providers: ["gitlab"] }} />);
    expect(await screen.findByTestId("slot-badge-gitlab")).toBeInTheDocument();
    expect(screen.queryByLabelText(/vcs|link/i)).toBeNull();
  });

  it("GitLab-only task: no badge, the generic icon stays when GitLab is disabled (red control)", async () => {
    registry(false);
    render(<TaskCard task={{ ...base, vcs_providers: ["gitlab"] }} />);
    await waitFor(() => expect(apiMock).toHaveBeenCalled());
    await act(async () => {});
    expect(screen.queryByTestId("slot-badge-gitlab")).toBeNull();
    expect(screen.queryByLabelText(/vcs|link/i)).not.toBeNull();
  });

  it("GitHub link keeps the generic icon", async () => {
    registry(true);
    render(<TaskCard task={{ ...base, vcs_providers: ["github"] }} />);
    expect(await screen.findByLabelText(/vcs|link/i)).toBeInTheDocument();
  });

  it("older API without vcs_providers keeps the generic icon", async () => {
    registry(true);
    render(<TaskCard task={base} />);
    expect(await screen.findByLabelText(/vcs|link/i)).toBeInTheDocument();
  });
});
