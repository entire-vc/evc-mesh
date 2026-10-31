/**
 * The full-area screen of an external extension: which slot it opens, for which
 * project, at which section of the screen.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";

const slots = vi.hoisted(() => ({
  sidebar: [] as Array<{ extensionId: string; title: string; projectLevel?: boolean }>,
  project: [] as Array<{ extensionId: string; title: string }>,
}));
vi.mock("@/lib/extension-slots", async (orig) => ({
  ...(await orig<typeof import("@/lib/extension-slots")>()),
  useSidebarItems: () => slots.sidebar,
  useProjectScreens: () => slots.project,
}));
// The frame itself is tested on its own; here only what the page hands it.
vi.mock("@/components/extensions/external-frame", () => ({
  ExternalFrame: (p: { slot: string; projectId?: string; section?: string; onSection?: (s: string, r: boolean) => void }) => (
    <button
      type="button"
      data-testid="frame"
      data-slot={p.slot}
      data-project={p.projectId ?? ""}
      data-section={p.section ?? ""}
      onClick={() => p.onSection?.("/models#top", false)}
    />
  ),
}));

import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { ExtensionScreenPage } from "../extension-screen";

function Where() {
  const l = useLocation();
  return <output data-testid="where">{l.pathname + l.hash}</output>;
}

function renderAt(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="w/:wsSlug/x/:extId/*" element={<ExtensionScreenPage />} />
        <Route path="w/:wsSlug/p/:projectSlug/x/:extId/*" element={<ExtensionScreenPage />} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  slots.sidebar = [{ extensionId: "mem", title: "Memory", projectLevel: true }];
  slots.project = [{ extensionId: "mem", title: "Memory" }];
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "acme" } as never });
  useProjectStore.setState({
    projects: [{ id: "proj-1", slug: "core", workspace_id: "ws-1" }] as never,
    currentProject: null,
    loaded: true,
    isLoading: false,
  });
});

describe("ExtensionScreenPage", () => {
  it("opens project.view for the project of the address, at the section", () => {
    renderAt("/w/acme/p/core/x/mem/memory/search#recent");
    const f = screen.getByTestId("frame");
    expect(f.dataset.slot).toBe("project.view");
    expect(f.dataset.project).toBe("proj-1");
    expect(f.dataset.section).toBe("/memory/search#recent");
  });

  it("puts the section the frame reports into the address", () => {
    renderAt("/w/acme/p/core/x/mem");
    expect(screen.getByTestId("frame").dataset.section).toBe("");
    fireEvent.click(screen.getByTestId("frame"));
    expect(screen.getByTestId("where").textContent).toBe("/w/acme/p/core/x/mem/models#top");
    expect(screen.getByTestId("frame").dataset.section).toBe("/models#top");
  });

  it("keeps the workspace screen without a project outside a project", () => {
    renderAt("/w/acme/x/mem/memory");
    const f = screen.getByTestId("frame");
    expect(f.dataset.slot).toBe("sidebar.item");
    expect(f.dataset.project).toBe("");
    expect(f.dataset.section).toBe("/memory");
  });

  it("falls back to the per-project sidebar screen where there is no project.view", () => {
    slots.project = [];
    renderAt("/w/acme/p/core/x/mem/models");
    const f = screen.getByTestId("frame");
    expect(f.dataset.slot).toBe("sidebar.item");
    expect(f.dataset.project).toBe("proj-1");
    expect(f.dataset.section).toBe("/models");
  });

  it("red control: an unknown project or a malformed section opens nothing foreign", () => {
    renderAt("/w/acme/p/nope/x/mem");
    expect(screen.queryByTestId("frame")).toBeNull();
    expect(screen.getByTestId("extension-screen-unavailable")).toBeTruthy();
  });

  it("an unknown project in a workspace without projects is unavailable, not a spinner", () => {
    useProjectStore.setState({ projects: [], loaded: true });
    renderAt("/w/acme/p/nope/x/mem");
    expect(screen.getByTestId("extension-screen-unavailable")).toBeTruthy();
  });

  it("waits for the project list before deciding", () => {
    useProjectStore.setState({ projects: [], loaded: false });
    renderAt("/w/acme/p/core/x/mem");
    expect(screen.queryByTestId("extension-screen-unavailable")).toBeNull();
    expect(screen.queryByTestId("frame")).toBeNull();
  });

  it("red control: after a workspace switch the previous workspace's list is not used", () => {
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-2", slug: "beta" } as never });
    useProjectStore.setState({ isLoading: true });
    renderAt("/w/beta/p/core/x/mem");
    expect(screen.queryByTestId("frame")).toBeNull();
    expect(screen.queryByTestId("extension-screen-unavailable")).toBeNull();
  });

  it("drops a section that is not one", () => {
    renderAt("/w/acme/x/mem/a/%2e%2e/b");
    expect(screen.getByTestId("frame").dataset.section).toBe("");
  });
});
