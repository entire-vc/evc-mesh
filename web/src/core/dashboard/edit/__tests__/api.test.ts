// The HTTP boundary (PRD §6.2): the only mock is the network function, so URLs and bodies are asserted as sent.
import { beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { realApi, toWire, fromWire, layoutFromDoc, workspaceApi } from "../api";
import { StaleLayoutError, type LayoutItem } from "../types";

const a: LayoutItem = { id: "w1", type: "my-tasks", x: 0, y: 0, w: 6, h: 4, title: "Mine", auto_refresh: false, settings: { limit: 10 } };

beforeEach(() => {
  vi.mocked(api).mockReset();
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "w", name: "W" } as never });
});

describe("edit mode HTTP contract", () => {
  it("reads the catalog from the workspace-scoped route and maps widget_type to id", async () => {
    vi.mocked(api).mockResolvedValue({ widgets: [{ widget_type: "gitlab.failed-pipelines", title: "T", description: "", category: "CI", source: { kind: "extension", name: "GitLab" }, size: { default: { w: 6, h: 3 }, min: { w: 3, h: 2 }, max: { w: 12, h: 6 } } }] });
    const c = await realApi.catalog();
    expect(vi.mocked(api).mock.calls[0]![0]).toBe("/api/v1/workspaces/ws1/dashboard-widgets");
    expect(c[0]!.id).toBe("gitlab.failed-pipelines");
  });

  it("reads a dashboard from GET /workspaces/:ws/dashboards/:id", async () => {
    vi.mocked(api).mockResolvedValue({ version: 3, widgets: [{ id: "w1", widget_type: "my-tasks", x: 0, y: 0, w: 6, h: 4, settings: { title: "Mine", auto_refresh: false, limit: 10 } }] });
    const l = await realApi.load("d1");
    expect(vi.mocked(api).mock.calls[0]![0]).toBe("/api/v1/workspaces/ws1/dashboards/d1");
    expect(l).toEqual({ dashboardId: "d1", version: 3, items: [a] });
  });

  it("saves with PUT /workspaces/:ws/dashboards/:id/layout {version, widgets[widget_type]}; a new widget goes without an id", async () => {
    vi.mocked(api).mockResolvedValue({ version: 4, widgets: [{ id: "srv", widget_type: "mentions", x: 0, y: 4, w: 6, h: 4 }] });
    const fresh: LayoutItem = { id: "client-uuid", type: "mentions", x: 0, y: 4, w: 6, h: 4, fresh: true };
    const r = await realApi.save("d1", 3, [a, fresh]);
    const [url, opts] = vi.mocked(api).mock.calls[0]!;
    expect(url).toBe("/api/v1/workspaces/ws1/dashboards/d1/layout");
    expect(opts).toMatchObject({ method: "PUT" });
    const body = (opts as { body: { version: number; widgets: unknown[] } }).body;
    expect(body.version).toBe(3);
    expect(body.widgets).toEqual([
      { id: "w1", widget_type: "my-tasks", x: 0, y: 0, w: 6, h: 4, settings: { limit: 10, title: "Mine", auto_refresh: false } },
      { widget_type: "mentions", x: 0, y: 4, w: 6, h: 4, settings: {} },
    ]);
    expect(r.version).toBe(4);
    expect(r.items![0]!.id).toBe("srv"); // the server's id replaces the client one
  });

  it("a session's transport keeps its workspace when the current one changes", async () => {
    vi.mocked(api).mockResolvedValue({ version: 2, widgets: [] });
    const session = workspaceApi("ws1");
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws2", slug: "x", name: "X" } as never });
    await session.save("d1", 1, [a]);
    await session.catalog();
    expect(vi.mocked(api).mock.calls.map(([u]) => u)).toEqual(["/api/v1/workspaces/ws1/dashboards/d1/layout", "/api/v1/workspaces/ws1/dashboard-widgets"]);
  });

  it("409 becomes StaleLayoutError", async () => {
    vi.mocked(api).mockRejectedValue(Object.assign(new Error("conflict"), { status: 409, code: "stale_layout" }));
    await expect(realApi.save("d1", 1, [a])).rejects.toBeInstanceOf(StaleLayoutError);
  });

  it("another failure is not mistaken for a conflict", async () => {
    vi.mocked(api).mockRejectedValue(Object.assign(new Error("boom"), { status: 500 }));
    await expect(realApi.save("d1", 1, [a])).rejects.not.toBeInstanceOf(StaleLayoutError);
  });

  it("completes a core entry of the server catalog from the client registry (contract C)", async () => {
    // the server's own shape (catalog.go WidgetType): no title, description or source for a core type
    vi.mocked(api).mockResolvedValue({
      widgets: [
        { id: "initiatives", origin: "core", category: "initiatives", size: { default: { w: 6, h: 4 }, min: { w: 3, h: 3 }, max: { w: 12, h: 8 } }, settings: { type: "object", properties: {} } },
        { id: "gitlab.failed-pipelines", origin: "extension", category: "ci", title: "Failed pipelines", source: { kind: "extension", name: "GitLab" }, size: { default: { w: 6, h: 3 }, min: { w: 3, h: 2 }, max: { w: 12, h: 6 } } },
        { id: "theme.clock", origin: "theme", category: "theme", theme_id: "studio", size: { default: { w: 3, h: 2 }, min: { w: 2, h: 2 }, max: { w: 6, h: 4 } } },
      ],
    });
    const [core, ext, theme] = await realApi.catalog();
    expect(core).toMatchObject({ id: "initiatives", title: "Initiatives", category: "initiatives", source: { kind: "core", name: "Mesh" }, settings_schema: { type: "object" } });
    expect(core!.description).not.toBe("");
    expect(ext).toMatchObject({ id: "gitlab.failed-pipelines", title: "Failed pipelines", source: { kind: "extension", name: "GitLab" } });
    expect(theme).toMatchObject({ id: "theme.clock", title: "theme.clock", source: { kind: "theme", name: "studio" } });
  });

  it("a core widget's settings form is the client's UI schema, not the server's validation schema", async () => {
    // catalog.go: my-tasks carries limitSchema (bounds of `limit` only); the form fields live in the registry
    const limitSchema = { type: "object", properties: { limit: { type: "integer", minimum: 5, maximum: 50 } } };
    vi.mocked(api).mockResolvedValue({
      widgets: [
        { id: "my-tasks", origin: "core", category: "tasks", size: { default: { w: 6, h: 4 }, min: { w: 3, h: 2 }, max: { w: 12, h: 8 } }, settings: limitSchema },
        { id: "gitlab.failed-pipelines", origin: "extension", category: "ci", title: "Failed pipelines", source: { kind: "extension", name: "GitLab" }, size: { default: { w: 6, h: 3 }, min: { w: 3, h: 2 }, max: { w: 12, h: 6 } }, settings: limitSchema },
        { id: "core.unknown", origin: "core", category: "tasks", size: { default: { w: 6, h: 4 }, min: { w: 3, h: 2 }, max: { w: 12, h: 8 } }, settings: limitSchema },
      ],
    });
    const [mine, ext, unknown] = await realApi.catalog();
    const props = (mine!.settings_schema as { properties: Record<string, Record<string, unknown>> }).properties;
    expect(props.limit).toMatchObject({ title: "Limit", default: 10 });
    expect(props).toHaveProperty("statuses");
    expect(props).toHaveProperty("projectId");
    // an extension's schema is the server's; a core id the client does not know falls back to it
    expect(ext!.settings_schema).toEqual(limitSchema);
    expect(unknown!.settings_schema).toEqual(limitSchema);
  });

  it("an instance of the virtual default (no server id) is saved as a new one", () => {
    const l = layoutFromDoc({ id: "default", version: 0, widgets: [{ id: "~0", widget_type: "my-tasks", x: 0, y: 0, w: 6, h: 4 }, { id: "w9", widget_type: "mentions", x: 6, y: 0, w: 6, h: 4 }] });
    expect(l.items.map((i) => [i.id, !!i.fresh])).toEqual([["~0", true], ["w9", false]]);
    expect(toWire(l.items[0]!)).not.toHaveProperty("id");
  });

  it("wire round trip keeps title, refresh flag and settings", () => {
    expect(fromWire(toWire(a))).toEqual({ ...a });
  });
});
