import {
  createElement,
  createContext,
  useContext,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type CSSProperties,
  type ReactNode,
} from "react";
import {
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  closestCorners,
  useDroppable,
  useSensor,
  useSensors,
  type Announcements,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
  horizontalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import i18n from "@/i18n";
import { useCan } from "./permissions";
import type { DndProviderProps, DraggableProps, DropZoneProps } from "@entire-vc/mesh-theme-sdk";
import { zoneKey, type DragItem, type DropZoneRef, type Id } from "./types";

// Module-level, not inline at the useSensor call site: an inline options
// object is a new reference every render, so useSensors returns a new array,
// DndContext rebuilds `activators`, and every card re-renders (see
// board-drag-variance-two-mechanisms).
const POINTER_SENSOR_OPTIONS = { activationConstraint: { distance: 5 } };
const KEYBOARD_SENSOR_OPTIONS = { coordinateGetter: sortableKeyboardCoordinates };

// Disables @dnd-kit/sortable's post-drop layout-FLIP animation: it hangs on an
// internal timer, so whether the shifted cards re-render twice is timing
// dependent. Cards snap instantly instead.
const DISABLE_LAYOUT_ANIMATION = () => false;

interface ItemData {
  type: "item";
  item: DragItem;
  zone: DropZoneRef | null;
  label?: string;
}
interface ZoneData {
  type: "zone";
  zone: DropZoneRef;
}

const tr = (key: string, defaultValue: string, vars?: Record<string, unknown>) =>
  i18n.t(key, { ns: "board", defaultValue, ...vars }) as string;

function describeTarget(
  over: { data: { current?: unknown } } | null,
  zoneItems: ReadonlyMap<string, readonly Id[]>,
): string {
  const d = over?.data.current as ItemData | ZoneData | undefined;
  if (!d || !d.zone) return tr("dnd.outside", "outside any list");
  const ids = zoneItems.get(zoneKey(d.zone)) ?? [];
  const pos = d.type === "item" ? ids.indexOf(d.item.id) + 1 : ids.length + 1;
  return tr("dnd.position", "position {{position}} of {{total}}", {
    position: pos,
    total: Math.max(ids.length, pos),
  });
}

const ZoneItemsContext = createContext<Map<string, readonly Id[]>>(new Map());

// Which item is being moved with the keyboard. A store with per-item selectors
// instead of useDndContext(): that hook re-renders every card on every drag
// move (board.drag board_card_commits), this one only the card that changes.
interface KeyboardDragStore {
  id: Id | null;
  listeners: Set<() => void>;
}
const KeyboardDragContext = createContext<KeyboardDragStore>({ id: null, listeners: new Set() });
const setKeyboardDrag = (store: KeyboardDragStore, id: Id | null) => {
  if (store.id === id) return;
  store.id = id;
  store.listeners.forEach((l) => l());
};

/** The one DnD container of a page: pointer + keyboard sensors, screen-reader announcements, overlay and move resolution. */
export function DndProvider({ onMove, onDragActiveChange, renderOverlay, enabled = true, children }: DndProviderProps) {
  const sensors = useSensors(
    useSensor(PointerSensor, POINTER_SENSOR_OPTIONS),
    useSensor(KeyboardSensor, KEYBOARD_SENSOR_OPTIONS),
  );
  const [active, setActive] = useState<DragItem | null>(null);
  // zone key -> item ids, fed by DropZone, read for the move index and announcements.
  const zoneItems = useRef(new Map<string, readonly Id[]>());
  const keyboardDrag = useRef<KeyboardDragStore>({ id: null, listeners: new Set() });
  const onMoveRef = useRef(onMove);
  onMoveRef.current = onMove;
  const activeChangeRef = useRef(onDragActiveChange);
  activeChangeRef.current = onDragActiveChange;

  const announcements = useMemo<Announcements>(() => {
    const name = (id: string | number, data?: { current?: unknown }) =>
      (data?.current as ItemData | undefined)?.label ?? String(id).slice(0, 8);
    return {
      onDragStart: ({ active }) => tr("dnd.pickedUp", "Picked up {{name}}", { name: name(active.id, active.data) }),
      onDragOver: ({ active, over }) =>
        tr("dnd.over", "{{name}} is over {{where}}", {
          name: name(active.id, active.data),
          where: describeTarget(over, zoneItems.current),
        }),
      onDragEnd: ({ active, over }) =>
        tr("dnd.dropped", "{{name}} dropped at {{where}}", {
          name: name(active.id, active.data),
          where: describeTarget(over, zoneItems.current),
        }),
      onDragCancel: ({ active }) =>
        tr("dnd.cancelled", "Move of {{name}} cancelled", { name: name(active.id, active.data) }),
    };
  }, []);

  const handleDragStart = (e: DragStartEvent) => {
    const d = e.active.data.current as ItemData | undefined;
    if (d) setActive(d.item);
    const byKeyboard = typeof KeyboardEvent !== "undefined" && e.activatorEvent instanceof KeyboardEvent;
    setKeyboardDrag(keyboardDrag.current, byKeyboard ? String(e.active.id) : null);
    activeChangeRef.current?.(true);
  };

  const handleDragEnd = async (e: DragEndEvent) => {
    setActive(null);
    setKeyboardDrag(keyboardDrag.current, null);
    try {
      const a = e.active.data.current as ItemData | undefined;
      const o = e.over?.data.current as ItemData | ZoneData | undefined;
      if (!a || !o || !o.zone) return;
      if (zoneItems.current.has(OFF_PREFIX + zoneKey(o.zone))) return;
      const overItem = o.type === "item" ? o.item : null;
      // Released over the card that was picked up: nothing moved.
      if (overItem && overItem.id === a.item.id && a.zone && zoneKey(o.zone) === zoneKey(a.zone)) return;
      const full = zoneItems.current.get(zoneKey(o.zone)) ?? [];
      const ids = full.filter((id) => id !== a.item.id);
      let at = overItem ? ids.indexOf(overItem.id) : -1;
      // Inside one zone a drop on a card below the dragged one lands after it, as in any sortable list.
      if (at !== -1 && overItem && a.zone && zoneKey(o.zone) === zoneKey(a.zone)) {
        const from = full.indexOf(a.item.id);
        if (from !== -1 && from < full.indexOf(overItem.id)) at += 1;
      }
      await onMoveRef.current({
        item: a.item,
        from: a.zone,
        to: o.zone,
        overItem,
        index: at === -1 ? ids.length : at,
      });
    } finally {
      activeChangeRef.current?.(false);
    }
  };

  const handleDragCancel = () => {
    setActive(null);
    setKeyboardDrag(keyboardDrag.current, null);
    activeChangeRef.current?.(false);
  };

  return (
    <ZoneItemsContext.Provider value={zoneItems.current}>
      <KeyboardDragContext.Provider value={keyboardDrag.current}>
      <DndEnabledContext.Provider value={enabled}>
      <DndContext
        sensors={sensors}
        collisionDetection={closestCorners}
        accessibility={{
          announcements,
          screenReaderInstructions: {
            draggable: tr(
              "dnd.instructions",
              "Press Space or Enter to pick up. Arrow keys move, Space or Enter drops, Escape cancels.",
            ),
          },
        }}
        onDragStart={handleDragStart}
        onDragEnd={handleDragEnd}
        onDragCancel={handleDragCancel}
      >
        {children}
        <DragOverlay>{active && renderOverlay ? renderOverlay(active) : null}</DragOverlay>
      </DndContext>
      </DndEnabledContext.Provider>
      </KeyboardDragContext.Provider>
    </ZoneItemsContext.Provider>
  );
}

const ZoneContext = createContext<DropZoneRef | null>(null);
/** The container's switch: false = nothing on the page is draggable (e.g. board not grouped by status). */
const DndEnabledContext = createContext(true);

const OFF_PREFIX = "off:";

export function DropZone({
  zone,
  orientation,
  as = "div",
  className,
  itemIds,
  enabled = true,
  children,
  ...rest
}: DropZoneProps) {
  const key = zoneKey(zone);
  const dndOn = useContext(DndEnabledContext);
  enabled = enabled && dndOn;
  const zoneItems = useContext(ZoneItemsContext);
  zoneItems.set(key, itemIds);
  // A switched-off zone still has cards (draggables) that report it as their zone: mark it so a drop there is ignored.
  if (enabled) zoneItems.delete(OFF_PREFIX + key);
  else zoneItems.set(OFF_PREFIX + key, []);
  const { setNodeRef, isOver } = useDroppable({
    id: key,
    data: { type: "zone", zone } satisfies ZoneData,
    disabled: !enabled,
  });
  return (
    <ZoneContext.Provider value={zone}>
      <SortableContext
        id={key}
        items={enabled ? (itemIds as Id[]) : []}
        strategy={orientation === "vertical" ? verticalListSortingStrategy : horizontalListSortingStrategy}
      >
        {createElement(
          as,
          {
            ref: setNodeRef,
            className: typeof className === "function" ? className({ isOver }) : className,
            // marks a zone that accepts drops; a switched-off zone is only the list its cards belong to
            ...(enabled ? { "data-mesh-dropzone": `${zone.kind}:${zone.id ?? "root"}` } : {}),
            ...rest,
          },
          typeof children === "function" ? children({ isOver }) : children,
        )}
      </SortableContext>
    </ZoneContext.Provider>
  );
}

interface HandleBinding {
  attributes: object;
  listeners: object | undefined;
  setActivatorNodeRef: (el: HTMLElement | null) => void;
}
const HandleContext = createContext<HandleBinding | null>(null);

export function Draggable({
  item,
  as = "div",
  handle = "self",
  label,
  enabled = true,
  children,
  style,
  ...rest
}: DraggableProps) {
  const permitted = useCan(item.kind === "task" ? "task.move" : "doc.move", item);
  const dndOn = useContext(DndEnabledContext);
  const live = permitted && enabled && dndOn;
  const zone = useContext(ZoneContext);
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } =
    useSortable({
      id: item.id,
      data: { type: "item", item, zone, label } satisfies ItemData,
      disabled: !live,
      animateLayoutChanges: DISABLE_LAYOUT_ANIMATION,
    });
  const kb = useContext(KeyboardDragContext);
  const isKeyboardDragging = useSyncExternalStore(
    (cb) => {
      kb.listeners.add(cb);
      return () => void kb.listeners.delete(cb);
    },
    () => kb.id === item.id,
  );

  const css: CSSProperties = {
    ...style,
    transform: CSS.Transform.toString(transform),
    transition: transition ?? undefined,
    opacity: isDragging ? 0.4 : 1,
  };
  const body = typeof children === "function" ? children({ isDragging, isKeyboardDragging }) : children;
  return createElement(
    as,
    {
      ref: setNodeRef,
      style: css,
      // Contract tests find the card by it, handle or not (viewers get no handle but keep the marker).
      "data-mesh-draggable": `${item.kind}:${item.id}`,
      ...rest,
      ...(live && handle === "self" ? { ...attributes, ...listeners } : {}),
    },
    live && handle === "custom" ? (
      <HandleContext.Provider value={{ attributes, listeners, setActivatorNodeRef }}>{body}</HandleContext.Provider>
    ) : (
      body
    ),
  );
}

/** The grab area of a Draggable with handle="custom". Not rendered at all without the right to move. */
function Handle({ children }: { children: ReactNode }) {
  const h = useContext(HandleContext);
  if (!h) return null;
  return (
    <span ref={h.setActivatorNodeRef} {...h.attributes} {...h.listeners}>
      {children}
    </span>
  );
}
Draggable.Handle = Handle;
