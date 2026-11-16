// The "shown" state of mentions lives in localStorage (as on the Activity page). Every access is guarded:
// private mode / disabled storage must not take the widget down.
const SHOWN_KEY = "mesh.mentions.shown";
const SHOWN_MAX = 2000;
export const lastVisitKey = (ws: string | undefined) => `mesh:activity:last_visit:${ws ?? "global"}`;

export function readShownIds(): Set<string> {
  try {
    const raw = localStorage.getItem(SHOWN_KEY);
    if (!raw) return new Set();
    const parsed: unknown = JSON.parse(raw);
    return new Set(Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

export function saveShownIds(ids: Set<string>): void {
  try {
    let arr = [...ids];
    if (arr.length > SHOWN_MAX) arr = arr.slice(-SHOWN_MAX);
    localStorage.setItem(SHOWN_KEY, JSON.stringify(arr));
  } catch {
    /* storage unavailable: the state just does not persist */
  }
}

export function readLastVisit(ws: string | undefined): Date | null {
  try {
    const raw = localStorage.getItem(lastVisitKey(ws));
    const d = raw ? new Date(raw) : null;
    return d && !Number.isNaN(d.getTime()) ? d : null;
  } catch {
    return null;
  }
}

export function writeLastVisit(ws: string | undefined, at: Date): void {
  try {
    localStorage.setItem(lastVisitKey(ws), at.toISOString());
  } catch {
    /* ignore */
  }
}

export function clearUnseenTs(): void {
  try {
    localStorage.removeItem("mesh_unseen_ts");
  } catch {
    /* ignore */
  }
}
