import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(async () => ({})),
  getAccessToken: vi.fn(() => null),
}));

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

describe("Header breadcrumb on the Runner page", () => {
  beforeEach(() => {
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "ws", name: "Entire VC" } as never });
    useProjectStore.setState({ currentProject: null });
  });

  it("names the page Runner after the workspace", async () => {
    await setLanguage("en");
    renderAt("/w/ws/runner");
    expect(screen.getByText("Entire VC")).toBeInTheDocument();
    expect(screen.getByText("Runner")).toBeInTheDocument();
  });

  it("names it Раннер in Russian", async () => {
    await setLanguage("ru");
    renderAt("/w/ws/runner");
    expect(screen.getByText("Раннер")).toBeInTheDocument();
  });
});
