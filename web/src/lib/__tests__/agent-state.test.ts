import { describe, expect, it } from "vitest";
import { agentState } from "../agent-state";

// Production keeps busy in heartbeat_status; agents.status is online/offline.
describe("agentState", () => {
  it.each([
    [{ status: "online", heartbeat_status: "busy" }, "busy"],
    [{ status: "online", heartbeat_status: "working" }, "busy"],
    [{ status: "online", heartbeat_status: "idle" }, "online"],
    [{ status: "offline", heartbeat_status: "busy" }, "offline"],
  ] as const)("%o → %s", (input, want) => {
    expect(agentState({ ...input, is_stale: false })).toBe(want);
  });

  it("a stale heartbeat is offline whatever it said", () => {
    expect(agentState({ status: "online", heartbeat_status: "busy", is_stale: true })).toBe("offline");
  });

  it("no heartbeat status leaves the row status; case does not matter", () => {
    expect(agentState({ status: "online" })).toBe("online");
    expect(agentState({ status: "online", heartbeat_status: "" })).toBe("online");
    expect(agentState({ status: "ONLINE", heartbeat_status: "Working" })).toBe("busy");
  });

  it("an error from the row or the heartbeat is error; a missing row is offline", () => {
    expect(agentState({ status: "error" })).toBe("error");
    expect(agentState({ status: "online", heartbeat_status: "error" })).toBe("error");
    expect(agentState({})).toBe("offline");
  });

  it("busy comes only from the heartbeat: a busy row without one is online", () => {
    expect(agentState({ status: "busy" })).toBe("online");
    expect(agentState({ status: "busy", heartbeat_status: "idle" })).toBe("online");
    expect(agentState({ status: "busy", heartbeat_status: "working" })).toBe("busy");
  });
});
