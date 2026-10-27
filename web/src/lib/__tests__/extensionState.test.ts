import { describe, expect, it } from "vitest";
import { extensionState, type ExtensionStateInput } from "@/lib/extensionState";

type Status = NonNullable<ExtensionStateInput["status"]>;
const ok = { ok: true, message: "" };
const bad = { ok: false, message: "401 Unauthorized" };

// Every combination of allowed/blocked/source/connection/health/kind, with the state it must read as.
const kinds = ["integration", "plugin", "theme"] as const;
// no row, the workspace's own row, an empty row an instance-governed VCS extension carries
const connections = [null, { settings: { token: "***" } }, { settings: {} }] as const;
const statuses: Array<[string, Status | undefined]> = [
  ["none", undefined],
  ["off", { enabled: false, source: "none" }],
  ["off-own", { enabled: false, source: "workspace" }],
  ["instance-ok", { enabled: true, source: "instance", health: ok }],
  ["own-ok", { enabled: true, source: "workspace", health: ok }],
  ["instance-unchecked", { enabled: true, source: "instance" }],
  ["instance-bad", { enabled: true, source: "instance", health: bad, last_error: "401 Unauthorized" }],
  ["own-bad", { enabled: true, source: "workspace", health: bad }],
  ["own-stale-error", { enabled: true, source: "workspace", health: ok, last_error: "timeout" }],
];

function expected(kind: string, allowed: boolean, blocked: boolean, name: string, themeActive?: boolean) {
  if (blocked) return { state: "blocked", blockedReason: "CVE-1" };
  if (!allowed) return { state: "not_allowed" };
  const st = statuses.find(([n]) => n === name)![1];
  if (kind === "theme") return { state: (themeActive ?? Boolean(st?.enabled)) ? "enabled" : "available" };
  if (!st?.enabled) return { state: "available" };
  const keysFrom = st.source === "instance" ? "instance" : "workspace";
  if (name.endsWith("bad")) return { state: "error", keysFrom, error: "401 Unauthorized" };
  if (name === "own-stale-error") return { state: "error", keysFrom, error: "timeout" };
  return { state: "enabled", keysFrom };
}

describe("extensionState: full table", () => {
  for (const kind of kinds)
    for (const allowed of [true, false])
      for (const blocked of [false, true])
        for (const [name, status] of statuses)
          for (const connection of connections)
          for (const themeActive of kind === "theme" ? [undefined, true, false] : [undefined]) {
            const conn = connection === null ? "no-row" : Object.keys(connection.settings).length ? "own-row" : "empty-row";
            const label = `${kind} allowed=${allowed} blocked=${blocked} ${name} ${conn}${themeActive === undefined ? "" : ` themeActive=${themeActive}`}`;
            it(label, () => {
              const got = extensionState({ kind, allowed, blocked, blockedReason: "CVE-1", status, themeActive, connection });
              expect(got).toEqual(expected(kind, allowed, blocked, name, themeActive));
            });
          }
});

describe("extensionState: rules spelled out", () => {
  it("instance keys and own keys are both 'enabled', told apart by keysFrom", () => {
    expect(extensionState({ allowed: true, blocked: false, status: { enabled: true, source: "instance", health: ok } }))
      .toEqual({ state: "enabled", keysFrom: "instance" });
    expect(extensionState({ allowed: true, blocked: false, status: { enabled: true, source: "workspace", health: ok } }))
      .toEqual({ state: "enabled", keysFrom: "workspace" });
  });
  it("a last error with no health message still reads as error with that text", () => {
    expect(extensionState({ allowed: true, blocked: false, status: { enabled: true, source: "instance", last_error: "401" } }))
      .toMatchObject({ state: "error", error: "401" });
  });
  it("the health message stands in when there is no last error", () => {
    expect(extensionState({ allowed: true, blocked: false, status: { enabled: true, source: "workspace", health: bad } }).error)
      .toBe("401 Unauthorized");
  });
  it("a connection row alone does not switch an extension on, and keys follow the resolved source", () => {
    expect(extensionState({ allowed: true, blocked: false, connection: { settings: {} }, status: { enabled: false, source: "workspace" } }).state)
      .toBe("available");
    expect(extensionState({ allowed: true, blocked: false, connection: { settings: {} }, status: { enabled: true, source: "instance", health: ok } }))
      .toEqual({ state: "enabled", keysFrom: "instance" });
  });
  it("a block without a reason keeps an empty reason", () => {
    expect(extensionState({ allowed: true, blocked: true })).toEqual({ state: "blocked", blockedReason: "" });
  });
  it("blocked beats not allowed, not allowed beats an enabled extension", () => {
    expect(extensionState({ allowed: false, blocked: true, blockedReason: "x" }).state).toBe("blocked");
    expect(extensionState({ allowed: false, blocked: false, status: { enabled: true, source: "instance", health: ok } }).state).toBe("not_allowed");
  });
});
