// Backend-free harness for the «Память Mesh» widget: the real MemoryUsageView with real stores, so the
// widget's own request path (useCachedGet → /api/v1/workspaces/<id>/analytics/memory) runs. The API is
// answered at the network boundary by the spec's page.route; the fixture data lives there, not here.
// Russian: the AC reads the ru copy («нет данных»), so the harness boots the ru locale the product ships.
import { createRoot } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router";
import { setLanguage } from "@/i18n";
import { useAgentStore } from "@/stores/agent";
import { useAuthStore } from "@/stores/auth";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { MemoryUsageView } from "../src/core/dashboard/widgets/analytics/memory-view";
import "@/i18n";
import "../src/index.css";

void setLanguage("ru");

useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
useAuthStore.setState({ user: { id: "owner", name: "Owner" } as never, isAuthenticated: true });
useProjectStore.setState({ projects: [
  { id: "p1", slug: "core", name: "Core" } as never,
  { id: "p2", slug: "mesh", name: "Mesh" } as never,
] });
useAgentStore.setState({ agents: [
  { id: "a1", name: "Liam" } as never,
  { id: "a2", name: "Maya" } as never,
] });

// `?frame=<rows>&width=<px>`: the widget inside a dashboard cell of that many grid rows (rowHeight 72, margin 16,
// as edit/grid.tsx) that scrolls on its own, the way the dashboard frames it.
const q = new URLSearchParams(location.search);
const frameRows = Number(q.get("frame")) || 0;
const frameWidth = Number(q.get("width")) || undefined;

function Page() {
  const h = frameRows || 8;
  const view = <MemoryUsageView instance={{ id: "w1", type: "memory-usage", x: 0, y: 0, w: 12, h, settings: {} }} settings={{}} size={{ w: 12, h }} actions={{}} />;
  return (
    <div className="p-4">
      <div className="mx-auto max-w-[1360px]" style={frameWidth ? { width: frameWidth } : undefined}>
        <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
          <Routes>
            <Route
              path="/w/:wsSlug/dashboard"
              element={
                frameRows ? (
                  <div data-testid="harness-frame" className="overflow-y-auto" style={{ height: frameRows * 72 + (frameRows - 1) * 16 }}>
                    {view}
                  </div>
                ) : (
                  view
                )
              }
            />
          </Routes>
        </MemoryRouter>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<Page />);
