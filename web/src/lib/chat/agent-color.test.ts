import { describe, expect, it } from "vitest";
import { agentColorClass, agentInitials } from "./agent-color";

describe("agent badge", () => {
  it("uses the agent's own avatar token", () => {
    expect(agentColorClass("avatar-3", "Vera")).toBe("bg-avatar-3");
  });
  it("falls back to a stable colour from the name for an unknown token", () => {
    expect(agentColorClass("bg-red-500", "Vera")).toBe(agentColorClass("", "Vera"));
    expect(agentColorClass("bg-red-500", "Vera")).toMatch(/^bg-avatar-[1-8]$/);
  });
  it("makes initials from one or two words", () => {
    expect(agentInitials("Vera")).toBe("VE");
    expect(agentInitials("Hana Laurie")).toBe("HL");
    expect(agentInitials("")).toBe("?");
  });
});
