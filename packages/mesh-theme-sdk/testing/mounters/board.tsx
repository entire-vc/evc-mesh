// Container + reference neighbours for board.layout / board.column / board.card. A theme usually replaces one level;
// the other levels are the harness's reference views, written against the SDK primitives only, so the level under
// test is exercised inside a complete board.
import type { ComponentType } from "react";
import type {
  BoardCardView,
  BoardCardViewData,
  BoardColumnData,
  BoardColumnView,
  BoardLayoutData,
  BoardLayoutView,
  FieldDescriptor,
  SlotContext,
  TaskData,
} from "../../src/index";
import { fixtures } from "../fixtures";
import type { RenderEnv, Scenario, SlotMounter } from "../types";
import { registerMounter } from "./registry";

export type BoardData = Omit<BoardLayoutData, "components">;

const columnsOf = (d: BoardData): BoardColumnData[] => {
  const c = d.columns;
  return c.state === "ready" || c.state === "refreshing" ? c.value : c.state === "error" ? (c.value ?? []) : [];
};

// -- reference views ------------------------------------------------------------------------------------

const RefCard: BoardCardView = ({ data, actions }) => {
  const Sdk = REF.sdk;
  const { task, isOverlay } = data;
  const body = (
    <>
      <Sdk.TaskLink taskId={task.id}>{task.title}</Sdk.TaskLink>
      {data.gateStatusName ? <span data-ref="gate">{data.gateStatusName}</span> : null}
    </>
  );
  void actions;
  // The overlay is the picture under the pointer: it must not be a draggable itself.
  if (isOverlay) return <div data-ref="card" data-task-id={task.id}>{body}</div>;
  return (
    <Sdk.Draggable item={{ kind: "task", id: task.id }} label={task.title} data-ref="card" data-task-id={task.id}>
      {body}
    </Sdk.Draggable>
  );
};

const RefColumn: BoardColumnView = ({ data, actions, themeSettings, ctx }) => {
  const { column, components } = data;
  const Card = components.Card;
  const Sdk = REF.sdk;
  const zoneEnabled = data.dnd.enabled && column.kind === "status";
  return (
    <section data-column-id={column.id} aria-label={column.title}>
      <h3>
        {column.title} <span data-ref="count">{column.countLabel}</span>
      </h3>
      {(() => {
        const list = column.tasks.map((task) => {
          const cardData: BoardCardViewData = {
            task, column: { id: column.id, kind: column.kind, statusCategory: column.status?.category },
            gateStatusName: column.gateStatusNameByTask?.[task.id], fields: data.fields, selected: false, isOverlay: false,
          };
          return (
            <Card
              key={task.id}
              data={cardData}
              actions={{
                openTask: (o) => actions.openTask(task.id, o),
                ...(actions.quickEdit ? { quickEdit: (p) => actions.quickEdit!(task.id, p) } : {}),
              }}
              themeSettings={themeSettings}
              ctx={ctx}
            />
          );
        });
        return column.kind === "status" ? (
          <Sdk.DropZone zone={{ kind: "status", id: column.id }} orientation="vertical" itemIds={column.tasks.map((t) => t.id)} enabled={zoneEnabled}>
            {list}
          </Sdk.DropZone>
        ) : (
          <div>{list}</div>
        );
      })()}
      {column.hasMore ? (
        <button type="button" disabled={column.loadingMore} onClick={() => void actions.loadMore()}>More</button>
      ) : null}
    </section>
  );
};

const RefLayout: BoardLayoutView = ({ data, actions, themeSettings, ctx }) => {
  const { Column, Card } = data.components;
  const cols = data.columns;
  if (cols.state === "loading") return <div role="status" aria-busy="true" data-ref="skeleton">Loading board</div>;
  if (cols.state === "error" && !cols.value) {
    return (
      <div role="alert">
        <p>{cols.error.message}</p>
        <button type="button" onClick={() => cols.retry()}>Retry</button>
      </div>
    );
  }
  const list = columnsOf(data);
  const byKey = new Map(data.availableFields.map((f) => [f.key, f]));
  const fields = data.cardFields.map((k) => byKey.get(k)).filter((f): f is FieldDescriptor => Boolean(f));
  return (
    <div>
      {list.map((column, index) => (
        <Column
          key={column.id}
          data={{ column, index, total: list.length, dnd: data.dnd, fields, components: { Card } }}
          actions={{
            loadMore: () => actions.loadMore(column.id),
            openTask: actions.openTask,
            ...(actions.quickEdit ? { quickEdit: actions.quickEdit } : {}),
            moveTask: (taskId, position) => actions.moveTask(taskId, column.id, position),
            ...(actions.createTask ? { createTask: () => actions.createTask!(column.id) } : {}),
          }}
          themeSettings={themeSettings}
          ctx={ctx}
        />
      ))}
    </div>
  );
};

/** The host's primitives, set by the mounter at render time (reference views are plain functions). */
const REF: { sdk: RenderEnv<BoardData>["host"]["sdk"] } = { sdk: undefined as never };

// -- jsdom has no layout: put each column on a grid so dnd-kit can find the neighbours ----------------------

const COLUMN = "[data-mesh-dropzone],[data-column-id]";

/** The outermost column element around `el` (a column view may nest its DropZone inside its own root). */
function columnOf(el: Element): Element | null {
  let outer: Element | null = null;
  for (let p = el.closest(COLUMN); p; p = p.parentElement?.closest(COLUMN) ?? null) outer = p;
  return outer;
}

function stubLayout(): () => void {
  const original = Element.prototype.getBoundingClientRect;
  Element.prototype.getBoundingClientRect = function (this: Element) {
    const el = this as HTMLElement;
    const cols = Array.from(document.querySelectorAll(COLUMN)).filter((c) => columnOf(c) === c);
    const col = columnOf(el);
    const ci = col ? cols.indexOf(col) : 0;
    const isZone = el.matches(COLUMN);
    const row = isZone ? 0 : Array.from(col?.querySelectorAll("[data-mesh-draggable]") ?? []).indexOf(el.closest("[data-mesh-draggable]") as Element);
    const x = ci * 300;
    const y = isZone ? 0 : 20 + Math.max(row, 0) * 60;
    return { x, y, left: x, top: y, width: 200, height: isZone ? 300 : 50, right: x + 200, bottom: y + (isZone ? 300 : 50), toJSON() {} } as DOMRect;
  };
  return () => {
    Element.prototype.getBoundingClientRect = original;
  };
}

// -- the mounter ---------------------------------------------------------------------------------------

const DND_REASON = (role: string, groupBy: string, mobileReadonly: boolean): BoardData["dnd"]["reason"] =>
  mobileReadonly ? "mobile_readonly" : groupBy !== "status" ? "group_by" : role === "viewer" ? "no_permission" : undefined;

function defaults(s: Scenario<BoardData>): BoardData {
  const groupBy = (s.options?.groupBy as BoardData["groupBy"] | undefined) ?? "status";
  const reason = DND_REASON(s.role ?? "member", groupBy, s.options?.dndReason === "mobile_readonly");
  const base = fixtures.board({ gated: s.options?.gated === true });
  // An extension filling `task.badge` draws on a task that has a VCS link: give the first card one.
  if (s.extensions?.includes("task.badge")) {
    const first = base.columns.value[0]!.tasks[0]!;
    first.vcsProviders = ["gitlab"];
    first.counts = { ...first.counts, vcsLinks: 1 };
  }
  return { ...base, groupBy, dnd: { ...base.dnd, enabled: reason === undefined, reason } };
}

function render(env: RenderEnv<BoardData>) {
  REF.sdk = env.host.sdk;
  const Sdk = env.host.sdk;
  const slot = env.scenario.slot;
  const view = env.view as unknown;
  if (slot === "board.card" && env.scenario.options?.overlay) {
    // The preview under the pointer: the card alone, outside any DropZone, flagged isOverlay.
    const first = columnsOf(env.data)[0]!;
    const Card = view as BoardCardView;
    return (
      <Card
        data={{ task: first.tasks[0]!, column: { id: first.id, kind: first.kind }, fields: [], selected: false, isOverlay: true }}
        actions={{ openTask: () => {} }}
        themeSettings={env.themeSettings}
        ctx={env.ctx}
      />
    );
  }
  const components = {
    Column: (slot === "board.column" ? view : RefColumn) as BoardColumnView,
    Card: (slot === "board.card" ? view : RefCard) as BoardCardView,
  };
  const Layout = (slot === "board.layout" ? view : RefLayout) as ComponentType<{
    data: BoardLayoutData; actions: unknown; themeSettings: Record<string, unknown>; ctx: SlotContext;
  }>;
  const data: BoardLayoutData & { components: typeof components } = { ...env.data, components };
  // The container resolves a finished drop; a refusal is reported by the core and never reaches the view (CT-board-16).
  const onMove = async (m: { item: { id: string }; to: { kind: string; id: string | null }; index: number }) => {
    if (m.to.kind === "status") {
      const move = env.actions.moveTask as ((id: string, to: string, p: { index: number }) => Promise<void>) | undefined;
      if (!move) return;
      await Promise.resolve(move(m.item.id, m.to.id as string, { index: m.index })).catch(() => {});
    }
  };
  return (
    <Sdk.DndProvider enabled={env.data.dnd.enabled} onMove={onMove}>
      <Layout data={data} actions={env.actions} themeSettings={env.themeSettings} ctx={env.ctx} />
    </Sdk.DndProvider>
  );
}

const ACTIONS = [
  { name: "moveTask", needs: "move", async: true },
  { name: "openTask" },
  { name: "quickEdit", needs: "edit", async: true },
  { name: "createTask", needs: "create" },
  { name: "loadMore", async: true },
  { name: "retry" },
] as const;

function reduce(data: BoardData, e: { type: string; data: Record<string, unknown> }): BoardData {
  if (e.type !== "task.updated") return data;
  const { id, title } = e.data as { id: string; title?: string };
  const cols = columnsOf(data);
  const next = cols.map((c) => (c.tasks.some((t) => t.id === id) ? { ...c, tasks: c.tasks.map((t): TaskData => (t.id === id && title ? { ...t, title } : t)) } : c));
  return { ...data, columns: { state: "ready", value: next } };
}

function asyncData(data: BoardData, state: string, spies: Record<string, { (...a: unknown[]): unknown }>): BoardData {
  const retry = () => void spies.retry?.();
  const cols = columnsOf(data);
  if (state === "loading") return { ...data, columns: { state: "loading" } };
  if (state === "error") return { ...data, columns: { state: "error", error: { code: "x", message: "Could not load the board" }, retry } };
  return { ...data, columns: { state: "refreshing", value: cols } };
}

const mounter: SlotMounter<BoardData> = { slot: "board.layout", defaults, actions: [...ACTIONS], reduce, render, setup: stubLayout, asyncData };
registerMounter(mounter);
registerMounter({ ...mounter, slot: "board.column" });
registerMounter({ ...mounter, slot: "board.card" });
