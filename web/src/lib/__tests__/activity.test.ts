import { describe, expect, it } from "vitest";
import { decodeActivityChanges, describeActivity, formatActivityValue } from "@/lib/activity";

describe("activity JSON codec", () => {
  it("decodes mixed field diffs and event metadata without losing falsy values", () => {
    const result = decodeActivityChanges({
      status: { old: "Todo", new: "In Progress" },
      ttl_min: 120, expires_at: "2026-10-07T03:00:00Z", actor_id: "example-actor",
      checked_out_by: "example-holder", source: "checkout", reason: "completed",
      count: 0, active: false, missing: undefined, cleared: null,
      labels: ["one", "two"], context: { nested: { old: "a", new: "b" } },
    });
    expect(result.filter((d) => d.kind === "diff")).toEqual([
      { kind: "diff", field: "status", old: "Todo", new: "In Progress", unchanged: false },
    ]);
    expect(result).toHaveLength(13);
    expect(result.find((d) => d.field === "ttl_min")).toEqual({ kind: "metadata", field: "ttl_min", value: 120 });
    expect(result.find((d) => d.field === "active")).toEqual({ kind: "metadata", field: "active", value: false });
    expect(result.find((d) => d.field === "context")?.kind).toBe("metadata");
  });

  it.each([null, 120, false, "primitive", ["a", { old: 1, new: 2 }]])("retains a non-record root payload %j", (value) => {
    expect(decodeActivityChanges(value)).toEqual([{ kind: "metadata", field: "details", value }]);
  });

  it("distinguishes an absent payload, explicit null and an incomplete legacy object", () => {
    expect(decodeActivityChanges(undefined)).toEqual([]);
    expect(formatActivityValue(undefined)).toBe("not recorded");
    expect(formatActivityValue(null)).toBe("none");
    expect(decodeActivityChanges({ field: { new: null } })).toEqual([
      { kind: "metadata", field: "field", value: { new: null } },
    ]);
    expect(decodeActivityChanges({ field: { old: null } })[0]?.kind).toBe("metadata");
    expect(decodeActivityChanges({ field: { old: 1, new: 2, reason: "example" } })[0]?.kind).toBe("metadata");
  });

  it("retains equal old/new values as unchanged, and real value-to-null diffs", () => {
    for (const value of [null, "same", 0, [1, 2], { key: true }]) {
      expect(decodeActivityChanges({ field: { old: value, new: value } })[0]).toMatchObject({ kind: "diff", unchanged: true });
    }
    expect(decodeActivityChanges({ field: { old: { a: 1, b: 2 }, new: { b: 2, a: 1 } } })[0]).toMatchObject({ unchanged: true });
    expect(decodeActivityChanges({ assignee: { old: "example", new: null } })[0]).toMatchObject({ kind: "diff", unchanged: false, new: null });
    expect(decodeActivityChanges({ field: { old: undefined, new: null } })[0]).toMatchObject({ unchanged: false });
  });

  it("formats a real expiry timestamp, identities and unrecognized metadata", () => {
    const date = new Date("2026-10-07T03:00:00Z");
    const formatted = formatActivityValue(date.toISOString());
    expect(formatted).toContain("07.10.2026");
    expect(formatted).toMatch(/\d{2}:\d{2}$/);
    expect(formatActivityValue("example", new Map([["example", "Example Developer"]]))).toBe("Example Developer");
    expect(formatActivityValue("2026-99-99T00:00:00Z")).toBe("2026-99-99T00:00:00Z");
    expect(formatActivityValue({ nested: [null, false] })).toBe('{"nested":[null,false]}');
  });

  it("names checkout events and preserves unknown events without invented changes", () => {
    const details = decodeActivityChanges({ ttl_min: 120 });
    expect(describeActivity("task.checkout_acquired", details)).toBe("Checkout acquired");
    expect(describeActivity("task.checkout_renewed", details)).toBe("Checkout renewed");
    expect(describeActivity("task.checkout_released", details)).toBe("Checkout released");
    expect(describeActivity("task.unrecognized_event", details)).toBe("task unrecognized event");
    expect(describeActivity("task.updated", decodeActivityChanges({ field: { old: null, new: null } }))).toContain("unchanged (none)");
  });
});
