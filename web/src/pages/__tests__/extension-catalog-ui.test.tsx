import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import type { CatalogState } from "@/hooks/useExtensionCatalog";
import { mergeCatalog } from "@/hooks/useExtensionCatalog";

const state = vi.hoisted(() => ({ value: null as unknown as CatalogState }));
vi.mock("@/hooks/useExtensionCatalog", async (orig) => ({
  ...(await orig<typeof import("@/hooks/useExtensionCatalog")>()),
  useExtensionCatalog: () => state.value,
}));
const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api: apiMock }));
vi.mock("@/lib/use-is-mobile", () => ({ useIsMobile: () => false }));
const applyTheme = vi.hoisted(() => vi.fn());
vi.mock("@/core/themes/runtime", () => ({ applyWorkspaceTheme: applyTheme }));
vi.mock("@/components/markdown-view", () => ({ MarkdownView: ({ content }: { content: string }) => <div data-testid="md">{content}</div> }));

import { setLanguage } from "@/i18n";
import { ExtensionsCatalog } from "@/components/extensions/catalog";
import { ExtensionDetailPage } from "@/pages/extension-detail";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";

const sum = (id: string, over: Record<string, unknown> = {}) => ({
  id, name: id, version: "1.2.0", kind: "integration", runtime: "builtin", publisher: "Entire VC",
  description: `About ${id}`, category: "development", source: "builtin", trust_level: "builtin",
  blocked: false, blocked_reason: "",
  manifest: { mesh_api: ">=1 <2", repository: "https://git.example/x", capabilities: { api: ["tasks:read"], network: { allowed_domains: ["gitlab.com"] } } },
  ...over,
});

function Where() { return <div data-testid="where">{useLocation().pathname + useLocation().search}</div>; }

function mount(url: string, ui: "catalog" | "detail") {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="w/:wsSlug/extensions" element={ui === "catalog" ? <ExtensionsCatalog catalog={state.value} /> : null} />
        <Route path="w/:wsSlug/extensions/:extId" element={<ExtensionDetailPage />} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

beforeEach(async () => {
  await setLanguage("en");
  apiMock.mockReset();
  apiMock.mockRejectedValue(new Error("no card"));
  useMemberStore.setState({ myRole: "member" } as never);
  state.value = {
    state: "ready", catalogUnavailable: false, reload: vi.fn(), refresh: vi.fn(),
    entries: mergeCatalog(
      [sum("gitlab"), sum("telegram", { category: "communication", kind: "plugin" }),
       sum("custom-x", { category: "security", source: "repo", trust_level: "custom", runtime: "external", manifest: {} })],
      [{ id: "gitlab", name: "gitlab", version: "1.2.0", kind: "integration", category: "development",
         connection: {}, status: { enabled: true, source: "workspace" } }],
    ),
  };
});

describe("catalog grid", () => {
  it("ticking a category changes the cards and the count", () => {
    mount("/w/acme/extensions", "catalog");
    expect(screen.getByTestId("catalog-count").textContent).toBe("3 extensions");
    fireEvent.click(screen.getByRole("checkbox", { name: "Communication" }));
    expect(screen.getByTestId("catalog-count").textContent).toBe("1 extension");
    const list = screen.getByRole("list");
    expect(within(list).getAllByRole("link").map((a) => a.getAttribute("href"))).toEqual(["/w/acme/extensions/telegram"]);
    expect(screen.getByTestId("where").textContent).toContain("cat=communication");
  });

  it("leaves out the built-in Runner the API marks hidden", () => {
    state.value = {
      ...state.value,
      entries: mergeCatalog([sum("gitlab"), sum("runner", { category: "agents", kind: "plugin" })], [
        { id: "gitlab", name: "gitlab", version: "1.2.0", kind: "integration", category: "development" },
        { id: "runner", name: "Runner", version: "1.0.0", kind: "plugin", category: "agents", hidden: true,
          settings_managed_elsewhere: true, connection: { settings: {} }, status: { enabled: true, source: "workspace" } },
      ]),
    };
    mount("/w/acme/extensions", "catalog");
    expect(screen.getByTestId("catalog-count").textContent).toBe("1 extension");
    expect(within(screen.getByRole("list")).getAllByRole("link").map((a) => a.getAttribute("href"))).toEqual(["/w/acme/extensions/gitlab"]);
    expect(state.value.entries.map((e) => e.id)).toContain("runner");
  });

  it("shows installed state, badges, and a no-match state with reset", () => {
    mount("/w/acme/extensions?q=zzz", "catalog");
    expect(screen.getByText("Nothing matches these filters")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Reset filters" }));
    expect(screen.getAllByText("Enabled in workspace").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Official").length).toBeGreaterThan(0);
    expect(screen.getByText("Custom", { selector: "span" })).toBeTruthy();
  });

  it("shows the unavailable banner but still lists built-ins", () => {
    state.value = { ...state.value, catalogUnavailable: true };
    mount("/w/acme/extensions", "catalog");
    expect(screen.getByRole("status").textContent).toContain("The catalog is unavailable");
    expect(screen.getAllByRole("link").length).toBe(3);
  });

  it("marks an extension the instance does not allow and hides nothing", () => {
    state.value = { ...state.value, entries: mergeCatalog([sum("x", { allowed: false })], []) };
    mount("/w/acme/extensions", "catalog");
    expect(screen.getByText("Not allowed")).toBeTruthy();
  });

  it("renders loading and error states", () => {
    state.value = { ...state.value, state: "loading" };
    const { unmount } = mount("/w/acme/extensions", "catalog");
    expect(screen.getByRole("status", { name: "Loading catalog" })).toBeTruthy();
    unmount();
    state.value = { ...state.value, state: "error" };
    mount("/w/acme/extensions", "catalog");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(state.value.reload).toHaveBeenCalled();
  });
});

describe("extension page", () => {
  it.each([
    ["connected", true, { connection: { settings: { runner_url: "https://r.example" } }, status: { enabled: true, source: "workspace" } }],
    ["connected but not allowed", false, { connection: { settings: { runner_url: "https://r.example" } }, status: { enabled: true, source: "workspace" } }],
    ["installed, not connected", true, { connection: null, status: { enabled: false, source: "none" } }],
    ["blocked", true, { connection: null, status: { enabled: false, source: "none" } }, { blocked: true, blocked_reason: "CVE" }],
  ])("the runner program, %s, is managed only on the Runner screen", (_name, allowed, ws, extra: Record<string, unknown> = {}) => {
    state.value = { ...state.value, entries: mergeCatalog(
      [sum("agent-runner", { kind: "plugin", runtime: "external", source: "catalog", trust_level: "official", allowed, ...extra })],
      [{ id: "agent-runner", name: "agent-runner", version: "1.2.0", kind: "plugin", category: "agents", ...ws }] as never,
    ) };
    mount("/w/acme/extensions/agent-runner", "detail");
    expect(screen.queryByRole("button", { name: "Open settings" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Manage in Runner" }));
    expect(screen.getByTestId("where").textContent).toBe("/w/acme/runner");
  });

  it("opens by direct link with the header and switches tabs", () => {
    mount("/w/acme/extensions/gitlab", "detail");
    expect(screen.getByRole("heading", { name: "gitlab" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open settings" })).toBeTruthy();
    expect(screen.getByTestId("md").textContent).toBe("About gitlab");

    fireEvent.click(screen.getByRole("tab", { name: "Permissions" }));
    expect(screen.getByText("Read tasks")).toBeTruthy();
    expect(screen.getByText("gitlab.com")).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: "Versions" }));
    expect(screen.getByText("Compatible with Mesh API >=1 <2")).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: "Checks" }));
    expect(screen.getByText("Official. Ships with Mesh.")).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: "Support" }));
    expect(screen.getByRole("link", { name: /Open an issue|git.example/ })).toBeTruthy();
    expect(screen.getByTestId("where").textContent).toContain("tab=support");
  });

  it("opens a tab straight from the URL and shows the not-checked note for custom", () => {
    mount("/w/acme/extensions/custom-x?tab=checks", "detail");
    expect(screen.getByRole("tab", { name: "Checks" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getAllByText("Not checked by the catalog.", { exact: false }).length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: "Connect" })).toBeTruthy();
  });

  it("not-found and not-allowed states have their own text", () => {
    mount("/w/acme/extensions/nope", "detail");
    expect(screen.getByText("Extension not found")).toBeTruthy();
  });

  it("a blocked extension cannot be connected", () => {
    state.value = { ...state.value, entries: mergeCatalog([sum("b", { blocked: true, blocked_reason: "CVE" })], []) };
    mount("/w/acme/extensions/b", "detail");
    expect((screen.getByRole("button", { name: "Blocked" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("alert").textContent).toContain("CVE");
  });
});

describe("Spark-listed extension", () => {
  const hello = () => sum("mesh-ext-hello", { runtime: "external", source: "catalog", trust_level: "verified", manifest: {} });

  it("sits in the same grid as built-ins and not marked installed", () => {
    state.value = { ...state.value, entries: mergeCatalog([sum("gitlab")], [], [hello()]) };
    mount("/w/acme/extensions", "catalog");
    const hrefs = within(screen.getByRole("list")).getAllByRole("link").map((a) => a.getAttribute("href"));
    expect(hrefs).toContain("/w/acme/extensions/mesh-ext-hello");
    expect(screen.getByTestId("catalog-count").textContent).toBe("2 extensions");
  });

  it("takes README and checks from the Spark card", async () => {
    apiMock.mockResolvedValue({ readme: "# From Spark card", versions: [{ version: "1.0.0" }], checks: { result: "passed" } });
    state.value = { ...state.value, entries: mergeCatalog([], [], [hello()]) };
    mount("/w/acme/extensions/mesh-ext-hello", "detail");
    await waitFor(() => expect(screen.getByTestId("md").textContent).toBe("# From Spark card"));
    expect(apiMock).toHaveBeenCalledWith("/api/v1/extensions/mesh-ext-hello/catalog-card");
  });

  it("offers catalog install by instance-admin status, not by the workspace role", async () => {
    state.value = { ...state.value, entries: mergeCatalog([], [], [hello()]) };
    // workspace admin who is not an instance admin: overview is refused
    useMemberStore.setState({ myRole: "admin" } as never);
    mount("/w/acme/extensions/mesh-ext-hello", "detail");
    await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/v1/extensions/instance/overview"));
    expect(screen.queryByRole("button", { name: "Install to instance" })).toBeNull();
    cleanup();

    // instance admin who is only a member of this workspace
    useMemberStore.setState({ myRole: "member" } as never);
    apiMock.mockImplementation((url: string) =>
      url === "/api/v1/extensions/install" ? Promise.resolve({ extension_id: "mesh-ext-hello", version: "1.0.0" })
        : url === "/api/v1/extensions/instance/overview" ? Promise.resolve({ auto_update: false, items: [] })
        : Promise.reject(new Error("no card")));
    mount("/w/acme/extensions/mesh-ext-hello", "detail");
    const btn = (await screen.findByRole("button", { name: "Install to instance" })) as HTMLButtonElement;
    expect(btn.disabled).toBe(false);
    fireEvent.click(btn);
    await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/v1/extensions/install", { method: "POST", body: { source: "catalog", id: "mesh-ext-hello" } }));
    await waitFor(() => expect(state.value.reload).toHaveBeenCalled());
  });
});

describe("theme status", () => {
  const themes = (active?: string) => mergeCatalog(
    [sum("mesh-default", { kind: "theme", manifest: { capabilities: {} } }), sum("graphite", { kind: "theme", manifest: { capabilities: {} } })],
    [], [], active,
  );

  it("marks the workspace's theme active and the rest not selected, never connected", () => {
    state.value = { ...state.value, entries: themes("graphite") };
    mount("/w/acme/extensions", "catalog");
    const card = (id: string) => screen.getAllByRole("link").find((a) => a.getAttribute("href") === `/w/acme/extensions/${id}`) as HTMLElement;
    expect(within(card("graphite")).getByText("Enabled in workspace")).toBeTruthy();
    expect(within(card("mesh-default")).getByText("Available")).toBeTruthy();
    expect(state.value.entries.every((e) => !e.connected)).toBe(true);
  });

  it("lists themes in their own section, with the theme icon, after the extensions", () => {
    state.value = { ...state.value, entries: [...state.value.entries, ...themes("graphite")] };
    mount("/w/acme/extensions", "catalog");
    const section = screen.getByTestId("section-themes");
    expect(within(section).getByRole("heading", { name: /Themes/ })).toBeTruthy();
    const hrefs = within(section).getAllByRole("link").map((a) => a.getAttribute("href"));
    expect(hrefs.sort()).toEqual(["/w/acme/extensions/graphite", "/w/acme/extensions/mesh-default"]);
    expect(section.querySelectorAll('[data-kind="theme"]').length).toBe(2);
    const rest = screen.getByRole("heading", { name: /Extensions and integrations/ }).closest("section") as HTMLElement;
    expect(within(rest).queryByText("graphite")).toBeNull();
    expect(within(rest).getByText("gitlab")).toBeTruthy();
  });

  it("without themes the grid has no sections", () => {
    mount("/w/acme/extensions", "catalog");
    expect(screen.queryByTestId("section-themes")).toBeNull();
    expect(screen.queryByRole("heading", { name: /Extensions and integrations/ })).toBeNull();
  });

  it("enables a theme in the workspace from its page, the way an extension is enabled", async () => {
    useMemberStore.setState({ myRole: "admin" } as never);
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme" } } as never);
    apiMock.mockImplementation((url: string) => Promise.resolve(url.endsWith("/theme") ? { theme_id: "mesh-default" } : Promise.reject(new Error("no card"))));
    state.value = { ...state.value, entries: themes("graphite") };
    mount("/w/acme/extensions/mesh-default", "detail");
    fireEvent.click(screen.getByRole("button", { name: "Enable in workspace" }));
    await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/v1/workspaces/ws1/theme", { method: "PUT", body: { theme_id: "mesh-default", settings: {} } }));
    await waitFor(() => expect(state.value.reload).toHaveBeenCalled());
    expect(applyTheme).toHaveBeenCalledWith("acme");
    expect(screen.getByTestId("where").textContent).toBe("/w/acme/extensions/mesh-default");
  });

  it("the active theme's page opens its Settings window in Installed", () => {
    state.value = { ...state.value, entries: themes("graphite") };
    mount("/w/acme/extensions/graphite", "detail");
    fireEvent.click(screen.getByRole("button", { name: "Open settings" }));
    expect(screen.getByTestId("where").textContent).toBe("/w/acme/extensions?tab=installed&settings=graphite");
  });

  it("a member is told who enables the theme, with no button", () => {
    state.value = { ...state.value, entries: themes("graphite") };
    mount("/w/acme/extensions/mesh-default", "detail");
    expect(screen.queryByRole("button", { name: "Enable in workspace" })).toBeNull();
    expect(screen.getByText(/workspace owner or admin enables the workspace theme/)).toBeTruthy();
  });
});
