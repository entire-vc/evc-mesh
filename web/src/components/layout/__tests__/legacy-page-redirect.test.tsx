import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";

// The network is the only mock: the menu hook, the dialog and the redirect run as they do in the app.
const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", () => ({ api: apiMock, getAccessToken: vi.fn(() => null) }));

import { LEGACY_PAGE_TEMPLATES, LegacyPageRedirect, type LegacyPage } from "../legacy-page-redirect";
import { useDashboardsNav } from "../use-dashboards-nav";
import { useDashboardsStore } from "@/stores/dashboards";
import { useWebSocketStore } from "@/stores/websocket";
import type { Dashboard } from "@/lib/dashboards";

const HOME: Dashboard = {
  id: "default", name: "Dashboard", icon: "", placement: "top", position: 0, is_default: true, virtual: true,
  shared: false, hidden: false, can_edit: true, can_manage: true,
};
const TEMPLATE_IDS = ["empty", "standard", "initiatives", "triage", "activity", "agent-sessions", "analytics", "events"];

/** The dialog's last select is «Start from» (the template); it is disabled until the templates load. */
function templateSelect(dialog: HTMLElement): HTMLSelectElement | undefined {
  const all = dialog.querySelectorAll("select");
  const s = all[all.length - 1];
  return s && !s.disabled ? s : undefined;
}

function Where() {
  const l = useLocation();
  return <output data-testid="where">{l.pathname + l.search}</output>;
}

/** The menu's dashboards hook, as the sidebar mounts it on every workspace page. */
function Shell() {
  const { pathname } = useLocation();
  const nav = useDashboardsNav({ workspace: { id: "w1", slug: "acme" }, path: pathname, unseenCount: 0 });
  return <>{nav.dialogs}</>;
}

function mount(at: string) {
  useDashboardsStore.setState({ workspaceId: "w1", items: [HOME], status: "ready", error: null, load: async () => {} });
  useWebSocketStore.setState({ connect: () => {} } as never);
  apiMock.mockReset();
  apiMock.mockImplementation((url: string, opts?: { method?: string; body?: Record<string, unknown> }) => {
    if (url === "/api/v1/workspaces/w1/dashboard-templates") return Promise.resolve(TEMPLATE_IDS.map((id) => ({ id, widgets: [] })));
    if (url === "/api/v1/workspaces/w1/dashboards" && opts?.method === "POST") return Promise.resolve({ ...HOME, id: "d-new", virtual: false, is_default: false, name: opts.body!.name });
    return new Promise(() => {});
  });
  return render(
    <MemoryRouter initialEntries={[at]}>
      <Routes>
        {(Object.keys(LEGACY_PAGE_TEMPLATES) as LegacyPage[]).map((page) => (
          <Route key={page} path={`/w/:wsSlug/${page}`} element={<LegacyPageRedirect page={page} />} />
        ))}
        <Route path="/w/:wsSlug/*" element={<Shell />} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe("old page addresses open «New dashboard» with the page's template (§5.9)", () => {
  const cases: Array<[LegacyPage, string, string]> = [
    ["initiatives", "initiatives", "Initiatives"],
    ["triage", "triage", "Triage"],
    ["activity", "activity", "Activity"],
    ["sessions", "agent-sessions", "Agent sessions"],
    ["analytics", "analytics", "Analytics"],
    ["events", "events", "Events"],
  ];

  it.each(cases)("/w/acme/%s → dialog with template %s; Create posts it and opens the new dashboard", async (page, template, name) => {
    mount(`/w/acme/${page}`);
    const dialog = (await screen.findByRole("heading", { name: "New dashboard" })).closest("div.rounded-xl") as HTMLElement;
    // The parameter is dropped once the dialog is open: a reload or «Back» does not open it again.
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/w/acme/dashboard"));
    const select = await waitFor(() => {
      const s = templateSelect(dialog);
      expect(s?.value).toBe(template);
      return s!;
    });
    expect(select.selectedOptions[0]!.textContent).toBe(name);
    expect(dialog.querySelector("input")!.value).toBe(name); // the template's name fills the name field
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith("/api/v1/workspaces/w1/dashboards", {
        method: "POST",
        body: { name, icon: "", placement: "bottom", shared: false, template_id: template },
      }),
    );
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/w/acme/d/d-new"));
  });

  it("a template the server does not offer falls back to «Empty», not a create that fails", async () => {
    mount("/w/acme/dashboard?new-dashboard=nope");
    const dialog = (await screen.findByRole("heading", { name: "New dashboard" })).closest("div.rounded-xl") as HTMLElement;
    await waitFor(() => expect(templateSelect(dialog)?.value).toBe(""));
  });

  it("a plain dashboard address opens no dialog", async () => {
    mount("/w/acme/dashboard");
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/w/acme/dashboard"));
    expect(screen.queryByRole("heading", { name: "New dashboard" })).toBeNull();
  });
});
