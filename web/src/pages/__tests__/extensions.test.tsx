import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";

vi.mock("@/lib/api", () => ({ api: vi.fn().mockResolvedValue({ integrations: [], effective: [] }) }));
vi.mock("@/pages/integrations", () => ({ IntegrationsPage: () => <div>legacy-integrations</div> }));

const overview = vi.hoisted(() => ({
  value: { state: "ready", catalogCount: 0, updatesCount: 0 } as {
    state: "loading" | "error" | "ready";
    catalogCount: number;
    updatesCount: number;
  },
}));
vi.mock("@/hooks/useExtensionsOverview", () => ({ useExtensionsOverview: () => overview.value }));

import { setLanguage } from "@/i18n";
import { ExtensionsPage, IntegrationsRedirect, extensionsTab } from "@/pages/extensions";

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
  overview.value = { state: "ready", catalogCount: 0, updatesCount: 0 };
  await setLanguage("en");
});

describe("Extensions section", () => {
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
    expect(screen.getByText("legacy-integrations")).toBeTruthy();
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
    overview.value = { state: "ready", catalogCount: 0, updatesCount: 3 };
    renderAt("/w/acme/extensions");
    expect(screen.getByLabelText("3 updates available").textContent).toBe("3");
  });

  it("renders loading and error states without raw server text", () => {
    overview.value = { state: "loading", catalogCount: 0, updatesCount: 0 };
    const { unmount } = renderAt("/w/acme/extensions");
    expect(screen.getByRole("status")).toBeTruthy();
    unmount();
    overview.value = { state: "error", catalogCount: 0, updatesCount: 0 };
    renderAt("/w/acme/extensions");
    expect(screen.getByRole("alert").textContent).toContain("Couldn't load the catalog");
  });

  describe("Updates tab states", () => {
    it("shows its own loading state", () => {
      overview.value = { state: "loading", catalogCount: 0, updatesCount: 0 };
      renderAt("/w/acme/extensions?tab=updates");
      expect(
        screen.getByRole("status", { name: "Checking for updates" }),
      ).toBeTruthy();
      expect(screen.queryByText("Everything is up to date")).toBeNull();
    });

    it("shows its own error state without raw server text", () => {
      overview.value = { state: "error", catalogCount: 0, updatesCount: 0 };
      renderAt("/w/acme/extensions?tab=updates");
      const alert = screen.getByRole("alert");
      expect(alert.textContent).toContain("Couldn't check for updates");
      expect(alert.textContent).not.toContain("Couldn't load the catalog");
      expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
    });

    it("retry reloads the page, and a successful load then shows the content", () => {
      const reload = vi.fn();
      const original = window.location;
      Object.defineProperty(window, "location", {
        configurable: true,
        value: { ...original, reload },
      });
      try {
        overview.value = { state: "error", catalogCount: 0, updatesCount: 0 };
        const { unmount } = renderAt("/w/acme/extensions?tab=updates");
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(reload).toHaveBeenCalledTimes(1);
        unmount();
        overview.value = { state: "ready", catalogCount: 0, updatesCount: 0 };
        renderAt("/w/acme/extensions?tab=updates");
        expect(screen.getByText("Everything is up to date")).toBeTruthy();
        expect(screen.queryByRole("alert")).toBeNull();
      } finally {
        Object.defineProperty(window, "location", {
          configurable: true,
          value: original,
        });
      }
    });
  });

  it("falls back to catalog for an unknown tab", () => {
    expect(extensionsTab(new URLSearchParams("tab=nope"))).toBe("catalog");
  });

  it("hides admin actions while the flag is off", () => {
    renderAt("/w/acme/extensions");
    expect(screen.queryByText("Install from repository")).toBeNull();
  });

  it("renders instance settings as inert text, not a link, while its route does not exist", async () => {
    vi.stubEnv("VITE_EXTENSIONS_ADMIN_ACTIONS", "true");
    vi.resetModules();
    const { useMemberStore } = await import("@/stores/member");
    useMemberStore.setState({ myRole: "admin" });
    const mod = await import("@/pages/extensions");
    render(
      <MemoryRouter initialEntries={["/w/acme/extensions"]}>
        <Routes>
          <Route path="w/:wsSlug/extensions" element={<mod.ExtensionsPage />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByText("Instance settings").closest("a")).toBeNull();
    expect(screen.getByText("Install from repository")).toBeTruthy();
    vi.unstubAllEnvs();
  });
});
