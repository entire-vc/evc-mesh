// The Mesh core as the host of the theme contract tests (packages/mesh-theme-sdk/testing): the real primitives,
// router and permissions of the page, with the transport (fetch, WebSocket) the only thing replaced.
import { MemoryRouter, Route, Routes } from "react-router";
import { fixtures, type HarnessHost, type ExtensionPoint } from "@entire-vc/mesh-theme-sdk/testing";
import { Can, CoreActionsProvider, DndProvider, Draggable, DropZone, InlineEdit, TaskLink, useRealtime } from "@/core/primitives";
import { SlotContextProvider } from "@/core/slots/slot-context";
import { resetSlotRegistryForTests } from "@/lib/extension-slots";
import { useMemberStore } from "@/stores/member";
import { useWebSocketStore } from "@/stores/websocket";
import { useWorkspaceStore } from "@/stores/workspace";

const WS_ID = "ws-1";

let installed: ExtensionPoint[] = [];

// The extension the core ships a badge/panel/item for. One extension fills every point: what is under test is
// that the view carries the insertion point, not the extension.
const EXT = { id: "gitlab", name: "GitLab" };
const SELECTOR: Record<ExtensionPoint, string> = {
  "task.badge": '[data-testid="slot-badge-gitlab"]',
  "task.panel": '[data-testid="slot-panel-gitlab"], [data-extension-slot="task.panel"]',
  "task.actions": '[data-extension-slot="task.actions"]',
  "sidebar.item": '[data-slot-origin="extension"]',
};

export const meshHost: HarnessHost = {
  sdk: { Can, DndProvider, Draggable, DropZone, InlineEdit, TaskLink, useRealtime },
  Provider({ role, ctx, coreActions, children }) {
    useMemberStore.setState({ myRole: role });
    // A theme's own widget reads SDK hooks, which load per workspace.
    if (ctx.slot.startsWith("dashboard.widget.")) useWorkspaceStore.setState({ currentWorkspace: { id: WS_ID } as never });
    return (
      <MemoryRouter initialEntries={["/ws/proj"]}>
        <Routes>
          <Route
            path="/:wsSlug/:projectSlug"
            element={
              <CoreActionsProvider actions={coreActions as never}>
                <SlotContextProvider value={ctx}>{children}</SlotContextProvider>
              </CoreActionsProvider>
            }
          />
        </Routes>
      </MemoryRouter>
    );
  },
  emitRealtime(e) {
    useWebSocketStore.setState({ lastEvent: { type: e.type, channel: "project:p1", data: e.data, timestamp: e.at } as never });
  },
  installExtension(point) {
    installed = [...installed, point];
    resetSlotRegistryForTests();
    useWorkspaceStore.setState({ currentWorkspace: { id: WS_ID } as never });
  },
  extensionSelector: (p) => SELECTOR[p],
  handleRequest(url, _init, scenario) {
    // The endpoints behind useTasks (mine, triage): ready = two tasks, error = 500, loading = never settles.
    if (url.includes("/api/v1/me/tasks") || url.includes(`/workspaces/${WS_ID}/triage`)) {
      if (scenario?.state === "loading") return new Promise(() => {});
      if (scenario?.state === "error") return new Response("{}", { status: 500, headers: { "content-type": "application/json" } });
      return { items: [fixtures.dashboard.apiTask("t1", { title: "First", priority: "high" }), fixtures.dashboard.apiTask("t2")], total: 2 };
    }
    if (!url.includes(`/workspaces/${WS_ID}/extensions`)) return undefined;
    return { extensions: [{ ...EXT, status: { enabled: true }, manifest: { capabilities: { ui: installed } } }] };
  },
  reset() {
    installed = [];
    resetSlotRegistryForTests();
    useWebSocketStore.setState({ lastEvent: null } as never);
  },
};
