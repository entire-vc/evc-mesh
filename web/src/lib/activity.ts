/** The activity API contains both field diffs and event metadata in `changes`.
 * Only an object with exactly two own keys, old and new, is a field diff.
 * Everything else is retained as event data, including partial legacy objects.
 */
export type ActivityDetail =
  | { kind: "diff"; field: string; old: unknown; new: unknown; unchanged: boolean }
  | { kind: "metadata"; field: string; value: unknown };

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

const hasOwn = (value: object, key: string) => Object.prototype.hasOwnProperty.call(value, key);

function equalValues(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (Array.isArray(a) && Array.isArray(b)) {
    return a.length === b.length && a.every((value, i) => equalValues(value, b[i]));
  }
  if (isRecord(a) && isRecord(b)) {
    const keys = Object.keys(a);
    return keys.length === Object.keys(b).length && keys.every(
      (key) => hasOwn(b, key) && equalValues(a[key], b[key]),
    );
  }
  return false;
}

export function decodeActivityChanges(changes: unknown): ActivityDetail[] {
  // Missing payload is different from an explicitly recorded null payload.
  if (changes === undefined) return [];
  if (!isRecord(changes)) return [{ kind: "metadata", field: "details", value: changes }];
  return Object.entries(changes).map(([field, value]) => {
    if (isRecord(value) && Object.keys(value).length === 2 &&
      hasOwn(value, "old") && hasOwn(value, "new")) {
      return { kind: "diff", field, old: value.old, new: value.new,
        unchanged: equalValues(value.old, value.new) };
    }
    return { kind: "metadata", field, value };
  });
}

export function formatActivityField(field: string): string {
  if (field === "ttl_min") return "TTL (minutes)";
  return field.replace(/_/g, " ").replace(/\bid\b/g, "ID")
    .replace(/^\w/, (c) => c.toUpperCase());
}

export function formatActivityValue(value: unknown, names?: Map<string, string>): string {
  if (value === undefined) return "not recorded";
  if (value === null) return "none";
  if (typeof value === "string") {
    if (names?.has(value)) return names.get(value)!;
    if (/^\d{4}-\d{2}-\d{2}T/.test(value)) {
      const date = new Date(value);
      if (!Number.isNaN(date.getTime())) {
        // Include time even at midnight: checkout expiry is a timestamp.
        const pad = (n: number) => String(n).padStart(2, "0");
        return `${pad(date.getDate())}.${pad(date.getMonth() + 1)}.${date.getFullYear()} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
      }
    }
    return value === "" ? '""' : value;
  }
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

const ACTION_LABELS: Record<string, string> = {
  "task.checkout_acquired": "Checkout acquired",
  "task.checkout_renewed": "Checkout renewed",
  "task.checkout_released": "Checkout released",
  "task.checkout_released_auto": "Checkout released automatically",
  "task.checkout_force_released": "Checkout released by administrator",
  "task.checkout_lease_expired": "Checkout expired",
  "task.checkout_auto_progress": "Moved to In Progress after checkout",
  "task.checkout_auto_progress_failed": "Could not move to In Progress after checkout",
};

export function describeActivity(action: string, details: ActivityDetail[], names?: Map<string, string>): string {
  const label = hasOwn(ACTION_LABELS, action) ? ACTION_LABELS[action]! : action.replace(/[._]/g, " ");
  const diffs = details.flatMap((detail) => {
    if (detail.kind !== "diff") return [];
    const field = formatActivityField(detail.field);
    const value = formatActivityValue(detail.new, names);
    if (detail.unchanged) return [`${field} unchanged (${value})`];
    return [`${field} changed from "${formatActivityValue(detail.old, names)}" to "${value}"`];
  });
  return diffs.length > 0 ? `${label}: ${diffs.join(", ")}` : label;
}
