import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";

const role = vi.hoisted(() => ({ value: "admin", instanceAdmin: true }));
vi.mock("@/lib/api", () => ({
  api: vi.fn(async (path: string) => {
    if (path.endsWith("/extensions/instance/overview")) {
      if (!role.instanceAdmin) throw new Error("forbidden");
      return { auto_update: false, items: [] };
    }
    return path.endsWith("/members/me") ? { role: role.value } : { integrations: [], effective: [] };
  }),
}));
vi.mock("@/pages/mcp-reference", () => ({ McpReference: () => <div>mcp-reference</div> }));

const overview = vi.hoisted(() => ({
  value: { state: "ready", catalogCount: 0 } as {
    state: "loading" | "error" | "ready";
    catalogCount: number;
  },
}));
// Updates of repo-installed extensions: the hook's own behaviour is covered in extension-repo-ui.test.tsx.
const repoUpdates = vi.hoisted(() => ({
  reload: vi.fn(),
  value: { status: "ready", updates: [], count: 0 } as { status: "loading" | "ready" | "forbidden" | "error"; updates: never[]; count: number },
}));
vi.mock("@/hooks/useRepoUpdates", () => ({ useRepoUpdates: () => ({ ...repoUpdates.value, reload: repoUpdates.reload }) }));
// The catalog tab follows the same load state; its own behaviour is covered in extension-catalog.test.tsx.
vi.mock("@/hooks/useExtensionCatalog", () => ({
  useExtensionCatalog: () => ({
    state: overview.value.state,
    entries: [],
    catalogUnavailable: false,
    reload: () => undefined,
    refresh: () => undefined,
  }),
}));

import { setLanguage } from "@/i18n";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import { ExtensionsPage, IntegrationsRedirect, extensionsTab } from "@/pages/extensions";

// An earlier test replaces fetchMyRole with a spy; the role tests below need the real one.
const realFetchMyRole = useMemberStore.getState().fetchMyRole;
const useMemberStoreRole = () => useMemberStore.getState().myRole;

function Where() {
  const l = useLocation();
  return <div data-testid="where">{l.pathname + l.search + l.hash}</div>;
}

function renderAt(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="w/:wsSlug/extensions" element={<ExtensionsPage />} />
        <Route path="w/:wsSlug/integrations" element={<IntegrationsRedirect />} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

beforeEach(async () => {
  role.instanceAdmin = true;
  overview.value = { state: "ready", catalogCount: 0 };
  repoUpdates.value = { status: "ready", updates: [], count: 0 };
  repoUpdates.reload.mockClear();
  await setLanguage("en");
});

describe("Extensions section", () => {
  it("fetches the caller's role itself, so a reload on the page does not leave an admin read-only", () => {
    const fetchMyRole = vi.fn().mockResolvedValue(undefined);
    useMemberStore.setState({ myRole: null, fetchMyRole });
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-uuid-1", slug: "acme", name: "Acme" } as never });
    renderAt("/w/acme/extensions?tab=installed");
    expect(fetchMyRole).toHaveBeenCalledWith("ws-uuid-1");
  });

  it("redirects the old integrations URL, keeping query and hash", () => {
    renderAt("/w/acme/integrations?tab=updates#x");
    expect(screen.getByTestId("where").textContent).toBe("/w/acme/extensions?tab=updates#x");
    expect(screen.getByRole("tab", { name: /Updates/ }).getAttribute("aria-selected")).toBe("true");
  });

  it("switches tabs and mirrors the choice in the URL", () => {
    renderAt("/w/acme/extensions");
    expect(screen.getByRole("tab", { name: "Catalog" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByText("The catalog is empty")).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: "Installed" }));
    expect(screen.getByText("mcp-reference")).toBeTruthy();
    expect(screen.getByTestId("where").textContent).toContain("tab=installed");

    fireEvent.click(screen.getByRole("tab", { name: "Updates" }));
    expect(screen.getByText("Everything is up to date")).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: "Catalog" }));
    expect(screen.getByTestId("where").textContent).toBe("/w/acme/extensions");
  });

  it("shows the counter on Updates only when there are updates", () => {
    const { unmount } = renderAt("/w/acme/extensions");
    expect(screen.queryByLabelText(/updates available/)).toBeNull();
    unmount();
    repoUpdates.value = { status: "ready", updates: [], count: 3 };
    renderAt("/w/acme/extensions");
    expect(screen.getByLabelText("3 updates available").textContent).toBe("3");
  });

  it("renders loading and error states without raw server text", () => {
    overview.value = { state: "loading", catalogCount: 0 };
    const { unmount } = renderAt("/w/acme/extensions");
    expect(screen.getByRole("status")).toBeTruthy();
    unmount();
    overview.value = { state: "error", catalogCount: 0 };
    renderAt("/w/acme/extensions");
    expect(screen.getByRole("alert").textContent).toContain("Couldn't load the catalog");
  });

  describe("Updates tab states", () => {
    it("shows its own loading state", () => {
      repoUpdates.value = { status: "loading", updates: [], count: 0 };
      renderAt("/w/acme/extensions?tab=updates");
      expect(screen.getByRole("status", { name: "Checking for updates" })).toBeTruthy();
      expect(screen.queryByText("Everything is up to date")).toBeNull();
    });

    it("shows its own error state without raw server text", () => {
      repoUpdates.value = { status: "error", updates: [], count: 0 };
      renderAt("/w/acme/extensions?tab=updates");
      const alert = screen.getByRole("alert");
      expect(alert.textContent).toContain("Couldn't check for updates");
      expect(alert.textContent).not.toContain("Couldn't load the catalog");
      fireEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(repoUpdates.reload).toHaveBeenCalledTimes(1);
    });

    it("a non-administrator gets an explanation, not an error", () => {
      repoUpdates.value = { status: "forbidden", updates: [], count: 0 };
      renderAt("/w/acme/extensions?tab=updates");
      expect(screen.getByText("Only for instance administrators")).toBeTruthy();
      expect(screen.queryByRole("alert")).toBeNull();
    });
  });

  it("falls back to catalog for an unknown tab", () => {
    expect(extensionsTab(new URLSearchParams("tab=nope"))).toBe("catalog");
  });

  it("hides the install button from anyone but the instance administrator (red control)", async () => {
    role.instanceAdmin = false;
    renderAt("/w/acme/extensions");
    await screen.findByText("The catalog is empty");
    await waitFor(() => expect(screen.queryByText("Install from repository")).toBeNull());
  });

  it("offers the instance administrator the settings link and the install button", async () => {
    renderAt("/w/acme/extensions");
    expect((await screen.findByText("Instance settings")).closest("a")?.getAttribute("href")).toBe("/w/acme/instance/extensions");
    fireEvent.click(screen.getByRole("button", { name: /Install from repository/ }));
    expect(screen.getByRole("dialog", { name: "Install from repository" })).toBeTruthy();
  });

  describe("instance settings link on the catalog tab", () => {
    function mountWith(workspaceRole: string, instanceAdmin: boolean) {
      role.value = workspaceRole;
      role.instanceAdmin = instanceAdmin;
      useMemberStore.setState({ myRole: null, fetchMyRole: realFetchMyRole });
      useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as never });
      renderAt("/w/acme/extensions");
    }

    it("an instance admin sees the link without opening the Installed tab", async () => {
      mountWith("admin", true);
      const link = await screen.findByText("Instance settings");
      expect(link.closest("a")?.getAttribute("href")).toBe("/w/acme/instance/extensions");
    });

    it("an instance admin who is only a member of this workspace still sees the link", async () => {
      mountWith("member", true);
      expect(await screen.findByText("Instance settings")).toBeTruthy();
    });

    it("a workspace admin who is not the instance admin gets no link (red control)", async () => {
      mountWith("admin", false);
      await screen.findByText("The catalog is empty");
      await waitFor(() => expect(useMemberStoreRole()).toBe("admin"));
      expect(screen.queryByText("Instance settings")).toBeNull();
    });
  });
});
