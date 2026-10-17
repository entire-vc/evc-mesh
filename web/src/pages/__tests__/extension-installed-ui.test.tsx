import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import type { CatalogState } from "@/hooks/useExtensionCatalog";
import { mergeCatalog } from "@/hooks/useExtensionCatalog";

const api = vi.hoisted(() => ({
  setEnabled: vi.fn(), connect: vi.fn(), disconnect: vi.fn(), test: vi.fn(), save: vi.fn(),
}));
vi.mock("@/lib/extension-settings", () => ({
  setWorkspaceExtensionEnabled: api.setEnabled,
  connectWorkspaceExtension: api.connect,
  disconnectWorkspaceExtension: api.disconnect,
  testWorkspaceExtension: api.test,
  saveWorkspaceExtensionSettings: api.save,
}));

import { setLanguage } from "@/i18n";
import { InstalledExtensions } from "@/components/extensions/installed";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";

const sum = (id: string, name: string) => ({
  id, name, version: "1.0.0", kind: "integration", runtime: "builtin", publisher: "Entire VC",
  description: "", category: "development", source: "builtin", trust_level: "builtin",
  blocked: false, blocked_reason: "", manifest: { mesh_api: ">=1 <2", capabilities: {} },
});

let refresh: ReturnType<typeof vi.fn>;
function mount(entries: CatalogState["entries"], state: CatalogState["state"] = "ready") {
  refresh = vi.fn();
  const catalog = { state, entries, catalogUnavailable: false, reload: vi.fn(), refresh } as unknown as CatalogState;
  return render(
    <MemoryRouter initialEntries={["/w/acme/extensions?tab=installed"]}>
      <Routes>
        <Route path="w/:wsSlug/extensions" element={<InstalledExtensions catalog={catalog} />} />
      </Routes>
    </MemoryRouter>,
  );
}

const instanceGitlab = () =>
  mergeCatalog([sum("gitlab", "GitLab")], [{
    id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
    connection: null, status: { enabled: true, source: "instance", health: { ok: true, message: "" } },
  }] as never);

beforeEach(async () => {
  await setLanguage("en");
  Object.values(api).forEach((f) => f.mockReset().mockResolvedValue({ ok: true }));
  useMemberStore.setState({ myRole: "admin" } as never);
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme" } } as never);
});

describe("Installed list", () => {
  it("says where an enabled extension comes from", () => {
    mount(instanceGitlab());
    expect(screen.getByTestId("status-gitlab").textContent).toContain("Active · set up by the instance admin");
  });

  it("turning off an instance-sourced extension writes nothing until the modal is confirmed", async () => {
    mount(instanceGitlab());
    fireEvent.click(screen.getByRole("switch", { name: "GitLab enabled" }));
    expect(await screen.findByText(/Turning it off affects only this workspace/)).toBeTruthy();
    expect(api.setEnabled).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(api.setEnabled).toHaveBeenCalledWith("ws1", "gitlab", false, true));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
  });

  it("cancelling the modal leaves state untouched", async () => {
    mount(instanceGitlab());
    fireEvent.click(screen.getByRole("switch", { name: "GitLab enabled" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(api.setEnabled).not.toHaveBeenCalled();
  });

  it("a workspace-sourced extension is switched off without confirm", async () => {
    mount(mergeCatalog([sum("gitlab", "GitLab")], [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    fireEvent.click(screen.getByRole("switch", { name: "GitLab enabled" }));
    await waitFor(() => expect(api.setEnabled).toHaveBeenCalledWith("ws1", "gitlab", false));
  });

  it("Remove keeps the extension's data and only drops the connection", async () => {
    mount(mergeCatalog([sum("gitlab", "GitLab")], [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    const del = screen.getByRole("radio", { name: /Delete the data too/ }) as HTMLInputElement;
    expect(del.disabled).toBe(true);
    expect((screen.getByRole("radio", { name: /Keep the data/ }) as HTMLInputElement).checked).toBe(true);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.disconnect).toHaveBeenCalledWith("ws1", "gitlab", "keep"));
  });

  it("Remove offers deleting the data where the extension supports it, and sends the choice", async () => {
    mount(mergeCatalog([sum("gitlab", "GitLab")], [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development", can_purge_data: true,
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    fireEvent.click(screen.getByRole("radio", { name: /Delete the data too/ }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.disconnect).toHaveBeenCalledWith("ws1", "gitlab", "delete"));
  });

  it("an extension whose settings live in its own API gets no generic Settings or Remove", () => {
    mount(mergeCatalog([sum("runner", "Runner")], [{
      id: "runner", name: "Runner", version: "1.0.0", kind: "integration", category: "development", settings_managed_elsewhere: true,
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    expect((screen.getByRole("button", { name: "Settings" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "More actions for Runner" }));
    expect(screen.getByRole("menuitem", { name: "Remove" }).getAttribute("aria-disabled")).toBe("true");
  });

  it("Runner without a connection: the switch does not open the generic Connect form", () => {
    mount(mergeCatalog([sum("runner", "Runner")], [{
      id: "runner", name: "Runner", version: "1.0.0", kind: "integration", category: "development", settings_managed_elsewhere: true,
      connection: null, status: { enabled: false, source: "none", health: null },
    }] as never));
    const sw = screen.getByRole("switch", { name: "Runner enabled" }) as HTMLButtonElement;
    expect(sw.disabled).toBe(true);
    fireEvent.click(sw);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("a dialog opened in one workspace closes on switching, and never writes to the other", async () => {
    mount(mergeCatalog([sum("gitlab", "GitLab")], [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    act(() => { useWorkspaceStore.setState({ currentWorkspace: { id: "ws2", slug: "other" } } as never); });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(api.disconnect).not.toHaveBeenCalled();
  });

  it("Test connection runs the extension test, and Roll back stays inert until the backend exists", async () => {
    mount(instanceGitlab());
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    const rollback = screen.getByRole("menuitem", { name: /Roll back/ });
    expect(rollback.getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(rollback);
    expect(api.test).not.toHaveBeenCalled();
    if (!screen.queryByRole("menuitem", { name: "Test connection" })) {
      fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    }
    fireEvent.click(screen.getByRole("menuitem", { name: "Test connection" }));
    await waitFor(() => expect(api.test).toHaveBeenCalledWith("ws1", "gitlab"));
    expect(api.setEnabled).not.toHaveBeenCalled();
    expect(api.disconnect).not.toHaveBeenCalled();
  });

  it("a member cannot trigger Test or Remove from the menu (no request is sent)", () => {
    useMemberStore.setState({ myRole: "member" } as never);
    mount(instanceGitlab());
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Test connection" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    expect(api.test).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("a member sees the list read-only", () => {
    useMemberStore.setState({ myRole: "member" } as never);
    mount(instanceGitlab());
    expect((screen.getByRole("switch", { name: "GitLab enabled" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Only workspace owners and admins can change extensions")).toBeTruthy();
  });

  it("shows loading, error and empty states", () => {
    const { unmount } = mount([], "loading");
    expect(screen.getByRole("status")).toBeTruthy();
    unmount();
    const e = mount([], "error");
    expect(screen.getByRole("alert").textContent).toContain("Could not load installed extensions");
    e.unmount();
    mount([]);
    expect(screen.getByText("Nothing is installed yet")).toBeTruthy();
  });
});
