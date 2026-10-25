import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn().mockResolvedValue({}),
  getAccessToken: vi.fn(() => null),
}));

const items = vi.hoisted(() => ({ current: [] as Array<{ extensionId: string; title: string }> }));
vi.mock("@/lib/extension-slots", async (orig) => ({
  ...(await orig<typeof import("@/lib/extension-slots")>()),
  useSidebarItems: () => items.current,
}));

import { Header } from "@/components/layout/header";

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
  });

  it("shows the strip on /updates", () => {
    renderAt("/w/ws/p/proj/updates");
    expect(document.querySelector(".mesh-project-tabs")).not.toBeNull();
  });

  it("shows the strip on /updates/ with a trailing slash", () => {
    renderAt("/w/ws/p/proj/updates/");
    expect(document.querySelector(".mesh-project-tabs")).not.toBeNull();
  });

  it("adds a tab for an enabled extension and keeps the strip on its screen", () => {
    items.current = [{ extensionId: "acme", title: "Acme board" }];
    renderAt("/w/ws/p/proj/x/acme");
    expect(document.querySelector(".mesh-project-tabs")).not.toBeNull();
    expect(screen.getAllByText("Acme board").length).toBeGreaterThan(0);
  });

  it("has no extension tab when nothing is enabled", () => {
    renderAt("/w/ws/p/proj/list");
    expect(screen.queryByText("Acme board")).toBeNull();
  });

  it("marks the extension tab active when its id matches a built-in suffix", () => {
    items.current = [{ extensionId: "calendar", title: "Acme calendar" }];
    renderAt("/w/ws/p/proj/x/calendar");
    const current = document.querySelector("[aria-current=page]");
    expect(current?.textContent).toContain("Acme calendar");
  });

  it("survives a corrupt stored split", () => {
    localStorage.setItem(
      "mesh.projectTabs.split",
      JSON.stringify({ sig: "board,list,timeline,calendar,docs,updates", w: window.innerWidth, ids: null }),
    );
    expect(() => renderAt("/w/ws/p/proj/list")).not.toThrow();
  });
});
