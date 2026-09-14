import { describe, expect, it } from "vitest";
import { agentTypeConfig, agentTypeDisplay } from "@/lib/agent-utils";

describe("agentTypeDisplay", () => {
  it("returns the configured label/color for every known AgentType", () => {
    for (const [type, config] of Object.entries(agentTypeConfig)) {
      expect(agentTypeDisplay(type)).toEqual(config);
    }
  });

  // Spark marketplace manifests type agent_type as `AgentType | string` — a
  // listing can name a harness we don't recognize yet. This is the fallback
  // path that used to live duplicated in web/src/pages/spark.tsx.
  it("falls back to the raw string as label for an unrecognized type", () => {
    expect(agentTypeDisplay("some-future-harness")).toEqual({
      label: "some-future-harness",
      color: "bg-gray-100 text-gray-700",
    });
  });
});
