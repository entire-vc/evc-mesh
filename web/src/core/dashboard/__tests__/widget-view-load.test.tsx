import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { DashboardContainer, DashboardThemeProvider } from "..";

// The network is mocked, and so is the other network edge under test: the fetch of a group's views.tsx chunk. Here
// my-tasks is a widget of a lazy group (widgets/tasks); its views file fails on the first fetch and arrives on the
// retry. The view cache itself is the real one.
vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: () => null }));
import { api } from "@/lib/api";
const fetches = vi.hoisted(() => ({ n: 0 }));
vi.mock("../registry", async (importActual) => {
  const actual = await importActual<typeof import("../registry")>();
  const { views } = await import("../widgets/tasks/views");
  const cache = actual.createViewCache({
    descriptors: [...actual.WIDGETS],
    catalogOnly: new Set(),
    viewLoaders: {
      "my-tasks": async () => {
        fetches.n++;
        if (fetches.n === 1) throw new Error("Failed to fetch dynamically imported module");
        return { views };
      },
    },
  });
  return { ...actual, peekWidgetView: cache.peek, loadWidgetView: cache.load };
});

function mount() {
  window.matchMedia = ((q: string) => ({ matches: q.includes("768"), media: q, addEventListener() {}, removeEventListener() {} })) as never;
  useMemberStore.setState({ myRole: "member" });
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useProjectStore.setState({ projects: [{ id: "p1", slug: "core", name: "Core" } as never] });
  return render(
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <DashboardThemeProvider theme={{ settings: { widgets: ["my-tasks"] } }}>
        <Routes>
          <Route path="/w/:wsSlug/dashboard" element={<DashboardContainer />} />
        </Routes>
      </DashboardThemeProvider>
    </MemoryRouter>,
  );
}

afterEach(() => cleanup());

describe("a core widget's view loads lazily", () => {
  it("first visit: the place is held while views.tsx loads; a failed load is an error with a retry that loads again", async () => {
    vi.mocked(api).mockImplementation(async (url: string) => (url === "/api/v1/workspaces/ws1/tasks/query" ? { items: [{ id: "t1", title: "Task 1", priority: "high", project_id: "p1", created_at: "2026-01-01T00:00:00Z" }], total_count: 1 } : {}));
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const { container } = mount();
    // Before the views file answers: the widget holds its place, busy, nothing drawn.
    const slot = container.querySelector('[data-mesh-widget="my-tasks"]');
    expect(slot?.getAttribute("aria-busy")).toBe("true");
    expect(slot?.childElementCount).toBe(0);
    // The load fails: an error in the widget's place, not a blank cell and not a crashed dashboard.
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/could not be loaded|не удалось загрузить/i);
    expect(screen.queryByText("Task 1")).toBeNull();
    // Retry loads views.tsx again and the widget draws its data.
    await act(async () => fireEvent.click(screen.getByRole("button", { name: /retry|повторить/i })));
    expect(await screen.findByText("Task 1")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(fetches.n).toBe(2);
    error.mockRestore();
  });
});
