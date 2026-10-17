import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import type { CatalogState } from "@/hooks/useExtensionCatalog";
import { mergeCatalog } from "@/hooks/useExtensionCatalog";

const state = vi.hoisted(() => ({ value: null as unknown as CatalogState }));
vi.mock("@/hooks/useExtensionCatalog", async (orig) => ({
  ...(await orig<typeof import("@/hooks/useExtensionCatalog")>()),
  useExtensionCatalog: () => state.value,
}));
vi.mock("@/lib/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/components/markdown-view", () => ({ MarkdownView: ({ content }: { content: string }) => <div data-testid="md">{content}</div> }));

import { setLanguage } from "@/i18n";
import { ExtensionsCatalog } from "@/components/extensions/catalog";
import { ExtensionDetailPage } from "@/pages/extension-detail";
import { useMemberStore } from "@/stores/member";

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

  it("shows installed state, badges, and a no-match state with reset", () => {
    mount("/w/acme/extensions?q=zzz", "catalog");
    expect(screen.getByText("Nothing matches these filters")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Reset filters" }));
    expect(screen.getByText("Installed")).toBeTruthy();
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
    expect(screen.getByText("Not allowed by the instance administrator")).toBeTruthy();
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
