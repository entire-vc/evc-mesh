import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import type { CatalogState } from "@/hooks/useExtensionCatalog";
import { mergeCatalog, type WorkspaceDto } from "@/hooks/useExtensionCatalog";

const state = vi.hoisted(() => ({ value: null as unknown as CatalogState }));
vi.mock("@/hooks/useExtensionCatalog", async (orig) => ({
  ...(await orig<typeof import("@/hooks/useExtensionCatalog")>()),
  useExtensionCatalog: () => state.value,
}));
const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api: apiMock }));
vi.mock("@/lib/extension-admin", () => ({ useIsInstanceAdmin: () => false }));
vi.mock("@/components/markdown-view", () => ({ MarkdownView: ({ content }: { content: string }) => <div>{content}</div> }));

import { setLanguage } from "@/i18n";
import { ExtensionDetailPage } from "@/pages/extension-detail";
import { ExtensionConnectPage } from "@/pages/extension-connect";
import { InstalledExtensions } from "@/components/extensions/installed";
import { extensionSettingsPath } from "@/lib/extension-settings-route";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";

const sum = (id: string, over: Record<string, unknown> = {}) => ({
  id, name: id, version: "1.0.0", kind: "integration", runtime: "builtin", publisher: "Entire VC",
  description: `About ${id}`, category: "development", source: "builtin", trust_level: "builtin",
  blocked: false, blocked_reason: "", manifest: { capabilities: {} }, ...over,
});
const ws = (id: string, over: Partial<WorkspaceDto> = {}): WorkspaceDto =>
  ({ id, name: id, version: "1.0.0", kind: "integration", category: "development", connection: null,
     status: { enabled: false, source: "none" }, ...over }) as WorkspaceDto;

// GitLab connected in this workspace; Runner is the hidden built-in whose settings live in its own section.
const entries = () => mergeCatalog(
  [sum("gitlab"), sum("runner")],
  [
    ws("gitlab", { connection: {} as never, status: { enabled: true, source: "workspace" } }),
    ws("runner", { settings_managed_elsewhere: true, hidden: true }),
  ],
);

function Where() { const l = useLocation(); return <div data-testid="where">{l.pathname + l.search}</div>; }

function mount(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="w/:wsSlug/extensions" element={<InstalledExtensions catalog={state.value} />} />
        <Route path="w/:wsSlug/extensions/:extId" element={<ExtensionDetailPage />} />
        <Route path="w/:wsSlug/extensions/:extId/connect" element={<ExtensionConnectPage />} />
        <Route path="w/:wsSlug/runner" element={<h1>Runner section</h1>} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

beforeEach(async () => {
  await setLanguage("en");
  apiMock.mockReset();
  apiMock.mockRejectedValue(new Error("no card"));
  useMemberStore.setState({ myRole: "admin", myRoleLoaded: true } as never);
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme" } } as never);
  state.value = { state: "ready", catalogUnavailable: false, reload: vi.fn(), refresh: vi.fn(), entries: entries() };
});

describe("extensionSettingsPath", () => {
  const e = (id: string, over: Record<string, unknown> = {}) =>
    ({ id, kind: "integration", hidden: false, managedElsewhere: false, ...over }) as never;
  it("leads each kind to its own settings", () => {
    expect(extensionSettingsPath(e("runner", { hidden: true, managedElsewhere: true }), "acme")).toBe("/w/acme/runner");
    // a theme goes to its Settings window like any extension (U4, П8), not to a wizard of its own
    expect(extensionSettingsPath(e("prototypes", { kind: "theme" }), "acme")).toBe("/w/acme/extensions?tab=installed&settings=prototypes");
    expect(extensionSettingsPath(e("gitlab"), "acme")).toBe("/w/acme/extensions?tab=installed&settings=gitlab");
  });
  it("has no page for a provider whose settings are written elsewhere and no section of its own", () => {
    expect(extensionSettingsPath(e("x", { managedElsewhere: true }), "acme")).toBeNull();
  });
});

describe("Open settings", () => {
  it("on a connected extension opens that extension's settings dialog", async () => {
    mount("/w/acme/extensions/gitlab");
    fireEvent.click(screen.getByRole("button", { name: "Open settings" }));
    expect(await screen.findByRole("dialog", { name: "gitlab" })).toBeTruthy();
    expect(screen.getByText("gitlab settings")).toBeTruthy();
    // the parameter is consumed: closing the dialog does not bring it back
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/w/acme/extensions?tab=installed"));
  });

  it("a member without rights lands on the list, not in a dialog", async () => {
    useMemberStore.setState({ myRole: "member", myRoleLoaded: true } as never);
    mount("/w/acme/extensions?tab=installed&settings=gitlab");
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/w/acme/extensions?tab=installed"));
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("Runner has no catalog page and no Connect wizard", () => {
  it("its extension page opens the Runner section", async () => {
    mount("/w/acme/extensions/runner");
    expect(await screen.findByRole("heading", { name: "Runner section" })).toBeTruthy();
    expect(screen.getByTestId("where").textContent).toBe("/w/acme/runner");
  });

  it("its connect URL opens the Runner section instead of the wizard", async () => {
    mount("/w/acme/extensions/runner/connect");
    expect(await screen.findByRole("heading", { name: "Runner section" })).toBeTruthy();
    expect(screen.queryByText("Next")).toBeNull();
  });

  it("a managed-elsewhere provider without its own section gets an explanation, not the wizard", () => {
    state.value = {
      ...state.value,
      entries: mergeCatalog([sum("chat")], [ws("chat", { settings_managed_elsewhere: true })]),
    };
    mount("/w/acme/extensions/chat/connect");
    expect(screen.getByRole("note").textContent).toBe("Managed through the extension's own page");
    expect(screen.queryByRole("button", { name: "Next" })).toBeNull();
  });
});
