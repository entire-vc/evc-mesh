import { describe, expect, it } from "vitest";
import en from "@/locales/en/integrations.json";
import ru from "@/locales/ru/integrations.json";
import { describeCapabilities } from "@/lib/extension-capabilities";
import schema from "../../../../internal/extensions/schema/manifest.v1.json";

function get(obj: unknown, path: string): unknown {
  return path.split(".").reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], obj);
}

describe("capability wording covers the manifest schema", () => {
  const caps = schema.properties.capabilities.properties;
  const apiPattern = (caps.api.items.pattern.match(/\(([a-z_|]+)\):/)?.[1] ?? "").split("|");
  const slots = schema.$defs.slot.enum;
  const lines = describeCapabilities({
    api: apiPattern.flatMap((r) => [`${r}:read`, `${r}:write`]),
    events: ["task.updated"], webhooks_in: ["x"], secrets: ["s"], ui: slots,
  });

  it("has an en and ru string for every api resource, slot and group", () => {
    expect(lines.length).toBe(apiPattern.length * 2 + slots.length + 3);
    for (const l of lines) {
      expect(l.key).not.toContain("unknown");
      expect(get(en, l.key), `en ${l.key}`).toBeTypeOf("string");
      expect(get(ru, l.key), `ru ${l.key}`).toBeTypeOf("string");
    }
  });
});
