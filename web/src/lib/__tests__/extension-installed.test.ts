import { describe, expect, it } from "vitest";
import type { CatalogEntry } from "@/hooks/useExtensionCatalog";
import { formatNumber } from "@/i18n/format";
import { receivingNote, workspaceSettingsSchema } from "@/lib/extension-installed";
import { consentTokens } from "@/lib/extension-capabilities";

const base = {
  id: "gitlab", name: "GitLab", blocked: false, blockedReason: "", connected: false, installed: true,
  status: undefined, connection: null, manifest: { capabilities: {} },
} as unknown as CatalogEntry;
const e = (over: Record<string, unknown>) => ({ ...base, ...over }) as unknown as CatalogEntry;

describe("receivingNote", () => {
  it("links without the extension on read as receiving events, not unused", () => {
    const n = receivingNote(e({ activity: { links: 1748, last_event_at: "2026-10-24T12:00:00Z" } }));
    expect(n?.key).toBe("installed.receiving");
    expect(n?.values.count).toBe(formatNumber(1748));
    expect(receivingNote(e({ activity: { links: 0 } }))).toBeNull();
    expect(receivingNote(e({ status: { enabled: true, source: "instance" }, activity: { links: 5 } }))).toBeNull();
  });
});

describe("workspaceSettingsSchema", () => {
  it("returns the workspace schema, null when the level is not offered or absent", () => {
    const schema = { type: "object" };
    expect(workspaceSettingsSchema(e({ manifest: { settings: { workspace: schema } } }))).toBe(schema);
    expect(workspaceSettingsSchema(e({ manifest: { levels: { workspace: false }, settings: { workspace: schema } } }))).toBeNull();
    expect(workspaceSettingsSchema(e({ manifest: {} }))).toBeNull();
  });
});

describe("consentTokens", () => {
  it("is sorted kind:value so it matches what the server computes", () => {
    const tokens = consentTokens({ api: ["tasks:write", "agents:read"], events: ["task.created"], secrets: ["token"] } as never);
    expect(tokens).toEqual(["agents:read".replace(/^/, "api:"), "api:tasks:write", "events:task.created", "secrets:token"].sort());
  });
});
