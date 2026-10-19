import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { DocTree } from "@/components/doc-tree";
import type { ProjectDocument } from "@/types";

const loadServerExpanded = vi.fn();
const saveServerExpanded = vi.fn();
vi.mock("@/lib/docs-tree-sync", () => ({
  loadServerExpanded: (...a: unknown[]) => loadServerExpanded(...a),
  saveServerExpanded: (...a: unknown[]) => saveServerExpanded(...a),
}));

function doc(id: string, parent_id: string | null = null): ProjectDocument {
  return {
    id,
    project_id: "p1",
    parent_id,
    slug: id,
    title: id,
    storage_key: `documents/p1/${id}.md`,
    position: 0,
    created_by: "u1",
    created_by_type: "user",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    version: 1,
  };
}

// root > a > a1 ;  root > b > b1
const DOCS = [doc("root"), doc("a", "root"), doc("a1", "a"), doc("b", "root"), doc("b1", "b")];
const KEY = "mesh.docs.tree.expanded.u1.p1";

function mount(selectedId: string | null = null) {
  return render(
    <DocTree
      documents={DOCS}
      storageKey={KEY}
      projectId="p1"
      selectedId={selectedId}
      onSelect={vi.fn()}
      onCreateChild={vi.fn()}
      onRename={vi.fn()}
      onMove={vi.fn()}
      onDelete={vi.fn()}
    />,
  );
}

const flush = () => act(async () => { await Promise.resolve(); });

describe("DocTree server sync", () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.useFakeTimers();
    loadServerExpanded.mockReset();
    saveServerExpanded.mockReset().mockResolvedValue(true);
  });
  afterEach(() => vi.useRealTimers());

  it("takes the server's expansion on a device with an empty cache", async () => {
    loadServerExpanded.mockResolvedValue({ kind: "ok", expanded: ["root", "b"] });
    mount();
    expect(screen.queryByText("b1")).not.toBeInTheDocument();
    await flush();
    expect(screen.getByText("b1")).toBeInTheDocument();
    expect(screen.queryByText("a1")).not.toBeInTheDocument();
  });

  it("keeps the open document's ancestors on top of the server state", async () => {
    loadServerExpanded.mockResolvedValue({ kind: "ok", expanded: ["root", "b"] });
    mount("a1");
    await flush();
    expect(screen.getByText("a1")).toBeInTheDocument(); // root > a opened for the selection
    expect(screen.getByText("b1")).toBeInTheDocument(); // and the server's branch
  });

  it("pushes a toggle once, about a second later", async () => {
    loadServerExpanded.mockResolvedValue({ kind: "none" });
    mount();
    await flush();
    fireEvent.click(screen.getByLabelText("Expand root"));
    fireEvent.click(screen.getByLabelText("Expand a"));
    await act(async () => { await vi.advanceTimersByTimeAsync(900); });
    expect(saveServerExpanded).not.toHaveBeenCalled();
    await act(async () => { await vi.advanceTimersByTimeAsync(200); });
    expect(saveServerExpanded).toHaveBeenCalledTimes(1);
    const [projectId, ids] = saveServerExpanded.mock.calls[0]!;
    expect(projectId).toBe("p1");
    expect([...(ids as string[])].sort()).toEqual(["a", "root"]);
  });

  it("does not write anything before the person changes something", async () => {
    loadServerExpanded.mockResolvedValue({ kind: "ok", expanded: ["root"] });
    mount();
    await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(saveServerExpanded).not.toHaveBeenCalled();
  });

  it("a failed load leaves the tree working locally and never overwrites the server unprompted", async () => {
    loadServerExpanded.mockResolvedValue({ kind: "error" });
    window.localStorage.setItem(KEY, JSON.stringify(["root"]));
    mount();
    await flush();
    expect(screen.getByText("a")).toBeInTheDocument();
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(saveServerExpanded).not.toHaveBeenCalled();
    // A toggle is an explicit intent: it is pushed even though the load failed.
    fireEvent.click(screen.getByLabelText("Expand b"));
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); });
    expect(saveServerExpanded).toHaveBeenCalledTimes(1);
  });

  it("a toggle made before the server answers wins over the late answer", async () => {
    let resolve!: (v: unknown) => void;
    loadServerExpanded.mockReturnValue(new Promise((r) => { resolve = r; }));
    window.localStorage.setItem(KEY, JSON.stringify(["root"]));
    mount();
    fireEvent.click(screen.getByLabelText("Expand a"));
    await act(async () => { resolve({ kind: "ok", expanded: ["b"] }); });
    expect(screen.getByText("a1")).toBeInTheDocument();
    expect(screen.queryByText("b1")).not.toBeInTheDocument();
  });

  it("retries after a failed write on the next change", async () => {
    loadServerExpanded.mockResolvedValue({ kind: "none" });
    saveServerExpanded.mockResolvedValueOnce(false).mockResolvedValue(true);
    mount();
    await flush();
    fireEvent.click(screen.getByLabelText("Expand root"));
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); });
    fireEvent.click(screen.getByLabelText("Expand a"));
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); });
    expect(saveServerExpanded).toHaveBeenCalledTimes(2);
    expect([...(saveServerExpanded.mock.calls[1]![1] as string[])].sort()).toEqual(["a", "root"]);
  });

  it("flushes a pending write when the tree unmounts", async () => {
    loadServerExpanded.mockResolvedValue({ kind: "none" });
    const view = mount();
    await flush();
    fireEvent.click(screen.getByLabelText("Expand root"));
    view.unmount();
    expect(saveServerExpanded).toHaveBeenCalledTimes(1);
    expect(saveServerExpanded.mock.calls[0]![1]).toEqual(["root"]);
  });
});
