import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

// Bindings of project p1: extension ids bound (enabled) to it.
const bindings = vi.hoisted(() => ({ ids: [] as string[], fail: false }));
const BINDINGS_PATH = "/api/v1/projects/p1/extensions";
vi.mock("@/lib/api", () => ({
  api: vi.fn(async (path: string) => {
    if (path !== "/api/v1/projects/p1/extensions") return {};
    if (bindings.fail) throw new Error("boom");
    return { extensions: bindings.ids.map((id) => ({ extension_id: id, enabled: true, settings: {} })) };
  }),
  getAccessToken: vi.fn(() => null),
}));

const items = vi.hoisted(() => ({ current: [] as Array<{ extensionId: string; title: string; projectLevel?: boolean }> }));
vi.mock("@/lib/extension-slots", async (orig) => ({
  ...(await orig<typeof import("@/lib/extension-slots")>()),
  useSidebarItems: () => items.current,
}));


import { Header } from "@/components/layout/header";
import { api } from "@/lib/api";
import { resetProjectExtensionTabsForTests } from "@/lib/project-extension-tabs";
import { useProjectStore } from "@/stores/project";

const bindingReads = () => vi.mocked(api).mock.calls.filter(([path]) => path === BINDINGS_PATH).length;

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="w/:wsSlug/p/:projectSlug/*" element={<Header onToggleSidebar={() => {}} />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("Header project strip: Updates and extension tabs", () => {
  beforeEach(() => {
    localStorage.clear();
    items.current = [];
    bindings.ids = [];
    bindings.fail = false;
    vi.mocked(api).mockClear();
    resetProjectExtensionTabsForTests();
    useProjectStore.setState({ currentProject: { id: "p1", slug: "proj", name: "P" } as never });
  });

  it("shows the strip on /updates", () => {
    renderAt("/w/ws/p/proj/updates");
    expect(document.querySelector(".mesh-project-tabs")).not.toBeNull();
  });

  it("shows the strip on /updates/ with a trailing slash", () => {
    renderAt("/w/ws/p/proj/updates/");
    expect(document.querySelector(".mesh-project-tabs")).not.toBeNull();
  });

  it("adds a tab for a project-level extension bound to the project and keeps the strip on its screen", async () => {
    items.current = [{ extensionId: "acme", title: "Acme board", projectLevel: true }];
    bindings.ids = ["acme"];
    renderAt("/w/ws/p/proj/x/acme");
    expect(document.querySelector(".mesh-project-tabs")).not.toBeNull();
    await waitFor(() => expect(screen.getAllByText("Acme board").length).toBeGreaterThan(0));
    expect(bindingReads()).toBeGreaterThan(0);
  });

  it("has no tab for a workspace-level sidebar.item (levels.project=false), even when bound", async () => {
    // Keep: a left-menu screen of the workspace, not a project tab.
    items.current = [{ extensionId: "keep", title: "Secrets" }];
    bindings.ids = ["keep"];
    renderAt("/w/ws/p/proj/list");
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.queryByText("Secrets")).toBeNull();
    // Nothing project-level in the workspace: the project is not even asked.
    expect(bindingReads()).toBe(0);
  });

  it("has no tab for a project-level extension this project did not bind", async () => {
    items.current = [{ extensionId: "acme", title: "Acme board", projectLevel: true }];
    bindings.ids = ["other"];
    renderAt("/w/ws/p/proj/list");
    await waitFor(() => expect(bindingReads()).toBeGreaterThan(0));
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.queryByText("Acme board")).toBeNull();
  });

  it("shows no extension tab when the bindings fail to load", async () => {
    items.current = [{ extensionId: "acme", title: "Acme board", projectLevel: true }];
    bindings.fail = true;
    renderAt("/w/ws/p/proj/list");
    await waitFor(() => expect(bindingReads()).toBeGreaterThan(0));
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.queryByText("Acme board")).toBeNull();
  });

  it("drops the extension tab when a later refresh of the bindings fails", async () => {
    items.current = [{ extensionId: "acme", title: "Acme board", projectLevel: true }];
    bindings.ids = ["acme"];
    const first = renderAt("/w/ws/p/proj/list");
    await waitFor(() => expect(screen.getAllByText("Acme board").length).toBeGreaterThan(0));
    first.unmount();
    // The cached answer is past its TTL and the next read fails.
    bindings.fail = true;
    const now = Date.now();
    const clock = vi.spyOn(Date, "now").mockReturnValue(now + 120_000);
    try {
      renderAt("/w/ws/p/proj/list");
      await waitFor(() => expect(bindingReads()).toBe(2));
      await waitFor(() => expect(screen.queryByText("Acme board")).toBeNull());
    } finally {
      clock.mockRestore();
    }
  });

  it("has no extension tab when nothing is enabled", () => {
    renderAt("/w/ws/p/proj/list");
    expect(screen.queryByText("Acme board")).toBeNull();
  });

  it("marks the extension tab active when its id matches a built-in suffix", async () => {
    items.current = [{ extensionId: "calendar", title: "Acme calendar", projectLevel: true }];
    bindings.ids = ["calendar"];
    renderAt("/w/ws/p/proj/x/calendar");
    await waitFor(() => expect(document.querySelector("[aria-current=page]")?.textContent).toContain("Acme calendar"));
  });

  it("survives a corrupt stored split", () => {
    localStorage.setItem(
      "mesh.projectTabs.split",
      JSON.stringify({ sig: "board,list,timeline,calendar,docs,updates", w: window.innerWidth, ids: null }),
    );
    expect(() => renderAt("/w/ws/p/proj/list")).not.toThrow();
  });
});
