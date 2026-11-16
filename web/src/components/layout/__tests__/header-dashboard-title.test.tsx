import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

// The dashboards list answers 404 (backend not deployed): /w/:ws/dashboard falls back to the legacy page,
// and the header still has to name it.
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: vi.fn(async (url: string) => {
      if (url.endsWith("/dashboards")) throw new real.ApiRequestError("nope", "not_found", 404);
      return {};
    }),
    getAccessToken: vi.fn(() => null),
  };
});

import { Header } from "@/components/layout/header";
import { setLanguage } from "@/i18n";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="w/:wsSlug/*" element={<Header onToggleSidebar={() => {}} />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("Header title on the dashboard routes", () => {
  beforeEach(async () => {
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "ws", name: "Entire VC" } as never });
    useProjectStore.setState({ currentProject: null });
    await setLanguage("en");
  });

  it.each(["/w/ws", "/w/ws/d/d1", "/w/ws/dashboard", "/w/ws/dashboard/"])("%s is titled Dashboard", (path) => {
    renderAt(path);
    expect(screen.getByText("Dashboard")).toBeInTheDocument();
  });

  it("other workspace pages keep their own title", () => {
    renderAt("/w/ws/runner");
    expect(screen.queryByText("Dashboard")).toBeNull();
  });
});
