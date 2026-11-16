// State of one edit session. Every action is applied at once (optimistic), saved with the server version, and
// rolled back when the save fails. Saves are serialised: the next one starts from the version the last one returned.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { addItem, markUnconfirmed, reconcileIds, remapIds, removeItem, restoreItem, sameLayout } from "./model";
import { StaleLayoutError, type CatalogType, type DashboardLayout, type LayoutApi, type LayoutItem } from "./types";

export type EditStatus = "idle" | "saving" | "error" | "stale";

export interface LayoutEditor {
  items: LayoutItem[];
  status: EditStatus;
  /** Something differs from the state at the moment edit mode was entered. */
  dirty: boolean;
  /** Apply a new item list (drag, resize, settings, reorder). */
  apply: (next: LayoutItem[]) => void;
  add: (type: CatalogType) => LayoutItem;
  /** Removes and returns an undo function (valid for the toast's 5 s). */
  remove: (id: string) => () => void;
  cancelChanges: () => Promise<void>;
  /** Adopt the server copy after a conflict. */
  refresh: () => Promise<void>;
  /** Draft discarded by the last conflict: kept so nothing is lost silently. */
  lostDraft: LayoutItem[] | null;
}

export function useLayoutEditor(layout: DashboardLayout, api: LayoutApi, onChange: (next: DashboardLayout) => void): LayoutEditor {
  const [items, setItems] = useState<LayoutItem[]>(layout.items);
  const [status, setStatus] = useState<EditStatus>("idle");
  const [lostDraft, setLostDraft] = useState<LayoutItem[] | null>(null);

  // The snapshot "Cancel changes" returns to: the layout at the moment of entering the mode.
  const entry = useRef<LayoutItem[]>(layout.items);
  const confirmed = useRef({ version: layout.version, items: layout.items });
  const current = useRef(layout.items);
  const chain = useRef<Promise<void>>(Promise.resolve());
  const pending = useRef(0);
  const dead = useRef(false);
  const staleRef = useRef(false);
  // temporary id of an item added in this session -> the id the server gave it
  const idMap = useRef(new Map<string, string>());
  // bumped by a rollback: saves queued before it were built on a state that no longer exists
  const gen = useRef(0);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  useEffect(() => {
    dead.current = false;
    return () => {
      dead.current = true;
    };
  }, []);

  const commit = useCallback(
    (next: LayoutItem[]) => {
      current.current = next;
      setItems(next);
      pending.current += 1;
      setStatus("saving");
      const myGen = gen.current;
      chain.current = chain.current.then(async () => {
        // a conflict (or rollback) earlier in the chain: nothing after it may overwrite the other writer
        if (staleRef.current || myGen !== gen.current) {
          pending.current -= 1;
          return;
        }
        try {
          // an earlier save may have given the server ids to items this one still carries as new
          const known = new Set(confirmed.current.items.map((i) => i.id));
          const toSend = markUnconfirmed(remapIds(next, idMap.current), known);
          const r = await api.save(layout.dashboardId, confirmed.current.version, toSend);
          const given = r.items ? reconcileIds(toSend, r.items) : new Map<string, string>();
          given.forEach((serverId, tempId) => {
            // an id may be replaced more than once (restored, deleted, restored again): keep the chain flat
            idMap.current.forEach((v, k) => {
              if (v === tempId) idMap.current.set(k, serverId);
            });
            idMap.current.set(tempId, serverId);
          });
          entry.current = remapIds(entry.current, given);
          confirmed.current = { version: r.version, items: r.items ?? toSend };
          // later edits are already in `current`: swap only the ids, keep everything else
          const swapped = remapIds(current.current, given);
          if (swapped !== current.current) {
            current.current = swapped;
            if (!dead.current) setItems(swapped);
          }
          pending.current -= 1;
          if (dead.current) return;
          onChangeRef.current({ dashboardId: layout.dashboardId, version: r.version, items: confirmed.current.items });
          if (pending.current === 0) setStatus("idle");
        } catch (e) {
          pending.current -= 1;
          if (dead.current) return;
          if (e instanceof StaleLayoutError) {
            staleRef.current = true;
            setLostDraft(next);
            setStatus("stale");
            return;
          }
          // roll back to the last state the server confirmed; later queued saves are superseded by it
          gen.current += 1;
          current.current = confirmed.current.items;
          setItems(confirmed.current.items);
          setStatus("error");
        }
      });
    },
    [api, layout.dashboardId],
  );
  const apply = useCallback(
    (next: LayoutItem[]) => {
      if (staleRef.current || sameLayout(next, current.current)) return;
      commit(next);
    },
    [commit],
  );

  const add = useCallback(
    (type: CatalogType) => {
      const { items: next, added } = addItem(current.current, type);
      apply(next);
      return added;
    },
    [apply],
  );

  const remove = useCallback(
    (id: string) => {
      const before = current.current;
      const index = before.findIndex((i) => i.id === id);
      const gone = before[index];
      apply(removeItem(before, id));
      return () => {
        if (gone) apply(restoreItem(current.current, gone, index));
      };
    },
    [apply],
  );

  const cancelChanges = useCallback(async () => {
    if (staleRef.current) return;
    apply(entry.current);
    await chain.current;
  }, [apply]);

  const refresh = useCallback(async () => {
    const fresh = await api.load(layout.dashboardId);
    staleRef.current = false;
    idMap.current = new Map();
    confirmed.current = { version: fresh.version, items: fresh.items };
    entry.current = fresh.items;
    current.current = fresh.items;
    setItems(fresh.items);
    setLostDraft(null);
    setStatus("idle");
    onChangeRef.current(fresh);
  }, [api, layout.dashboardId]);

  return useMemo(
    () => ({ items, status, dirty: !sameLayout(items, entry.current), apply, add, remove, cancelChanges, refresh, lostDraft }),
    [items, status, apply, add, remove, cancelChanges, refresh, lostDraft],
  );
}
