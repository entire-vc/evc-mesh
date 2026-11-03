import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import type { CatalogState } from "@/hooks/useExtensionCatalog";
import { mergeCatalog, type WorkspaceDto } from "@/hooks/useExtensionCatalog";

const state = vi.hoisted(() => ({ value: null as unknown as CatalogState, instanceAdmin: true }));
vi.mock("@/hooks/useExtensionCatalog", async (orig) => ({
  ...(await orig<typeof import("@/hooks/useExtensionCatalog")>()),
  useExtensionCatalog: () => state.value,
}));
const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api: apiMock }));
vi.mock("@/lib/extension-admin", () => ({ useIsInstanceAdmin: () => state.instanceAdmin }));
vi.mock("@/components/markdown-view", () => ({ MarkdownView: ({ content }: { content: string }) => <div>{content}</div> }));

import { setLanguage } from "@/i18n";
import { ExtensionDetailPage } from "@/pages/extension-detail";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";

const theme = (over: Record<string, unknown> = {}) => ({
  id: "board-studio", name: "Board Studio", version: "1.0.0", kind: "theme", runtime: "builtin", publisher: "Entire VC",
  description: "About", category: "design", source: "builtin", trust_level: "builtin",
  blocked: false, blocked_reason: "", manifest: { capabilities: {} }, allowed: false, ...over,
});
const wsRow = (): WorkspaceDto => ({ id: "board-studio", name: "Board Studio", version: "1.0.0", kind: "theme",
  category: "design", connection: null, status: { enabled: false, source: "none" } }) as WorkspaceDto;

const setCatalog = (entries: CatalogState["entries"]) => {
  state.value = { state: "ready", catalogUnavailable: false, reload: vi.fn(), refresh: vi.fn(), entries };
};
const mount = () => render(
  <MemoryRouter initialEntries={["/w/acme/extensions/board-studio"]}>
    <Routes><Route path="w/:wsSlug/extensions/:extId" element={<ExtensionDetailPage />} /></Routes>
  </MemoryRouter>,
);

beforeEach(async () => {
  await setLanguage("en");
  apiMock.mockReset();
  apiMock.mockImplementation((path: string) => (path.includes("/instance/themes") || path.includes("/allowed") ? Promise.resolve({}) : Promise.reject(new Error("no card"))));
  state.instanceAdmin = true;
  useMemberStore.setState({ myRole: "admin", myRoleLoaded: true, myRoleWorkspaceId: "ws1" } as never);
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme" } } as never);
  // not installed on the instance: absent from instance summaries
  setCatalog(mergeCatalog([], [], [theme() as never]));
});

describe("builtin theme missing from the instance", () => {
  it("is labelled Not installed, not Not allowed", () => {
    mount();
    expect(screen.getByText("Not installed")).toBeTruthy();
    expect(screen.queryByText("Not allowed")).toBeNull();
  });

  it("is Not installed even when allowed by default, and still offers Install", () => {
    setCatalog(mergeCatalog([], [], [theme({ allowed: true }) as never]));
    mount();
    expect(screen.getByText("Not installed")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Install to instance" })).toBeTruthy();
  });

  it("an instance admin installs it through the theme-install API and the catalog is re-read", async () => {
    mount();
    fireEvent.click(screen.getByRole("button", { name: "Install to instance" }));
    await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/v1/instance/themes", { method: "POST", body: { source: "builtin", id: "board-studio" } }));
    await waitFor(() => expect(state.value.reload).toHaveBeenCalled());
    // the active workspace theme is not touched
    expect(apiMock.mock.calls.some(([, o]) => (o as { method?: string } | undefined)?.method === "PUT")).toBe(false);
  });

  it("a workspace-only admin gets the way to the instance admin and no install button", () => {
    state.instanceAdmin = false;
    mount();
    expect(screen.queryByRole("button", { name: /Install|Allow/ })).toBeNull();
    expect(screen.getByText(/Ask the instance administrator to install it/)).toBeTruthy();
  });

  it("a plain member gets no install either", () => {
    state.instanceAdmin = false;
    useMemberStore.setState({ myRole: "member", myRoleLoaded: true, myRoleWorkspaceId: "ws1" } as never);
    mount();
    expect(screen.queryByRole("button", { name: /Install|Allow/ })).toBeNull();
  });
});

describe("with the generic admin actions flag on", () => {
  it("a workspace-only admin still gets the pointer, not a disabled Install/Allow", () => {
    vi.stubEnv("VITE_EXTENSIONS_ADMIN_ACTIONS", "true");
    state.instanceAdmin = false;
    render(
      <MemoryRouter initialEntries={["/w/acme/extensions/board-studio"]}>
        <Routes><Route path="w/:wsSlug/extensions/:extId" element={<ExtensionDetailPage />} /></Routes>
      </MemoryRouter>,
    );
    expect(screen.queryByRole("button", { name: /Install|Allow/ })).toBeNull();
    expect(screen.getByText(/Ask the instance administrator to install it/)).toBeTruthy();
    vi.unstubAllEnvs();
  });
});

describe("installed theme denied by the instance", () => {
  beforeEach(() => setCatalog(mergeCatalog([theme() as never], [wsRow()])));

  it("is Not allowed (not Not installed) and the instance admin can allow it separately", async () => {
    mount();
    expect(screen.getByText("Not allowed")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Install to instance" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Allow" }));
    await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/v1/extensions/board-studio/allowed", { method: "PUT", body: { allowed: true } }));
  });

  it("a workspace-only admin only reads that the instance admin has not allowed it", () => {
    state.instanceAdmin = false;
    mount();
    expect(screen.queryByRole("button", { name: "Allow" })).toBeNull();
    expect(screen.getByText(/has not allowed this extension/)).toBeTruthy();
  });
});

describe("allowed installed theme", () => {
  it("offers Enable in workspace as before", () => {
    setCatalog(mergeCatalog([theme({ allowed: true }) as never], [wsRow()]));
    mount();
    expect(screen.getByRole("button", { name: "Enable in workspace" })).toBeTruthy();
  });
});
