import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useMemo, useState } from "react";
import { Toaster } from "@/components/ui/toast";
import { DashboardEditor } from "../dashboard-editor";
import { addItem, clampCells, firstFreeSpot, moveInOrder, readingOrder, resizeBy, scaleItems, unscaleItems } from "../model";
import type { DashboardLayout } from "../types";
import { CATALOG, FakeServer, LAYOUT, item } from "./fixtures";

const types = new Map(CATALOG.map((c) => [c.id, c]));

function desktop(width: "wide" | "tablet" | "phone" = "wide") {
  window.matchMedia = ((q: string) => {
    const t = new EventTarget();
    const matches = q.includes("1280") ? width === "wide" : q.includes("768") ? width !== "phone" : false;
    return Object.assign(t, { matches, media: q, addEventListener: t.addEventListener.bind(t), removeEventListener: t.removeEventListener.bind(t), addListener() {}, removeListener() {}, onchange: null }) as unknown as MediaQueryList;
  }) as typeof window.matchMedia;
}

function Harness({ server, onLayout, canEdit = true }: { server: FakeServer; onLayout?: (l: DashboardLayout) => void; canEdit?: boolean }) {
  const [layout, setLayout] = useState<DashboardLayout>({ dashboardId: "d1", version: server.version, items: structuredClone(server.items) });
  const api = useMemo(() => server.api(), [server]);
  return (
    <>
      <Toaster />
      <DashboardEditor
        layout={layout}
        canEdit={canEdit}
        api={api}
        onChange={(l) => { setLayout(l); onLayout?.(l); }}
        renderWidget={(it) => <span>body-{it.id}</span>}
      />
    </>
  );
}

const geometry = (s: FakeServer) => Object.fromEntries(s.items.map((i) => [i.id, [i.x, i.y, i.w, i.h]]));

beforeEach(() => desktop("wide"));
afterEach(cleanup);

describe("layout model", () => {
  it("clamps a size outside min..max to the type's limits and to the grid width", () => {
    const t = types.get("my-tasks");
    expect(clampCells({ w: 20, h: 30 }, t)).toEqual({ w: 12, h: 8 });
    expect(clampCells({ w: 1, h: 1 }, t)).toEqual({ w: 3, h: 2 });
    expect(clampCells({ w: 9, h: 3 }, t, 6)).toEqual({ w: 6, h: 3 });
  });

  it("keyboard resize beyond max stays at max, below min stays at min", () => {
    const grown = resizeBy(LAYOUT.items, "c", 10, 10, types);
    expect(grown.find((i) => i.id === "c")).toMatchObject({ w: 6, h: 2 }); // agents-summary max 6x2
    const shrunk = resizeBy(LAYOUT.items, "a", -10, -10, types);
    expect(shrunk.find((i) => i.id === "a")).toMatchObject({ w: 3, h: 2 }); // my-tasks min 3x2
  });

  it("places a new widget in the first free spot, scanning rows then columns", () => {
    expect(firstFreeSpot(LAYOUT.items, { w: 6, h: 4 })).toEqual({ x: 6, y: 2 }); // right of row one's 4x2, below it
    const { added } = addItem(LAYOUT.items, types.get("agents-summary")!);
    expect({ x: added.x, y: added.y, w: added.w, h: added.h }).toEqual({ x: 6, y: 2, w: 4, h: 2 }); // 4x2 does not fit beside c at x=10
  });

  it("a type unknown to the catalog keeps its size and is never dropped", () => {
    expect(clampCells({ w: 5, h: 5 }, undefined)).toEqual({ w: 5, h: 5 });
  });

  it("phone reorder swaps reading order and keeps every size", () => {
    const next = moveInOrder(LAYOUT.items, "b", -1);
    expect(readingOrder(LAYOUT.items).map((i) => i.id)).toEqual(["a", "c", "b"]);
    const order = readingOrder(next).map((i) => i.id);
    expect(order).toEqual(["a", "b", "c"]); // b swapped with its predecessor c
    for (const i of next) expect(i).toMatchObject({ w: LAYOUT.items.find((o) => o.id === i.id)!.w, h: LAYOUT.items.find((o) => o.id === i.id)!.h });
  });

  it("tablet view round-trips untouched items exactly", () => {
    const odd = [item("x", "mentions", 1, 0, 5, 4), item("y", "mentions", 6, 0, 5, 4)];
    const view = scaleItems(odd, 12, 6);
    expect(unscaleItems(view, odd, 6, 12)).toEqual(odd);
  });
});

describe("edit mode", () => {
  it("keyboard drag moves a widget and saves it with the server version", async () => {
    const s = new FakeServer();
    render(<Harness server={s} />);
    const handle = (await screen.findAllByTestId("dash-drag-handle"))[2]!; // c: agents summary at x=6,y=0
    handle.focus();
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    await waitFor(() => expect(s.saves.length).toBe(1));
    expect(s.saves[0]!.version).toBe(8);
    // c moved one column right; nothing overlaps
    expect(geometry(s).c).toEqual([7, 0, 4, 2]);
    const rects = s.items;
    for (const p of rects) for (const q of rects) if (p !== q) expect(p.x < q.x + q.w && p.x + p.w > q.x && p.y < q.y + q.h && p.y + p.h > q.y).toBe(false);
  });

  it("Shift+arrow resizes within min..max and never beyond", async () => {
    const s = new FakeServer();
    render(<Harness server={s} />);
    const handle = (await screen.findAllByTestId("dash-drag-handle"))[2]!;
    for (let n = 0; n < 6; n++) fireEvent.keyDown(handle, { key: "ArrowRight", shiftKey: true });
    await waitFor(() => expect(s.items.find((i) => i.id === "c")!.w).toBe(6)); // max 6, it started at 4
    expect(s.items.find((i) => i.id === "c")!.w).toBeLessThanOrEqual(6);
  });

  it("adds a widget from the catalog into the first free spot", async () => {
    const s = new FakeServer();
    render(<Harness server={s} />);
    fireEvent.click(await screen.findByRole("button", { name: "Add widget" }));
    const dialog = document.body;
    // grouped by category / source, with description and source badge
    expect(await within(dialog).findByText("Tasks")).toBeInTheDocument();
    expect(within(dialog).getByText("GitLab", { selector: "h3" })).toBeInTheDocument();
    expect(within(dialog).getByText("Tasks assigned to me")).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Search widgets"), { target: { value: "pipel" } });
    expect(within(dialog).queryByText("Tasks assigned to me")).toBeNull();
    fireEvent.click(within(dialog).getByText("Failed pipelines"));
    await waitFor(() => expect(s.saves.length).toBe(1));
    const added = s.items.find((i) => i.type === "gitlab-pipelines")!;
    expect(added).toMatchObject({ w: 6, h: 3 });
    expect({ x: added.x, y: added.y }).toEqual({ x: 6, y: 2 });
    expect(s.items.length).toBe(4);
  });

  it("removes a widget and brings it back with Undo within the toast", async () => {
    const s = new FakeServer();
    render(<Harness server={s} />);
    fireEvent.click(await screen.findByRole("button", { name: /Remove: My tasks/ }));
    await waitFor(() => expect(s.items.map((i) => i.id)).not.toContain("a"));
    const undo = await screen.findByRole("button", { name: "Undo" });
    fireEvent.click(undo);
    await waitFor(() => expect(s.items.map((i) => i.id)).toContain("a"));
    expect(s.items.find((i) => i.id === "a")).toMatchObject({ x: 0, y: 0, w: 6, h: 4 });
  });

  it("Cancel changes returns to the state at entering the mode", async () => {
    const s = new FakeServer();
    const before = structuredClone(s.items);
    render(<Harness server={s} />);
    const cancel = await screen.findByRole("button", { name: /Cancel changes/ });
    expect(cancel).toBeDisabled();
    fireEvent.click(await screen.findByRole("button", { name: /Remove: My tasks/ }));
    fireEvent.keyDown((await screen.findAllByTestId("dash-drag-handle"))[1]!, { key: "ArrowRight" });
    await waitFor(() => expect(cancel).toBeEnabled());
    await act(async () => fireEvent.click(cancel));
    await waitFor(() => expect(s.items).toEqual(before));
    expect(cancel).toBeDisabled();
  });

  it("settings: SchemaForm of the type plus Title and Refresh automatically", async () => {
    const s = new FakeServer();
    render(<Harness server={s} />);
    fireEvent.click(await screen.findByRole("button", { name: /Settings: My tasks/ }));
    const title = await screen.findByLabelText(/Title/);
    fireEvent.change(title, { target: { value: "Mine" } });
    expect(screen.getByLabelText(/Refresh automatically/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Rows/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(s.items.find((i) => i.id === "a")!.title).toBe("Mine"));
  });

  it("a failed save rolls the screen back and says so", async () => {
    const s = new FakeServer();
    render(<Harness server={s} />);
    s.failNext = true;
    fireEvent.click(await screen.findByRole("button", { name: /Remove: My tasks/ }));
    expect(await screen.findByText(/previous layout was restored/)).toBeInTheDocument();
    expect(await screen.findByText("body-a")).toBeInTheDocument();
    expect(s.items.length).toBe(3);
  });

  it("two tabs on one layout: the second gets the conflict message, the first one's data survives", async () => {
    const s = new FakeServer();
    // tab 1 and tab 2 both opened at version 7
    render(<div data-testid="tab1"><Harness server={s} /></div>);
    render(<div data-testid="tab2"><Harness server={s} /></div>);
    const tab1 = screen.getByTestId("tab1");
    const tab2 = screen.getByTestId("tab2");
    fireEvent.click(await within(tab1).findByRole("button", { name: /Remove: My tasks/ }));
    await waitFor(() => expect(s.version).toBe(8));
    const afterTab1 = structuredClone(s.items);
    fireEvent.click(await within(tab2).findByRole("button", { name: /Remove: Mentions/ }));
    expect(await within(tab2).findByText("The dashboard was changed in another tab or by another member")).toBeInTheDocument();
    // tab 2's save was refused: the server still holds tab 1's work
    expect(s.items).toEqual(afterTab1);
    expect(s.version).toBe(8);
    // further actions in tab 2 do not overwrite either
    fireEvent.keyDown((await within(tab2).findAllByTestId("dash-drag-handle"))[0]!, { key: "ArrowDown" });
    expect(s.version).toBe(8);
    // Refresh adopts the server copy and editing works again at the new version
    fireEvent.click(within(tab2).getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(within(tab2).queryByRole("alert")).toBeNull());
    expect(within(tab2).queryByText("body-a")).toBeNull();
    fireEvent.click(await within(tab2).findByRole("button", { name: /Remove: Mentions/ }));
    await waitFor(() => expect(s.version).toBe(9));
  });

  it("empty dashboard shows the call to add the first widget", async () => {
    const s = new FakeServer({ dashboardId: "d1", version: 1, items: [] });
    render(<Harness server={s} />);
    expect(await screen.findByText("Add your first widget")).toBeInTheDocument();
  });

  it("catalog failure shows an error with Retry, not an empty list", async () => {
    const s = new FakeServer();
    s.catalogFails = true;
    render(<Harness server={s} />);
    fireEvent.click(await screen.findAllByRole("button", { name: "Add widget" }).then((b) => b[0]!));
    expect(await screen.findByText("Could not load the widget catalog")).toBeInTheDocument();
    s.catalogFails = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Tasks")).toBeInTheDocument();
  });

  it("renders nothing without the right to edit", () => {
    render(<Harness server={new FakeServer()} canEdit={false} />);
    expect(screen.queryByTestId("dash-editor")).toBeNull();
  });
});

describe("phone", () => {
  it("offers only up, down, settings, remove - no drag handle, no resize", async () => {
    desktop("phone");
    const s = new FakeServer();
    render(<Harness server={s} />);
    await screen.findByTestId("dash-edit-phone");
    expect(screen.queryByTestId("dash-drag-handle")).toBeNull();
    expect(screen.getByRole("button", { name: /Move up: My tasks/ })).toBeDisabled(); // first item
    fireEvent.click(screen.getByRole("button", { name: /Move down: My tasks/ }));
    await waitFor(() => expect(s.saves.length).toBe(1));
    const order = [...s.items].sort((p, q) => p.y - q.y || p.x - q.x).map((i) => i.id);
    expect(order).toEqual(["c", "a", "b"]); // reading order was a, c, b: "down" on a swaps it with c
    expect(screen.getAllByRole("button", { name: /Settings:/ }).length).toBe(3);
    expect(screen.getAllByRole("button", { name: /Remove:/ }).length).toBe(3);
  });
});

void vi;

describe("tablet grid limits", () => {
  it("a resize in the 6-column view never saves a width above the type's max (review repro)", async () => {
    const { scaleTypes, scaleItems, unscaleItems, resizeBy } = await import("../model");
    const original = [item("x", "agents-summary", 0, 0, 6, 2)]; // max 6 of 12 columns
    const vt = scaleTypes(types, 12, 6);
    for (const dx of [1, 2, 3, 6]) {
      const view = resizeBy(scaleItems(original, 12, 6), "x", dx, 0, vt, 6);
      const saved = unscaleItems(view, original, 6, 12, types);
      expect(saved[0]!.w, `dx=${dx}`).toBeLessThanOrEqual(6);
      expect(saved[0]!.w).toBeGreaterThanOrEqual(3);
    }
  });
});
