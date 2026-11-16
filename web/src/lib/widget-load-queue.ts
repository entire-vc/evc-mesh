/**
 * At most MAX_CONCURRENT_WIDGET_LOADS external widget frames load at once
 * (dashboard.widget). A widget that wants to load waits in a FIFO queue and is
 * started when a running one has finished its start-up (`mesh.app-ready`), failed
 * or been removed. Module-level: every dashboard on the page shares one queue.
 */
export const MAX_CONCURRENT_WIDGET_LOADS = 6;

type Start = () => void;

let running = 0;
const waiting: Start[] = [];

function pump() {
  while (running < MAX_CONCURRENT_WIDGET_LOADS && waiting.length > 0) {
    running += 1;
    waiting.shift()!();
  }
}

/**
 * Ask for a load slot. `start` runs once the slot is granted (possibly at once).
 * The returned function gives the slot back, or leaves the queue when it was
 * never granted; calling it twice is harmless.
 */
export function acquireWidgetLoadSlot(start: Start): () => void {
  let state: "waiting" | "running" | "done" = "waiting";
  const entry: Start = () => {
    state = "running";
    start();
  };
  waiting.push(entry);
  pump();
  return () => {
    if (state === "waiting") {
      const i = waiting.indexOf(entry);
      if (i >= 0) waiting.splice(i, 1);
    } else if (state === "running") {
      running -= 1;
    }
    state = "done";
    pump();
  };
}

/** Test hook: how many loads run and how many wait. */
export function widgetLoadQueueState(): { running: number; waiting: number } {
  return { running, waiting: waiting.length };
}

export function resetWidgetLoadQueueForTests() {
  running = 0;
  waiting.length = 0;
}
