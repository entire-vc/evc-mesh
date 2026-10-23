import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { useMemberStore } from "@/stores/member";
import { useTaskStore } from "@/stores/task";
import type { Task } from "@/types";
import {
  Can,
  CoreActionsProvider,
  DndProvider,
  Draggable,
  DropZone,
  InlineEdit,
  TaskLink,
  type MoveRequest,
} from "..";
import { roleCan } from "../permissions";

const tick = () => act(async () => { await new Promise((r) => setTimeout(r, 30)); });
const asRole = (role: "viewer" | "member" | null) => useMemberStore.setState({ myRole: role });

beforeEach(() => asRole("member"));
afterEach(cleanup);

describe("roleCan / Can", () => {
  it("viewer cannot move or edit; member and unknown role can", () => {
    expect(roleCan("viewer", "task.move")).toBe(false);
    expect(roleCan("viewer", "task.edit")).toBe(false);
    expect(roleCan("member", "task.move")).toBe(true);
    expect(roleCan(null, "task.edit")).toBe(true);
  });

  it("renders children with the permission, fallback without", () => {
    const ui = (
      <Can permission="task.edit" fallback={<i>no</i>}>
        <b>yes</b>
      </Can>
    );
    const { unmount } = render(ui);
    expect(screen.getByText("yes")).toBeTruthy();
    unmount();
    asRole("viewer");
    render(ui);
    expect(screen.queryByText("yes")).toBeNull();
    expect(screen.getByText("no")).toBeTruthy();
  });
});

// jsdom has no layout: give every drag item / zone a rect from its position in a
// two-column grid so dnd-kit's keyboard coordinates can find the neighbours.
function stubLayout() {
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    const col = this.closest("[data-col]")?.getAttribute("data-col");
    const x = col === "b" ? 300 : 0;
    const el = this as HTMLElement;
    const row = Number(el.dataset.row ?? 0);
    const isZone = el.hasAttribute("data-col");
    return { x, y: isZone ? 0 : 20 + row * 60, left: x, top: isZone ? 0 : 20 + row * 60, width: 200, height: isZone ? 400 : 50, right: x + 200, bottom: 400, toJSON() {} } as DOMRect;
  });
}

function Board({ onMove }: { onMove: (m: MoveRequest) => void }) {
  return (
    <DndProvider onMove={onMove}>
      {["a", "b"].map((col) => (
        <DropZone
          key={col}
          zone={{ kind: "status", id: col }}
          orientation="vertical"
          itemIds={col === "a" ? ["t1", "t2"] : ["t3"]}
          data-col={col}
          data-testid={`zone-${col}`}
        >
          {(col === "a" ? ["t1", "t2"] : ["t3"]).map((id, row) => (
            <Draggable key={id} item={{ kind: "task", id }} data-testid={id} data-row={String(row)}>
              card {id}
            </Draggable>
          ))}
        </DropZone>
      ))}
    </DndProvider>
  );
}

describe("Draggable keyboard", () => {
  it("Space picks up, ArrowRight moves to the next zone, Space drops: onMove gets the target zone", async () => {
    stubLayout();
    const onMove = vi.fn();
    render(<Board onMove={onMove} />);
    const card = screen.getByTestId("t1");
    expect(card.getAttribute("tabindex")).toBe("0");
    expect(card.getAttribute("aria-roledescription")).toBeTruthy();
    card.focus();
    fireEvent.keyDown(card, { code: "Space", key: " " });
    await tick();
    fireEvent.keyDown(document, { code: "ArrowRight", key: "ArrowRight" });
    await tick();
    fireEvent.keyDown(document, { code: "Space", key: " " });
    await waitFor(() => expect(onMove).toHaveBeenCalledTimes(1));
    const m = onMove.mock.calls[0]![0] as MoveRequest;
    expect(m.item).toEqual({ kind: "task", id: "t1" });
    expect(m.from).toEqual({ kind: "status", id: "a" });
    expect(m.to).toEqual({ kind: "status", id: "b" });
  });

  it("Space then Space on the same card: dropped on itself, no move", async () => {
    stubLayout();
    const onMove = vi.fn();
    render(<Board onMove={onMove} />);
    const card = screen.getByTestId("t1");
    card.focus();
    fireEvent.keyDown(card, { code: "Space", key: " " });
    await tick();
    fireEvent.keyDown(document, { code: "Space", key: " " });
    await act(async () => {});
    await tick();
    expect(onMove).not.toHaveBeenCalled();
  });

  it("Escape cancels: no move", async () => {
    stubLayout();
    const onMove = vi.fn();
    render(<Board onMove={onMove} />);
    const card = screen.getByTestId("t1");
    card.focus();
    fireEvent.keyDown(card, { code: "Space", key: " " });
    await act(async () => {});
    fireEvent.keyDown(document, { code: "ArrowRight", key: "ArrowRight" });
    fireEvent.keyDown(document, { code: "Escape", key: "Escape" });
    await act(async () => {});
    expect(onMove).not.toHaveBeenCalled();
  });

  it("without task.move there is no handle: no tabindex, no roledescription, Space does nothing", async () => {
    asRole("viewer");
    stubLayout();
    const onMove = vi.fn();
    render(<Board onMove={onMove} />);
    const card = screen.getByTestId("t1");
    expect(card.hasAttribute("tabindex")).toBe(false);
    expect(card.hasAttribute("aria-roledescription")).toBe(false);
    fireEvent.keyDown(card, { code: "Space", key: " " });
    fireEvent.keyDown(document, { code: "ArrowRight", key: "ArrowRight" });
    fireEvent.keyDown(document, { code: "Space", key: " " });
    await act(async () => {});
    expect(onMove).not.toHaveBeenCalled();
  });

  it("enabled=false removes the handle even for a member", () => {
    render(
      <DndProvider onMove={() => {}}>
        <DropZone zone={{ kind: "status", id: "a" }} orientation="vertical" itemIds={["t1"]}>
          <Draggable item={{ kind: "task", id: "t1" }} enabled={false} data-testid="t1">
            x
          </Draggable>
        </DropZone>
      </DndProvider>,
    );
    expect(screen.getByTestId("t1").hasAttribute("tabindex")).toBe(false);
  });

  it("custom handle: the Handle carries the grab attributes, absent without the right", () => {
    const ui = (
      <DndProvider onMove={() => {}}>
        <DropZone zone={{ kind: "status", id: "a" }} orientation="vertical" itemIds={["t1"]}>
          <Draggable item={{ kind: "task", id: "t1" }} handle="custom" data-testid="t1">
            <Draggable.Handle>grip</Draggable.Handle>
          </Draggable>
        </DropZone>
      </DndProvider>
    );
    const { unmount } = render(ui);
    expect(screen.getByText("grip").getAttribute("aria-roledescription")).toBeTruthy();
    expect(screen.getByTestId("t1").hasAttribute("tabindex")).toBe(false);
    unmount();
    asRole("viewer");
    render(ui);
    expect(screen.queryByText("grip")).toBeNull();
  });
});

const task = { id: "t1", title: "Old", priority: "medium", labels: [] } as unknown as Task;

describe("InlineEdit", () => {
  beforeEach(() => useTaskStore.setState({ tasks: [task] }));

  it("without task.edit renders only its children", () => {
    asRole("viewer");
    render(
      <MemoryRouter>
        <InlineEdit taskId="t1" field="title">
          Old
        </InlineEdit>
      </MemoryRouter>,
    );
    expect(screen.getByText("Old").hasAttribute("role")).toBe(false);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("Enter opens the editor, Enter commits through actions.quickEdit, Escape cancels", async () => {
    const quickEdit = vi.fn().mockResolvedValue(undefined);
    render(
      <CoreActionsProvider actions={{ quickEdit }}>
        <MemoryRouter>
          <InlineEdit taskId="t1" field="title">
            Old
          </InlineEdit>
        </MemoryRouter>
      </CoreActionsProvider>,
    );
    fireEvent.keyDown(screen.getByRole("button"), { key: "Enter" });
    const input = screen.getByLabelText("title") as HTMLInputElement;
    expect(input.value).toBe("Old");
    fireEvent.change(input, { target: { value: "New" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(quickEdit).toHaveBeenCalledWith("t1", "title", "New"));
  });

  it("Escape closes without committing", () => {
    const quickEdit = vi.fn();
    render(
      <CoreActionsProvider actions={{ quickEdit }}>
        <MemoryRouter>
          <InlineEdit taskId="t1" field="title">
            Old
          </InlineEdit>
        </MemoryRouter>
      </CoreActionsProvider>,
    );
    fireEvent.click(screen.getByRole("button"));
    const input = screen.getByLabelText("title");
    fireEvent.change(input, { target: { value: "New" } });
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByLabelText("title")).toBeNull();
    expect(quickEdit).not.toHaveBeenCalled();
  });
});

describe("TaskLink", () => {
  it("has a real href; a plain click calls actions.openTask, ctrl-click is left to the browser", () => {
    const openTask = vi.fn();
    render(
      <CoreActionsProvider actions={{ openTask }}>
        <MemoryRouter>
          <TaskLink taskId="abc">open</TaskLink>
        </MemoryRouter>
      </CoreActionsProvider>,
    );
    const a = screen.getByText("open") as HTMLAnchorElement;
    expect(a.getAttribute("href")).toBe("/t/abc");
    fireEvent.click(a);
    expect(openTask).toHaveBeenCalledWith("abc");
    fireEvent.click(a, { ctrlKey: true });
    expect(openTask).toHaveBeenCalledTimes(1);
  });
});
