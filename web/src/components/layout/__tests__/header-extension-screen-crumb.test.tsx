import { render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router";

// The workspace's extensions as the server answers them: Keep fills `sidebar.item` with an iframe screen.
const server = vi.hoisted(() => ({ extensions: [] as unknown[], hold: null as Promise<void> | null }));

vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: vi.fn(async (url: string) => {
      if (url.endsWith("/extensions")) {
        if (server.hold) await server.hold;
        return { extensions: server.extensions };
      }
      return url.includes("/dashboards") ? [] : {};
    }),
    getAccessToken: vi.fn(() => null),
  };
});

import { Header } from "@/components/layout/header";
import { setLanguage } from "@/i18n";
import { resetSlotRegistryForTests } from "@/lib/extension-slots";
import { useProjectStore } from "@/stores/project";
import { useWebSocketStore } from "@/stores/websocket";
import { useWorkspaceStore } from "@/stores/workspace";

const WS = { id: "ws-1", slug: "ws", name: "Entire VC" };

const KEEP = {
  id: "keep",
  name: "keep-ext",
  manifest: {
    runtime: "external",
    capabilities: { ui: ["sidebar.item"] },
    external: { ui: { "sidebar.item": { path: "/ui", title: "Keep" } } },
  },
  status: { enabled: true },
};

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="w/:wsSlug/*" element={<Header onToggleSidebar={() => {}} />} />
      </Routes>
    </MemoryRouter>,
  );
}

const crumbs = () => within(screen.getByRole("navigation", { name: /breadcrumbs/i }));
const lastCrumb = () => crumbs().getByText((_, el) => el?.getAttribute("aria-current") === "page");

describe("Header crumb on an extension screen (/x/<ext>)", () => {
  beforeEach(async () => {
    server.extensions = [KEEP];
    server.hold = null;
    resetSlotRegistryForTests();
    useWebSocketStore.setState({ lastEvent: null, connect: vi.fn() } as never);
    useWorkspaceStore.setState({ currentWorkspace: WS as never });
    useProjectStore.setState({ currentProject: null });
    await setLanguage("en");
  });

  it.each(["/w/ws/x/keep", "/w/ws/x/keep/memory"])("%s reads «Entire VC › Keep»", async (path) => {
    renderAt(path);
    await waitFor(() => expect(lastCrumb()).toHaveTextContent(/^Keep$/));
    expect(crumbs().getByText("Entire VC")).toBeInTheDocument();
    expect(lastCrumb().querySelector("svg")).not.toBeNull();
  });

  it("names nothing before the registry answers, and an unknown extension stays unnamed", async () => {
    let release!: () => void;
    server.hold = new Promise<void>((r) => (release = r));
    renderAt("/w/ws/x/keep");
    expect(crumbs().queryByText("Keep")).toBeNull();
    expect(crumbs().queryByText("keep")).toBeNull();
    release();
    await waitFor(() => expect(lastCrumb()).toHaveTextContent(/^Keep$/));
  });

  it("an extension that is not in the registry gets no made-up crumb", async () => {
    renderAt("/w/ws/x/ghost");
    await waitFor(() => expect(crumbs().getByText("Entire VC")).toBeInTheDocument());
    // Let the registry answer, then check nothing was invented from the slug.
    await new Promise((r) => setTimeout(r, 20));
    expect(crumbs().queryByText(/ghost/i)).toBeNull();
  });

  it("built-in /memories keeps «Memory»", async () => {
    renderAt("/w/ws/memories");
    await waitFor(() => expect(lastCrumb()).toHaveTextContent(/^Memory$/));
    expect(crumbs().queryByText("Keep")).toBeNull();
  });

  it("built-in /settings keeps the workspace as the only crumb", async () => {
    renderAt("/w/ws/settings");
    await new Promise((r) => setTimeout(r, 20));
    expect(crumbs().getByText("Entire VC")).toBeInTheDocument();
    expect(crumbs().queryByText("Keep")).toBeNull();
  });
});
