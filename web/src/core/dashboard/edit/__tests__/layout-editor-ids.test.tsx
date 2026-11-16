// Review P1 (b8c9dddd): after the server gave an added widget its id, the editor kept the temporary id and the
// `fresh` mark, so the next save created a second copy. The server here assigns ids like the real one.
import { describe, expect, it } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useLayoutEditor } from "../use-layout-editor";
import { CATALOG, FakeServer, LAYOUT } from "./fixtures";

const tasks = CATALOG.find((c) => c.id === "my-tasks")!;

function setup(s: FakeServer) {
  return renderHook(() => useLayoutEditor(LAYOUT, s.api(), () => {}));
}

describe("ids of widgets added in the session", () => {
  it("after the server answers, the editor holds the server id and a later save keeps it (no duplicate)", async () => {
    const s = new FakeServer();
    s.assignIds = true;
    const { result } = setup(s);
    let temp = "";
    act(() => {
      temp = result.current.add(tasks).id;
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    const mine = result.current.items.find((i) => i.type === "my-tasks" && i.id !== "a")!;
    expect(mine.id).toBe("srv-1");
    expect(mine.fresh).toBeUndefined();
    expect(result.current.items.some((i) => i.id === temp)).toBe(false);

    act(() => result.current.apply(result.current.items.map((i) => (i.id === "srv-1" ? { ...i, title: "Mine" } : i))));
    await waitFor(() => expect(s.saves.length).toBe(2));
    expect(s.saves[1]!.sent.filter((i) => i.fresh).length).toBe(0);
    expect(s.items.filter((i) => i.id === "srv-1").length).toBe(1);
    expect(s.items.length).toBe(4); // 3 original + the added one, never a second copy
    expect(s.items.find((i) => i.id === "srv-1")!.title).toBe("Mine");
  });

  it("a save already queued behind the add is rewritten to the server id, newer edits survive", async () => {
    const s = new FakeServer();
    s.assignIds = true;
    let release!: () => void;
    s.hold = new Promise<void>((r) => (release = r));
    const { result } = setup(s);
    act(() => {
      result.current.add(tasks);
    });
    // second edit while the first save is still in flight: it carries the temporary id and fresh=true
    act(() => result.current.apply(result.current.items.map((i) => (i.fresh ? { ...i, title: "Queued" } : i))));
    s.hold = null;
    release();
    await waitFor(() => expect(s.saves.length).toBe(2));
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(s.saves[1]!.sent.filter((i) => i.fresh).length).toBe(0);
    expect(s.items.length).toBe(4);
    const added = s.items.find((i) => i.type === "my-tasks" && i.id !== "a")!;
    expect(added.id).toBe("srv-1");
    expect(added.title).toBe("Queued");
    expect(result.current.items.find((i) => i.id === "srv-1")!.title).toBe("Queued");
  });
});

describe("ids of widgets restored after a saved delete (review P1, 969f3675)", () => {
  it("Undo after the delete was saved: the server's new id is adopted and the next save does not recreate it", async () => {
    const s = new FakeServer();
    s.assignIds = true;
    const { result } = setup(s);
    let undo!: () => void;
    act(() => {
      undo = result.current.remove("b");
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(s.items.map((i) => i.id)).toEqual(["a", "c"]);
    act(() => undo());
    await waitFor(() => expect(s.saves.length).toBe(2));
    await waitFor(() => expect(result.current.status).toBe("idle"));
    const back = s.items.find((i) => i.type === LAYOUT.items.find((o) => o.id === "b")!.type && i.id !== "a" && i.id !== "c")!;
    expect(back.id).toMatch(/^srv-/);
    expect(result.current.items.map((i) => i.id).sort()).toEqual(s.items.map((i) => i.id).sort());
    expect(result.current.items.some((i) => i.fresh)).toBe(false);

    act(() => result.current.apply(result.current.items.map((i) => (i.id === back.id ? { ...i, title: "again" } : i))));
    await waitFor(() => expect(s.saves.length).toBe(3));
    expect(s.saves[2]!.sent.filter((i) => i.fresh).length).toBe(0);
    expect(s.items.length).toBe(3); // a, c and the restored one, never a second copy
  });

  it("Cancel changes after a saved delete restores the entry state without duplicating", async () => {
    const s = new FakeServer();
    s.assignIds = true;
    const { result } = setup(s);
    act(() => {
      result.current.remove("b");
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    await act(async () => {
      await result.current.cancelChanges();
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(s.items.length).toBe(3);
    expect(result.current.items.map((i) => i.id).sort()).toEqual(s.items.map((i) => i.id).sort());
    // restored once, then deleted and restored again: ids stay consistent
    let undo!: () => void;
    const victim = result.current.items.find((i) => i.type === LAYOUT.items.find((o) => o.id === "b")!.type && i.id !== "a" && i.id !== "c")!;
    act(() => {
      undo = result.current.remove(victim.id);
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    act(() => undo());
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(s.items.length).toBe(3);
    expect(result.current.items.map((i) => i.id).sort()).toEqual(s.items.map((i) => i.id).sort());
  });
});
