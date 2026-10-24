// Pure helpers of the card: no React, no SDK runtime, so they unit-test without a Mesh host.
export function text(value: unknown): string {
  if (value == null || value === "") return "";
  return Array.isArray(value) ? value.join(", ") : String(value);
}

export function dueLabel(dueDate: string | null | undefined, overdue: boolean): string {
  if (!dueDate) return "";
  return `${overdue ? "Overdue" : "Due"}: ${dueDate.slice(0, 10)}`;
}
