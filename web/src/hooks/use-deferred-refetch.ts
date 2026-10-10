import { useCallback, useEffect, useRef } from "react";

/**
 * Debounced refresh that waits for a drag to end. `schedule()` is called per WS
 * event; if a drag is active when the timer fires, the refresh is held. `flush()`
 * (drop or cancel) ends the drag and runs exactly one refresh when any event
 * arrived during it, whether its timer had fired or was still pending.
 */
export function useDeferredRefetch(run: () => void, delayMs = 500) {
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const dragging = useRef(false);
  const pending = useRef(false);
  const runRef = useRef(run);
  runRef.current = run;

  const schedule = useCallback(() => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      timer.current = null;
      if (dragging.current) {
        pending.current = true;
        return;
      }
      runRef.current();
    }, delayMs);
  }, [delayMs]);

  const startDrag = useCallback(() => {
    dragging.current = true;
  }, []);

  const flush = useCallback(() => {
    dragging.current = false;
    const timerWasPending = timer.current !== null;
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
    if (pending.current || timerWasPending) {
      pending.current = false;
      runRef.current();
    }
  }, []);

  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  return { schedule, startDrag, flush };
}
