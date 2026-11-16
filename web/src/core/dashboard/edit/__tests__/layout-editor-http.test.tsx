// The same session through the real transport (realApi) on the HTTP boundary: only @/lib/api is replaced, by a
// server that speaks the PRD 6.2 wire contract and, like the backend, gives a new id to a widget it does not know.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

interface W { id?: string; widget_type: string; x: number; y: number; w: number; h: number; settings?: Record<string, unknown> }
const server = vi.hoisted(() => ({ version: 1, widgets: [] as W[], seq: 0, puts: [] as W[][] }));
vi.mock("@/lib/api", () => ({
  api: vi.fn(async (url: string, opts?: { method?: string; body?: { version: number; widgets: W[] } }) => {
    // every dashboards route lives under the workspace (cmd/api/routes_dashboards.go); anything else is a 404
    const doc = "/api/v1/workspaces/ws1/dashboards/d1";
    if (url !== doc && url !== `${doc}/layout`) throw Object.assign(new Error("not found"), { status: 404 });
    if (opts?.method === "PUT") {
      const known = new Set(server.widgets.map((w) => w.id));
      server.puts.push(structuredClone(opts.body!.widgets));
      server.version += 1;
      server.widgets = opts.body!.widgets.map((w) => (w.id && known.has(w.id) ? w : { ...w, id: `srv-${++server.seq}` }));
      return { version: server.version, widgets: structuredClone(server.widgets) };
    }
    return { version: server.version, widgets: structuredClone(server.widgets) };
  }),
}));
import { useWorkspaceStore } from "@/stores/workspace";
import { realApi } from "../api";
import { useLayoutEditor } from "../use-layout-editor";
import { CATALOG } from "./fixtures";

const base: W[] = [
  { id: "a", widget_type: "my-tasks", x: 0, y: 0, w: 6, h: 4 },
  { id: "b", widget_type: "mentions", x: 6, y: 0, w: 6, h: 4 },
];

beforeEach(() => {
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "w", name: "W" } as never });
  server.version = 1;
  server.widgets = structuredClone(base);
  server.seq = 0;
  server.puts = [];
});

async function open() {
  const layout = await realApi.load("d1");
  return renderHook(() => useLayoutEditor(layout, realApi, () => {}));
}

describe("edit session over the wire contract", () => {
  it("delete, Undo, then another edit: the instance exists once and keeps the id the server gave it", async () => {
    const { result } = await open();
    let undo!: () => void;
    act(() => {
      undo = result.current.remove("b");
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    act(() => undo());
    await waitFor(() => expect(server.puts.length).toBe(2));
    await waitFor(() => expect(result.current.status).toBe("idle"));
    const restored = result.current.items.find((i) => i.type === "mentions")!;
    expect(restored.id).toBe("srv-1");
    expect(restored.fresh).toBeUndefined();
    act(() => result.current.apply(result.current.items.map((i) => (i.id === "srv-1" ? { ...i, x: 0, y: 4 } : i))));
    await waitFor(() => expect(server.puts.length).toBe(3));
    expect(server.puts[2]!.every((w) => w.id !== undefined)).toBe(true);
    expect(server.widgets.map((w) => w.id).sort()).toEqual(["a", "srv-1"]);
    expect(server.widgets.length).toBe(2);
  });

  it("add, then Cancel changes: no widget is left behind and none is duplicated", async () => {
    const { result } = await open();
    act(() => {
      result.current.add(CATALOG.find((c) => c.id === "my-tasks")!);
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    await act(async () => {
      await result.current.cancelChanges();
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(server.widgets.map((w) => w.id).sort()).toEqual(["a", "b"]);
  });
});
