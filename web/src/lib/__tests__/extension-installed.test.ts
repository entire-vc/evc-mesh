import { describe, expect, it } from "vitest";
import type { CatalogEntry } from "@/hooks/useExtensionCatalog";
import { formatNumber } from "@/i18n/format";
import { installedStatus, workspaceSettingsSchema } from "@/lib/extension-installed";
import { consentTokens } from "@/lib/extension-capabilities";

const base = {
  id: "gitlab", name: "GitLab", blocked: false, blockedReason: "", connected: false, installed: true,
  status: undefined, connection: null, manifest: { capabilities: {} },
} as unknown as CatalogEntry;
const e = (over: Record<string, unknown>) => ({ ...base, ...over }) as unknown as CatalogEntry;

describe("installedStatus", () => {
  it("instance-sourced enabled extension says it is set up by the instance admin", () => {
    expect(installedStatus(e({ status: { enabled: true, source: "instance", health: { ok: true } } })).key)
      .toBe("installed.status.instance");
  });
  it("workspace-sourced enabled extension", () => {
    expect(installedStatus(e({ status: { enabled: true, source: "workspace", health: { ok: true } } })).key)
      .toBe("installed.status.workspace");
  });
  it("a failing health check beats 'enabled', a catalog block beats everything", () => {
    const bad = installedStatus(e({ status: { enabled: true, source: "instance", health: { ok: false, message: "x" } } }));
    expect(bad).toMatchObject({ kind: "error", values: { message: "x" } });
    expect(installedStatus(e({ blocked: true, blockedReason: "CVE", status: { enabled: true, source: "instance" } })).kind)
      .toBe("blocked");
  });
  it("a theme keeps its workspace status when the catalog blocks it", () => {
    expect(installedStatus(e({ kind: "theme", themeActive: true, blocked: true, blockedReason: "CVE" })).kind).toBe("theme-active");
    expect(installedStatus(e({ kind: "theme", themeActive: false, blocked: true, blockedReason: "CVE" })).kind).toBe("theme-inactive");
    // status unknown: the block is all there is to say
    expect(installedStatus(e({ kind: "theme", blocked: true, blockedReason: "CVE" })).kind).toBe("blocked");
  });
  it("disabled-with-connection is 'off', nothing at all is 'none'", () => {
    expect(installedStatus(e({ connected: true, status: { enabled: false, source: "workspace" } })).kind).toBe("off");
    expect(installedStatus(e({})).kind).toBe("none");
  });
  it("links without any connection row read as receiving events, not 'not connected'", () => {
    const st = installedStatus(e({ activity: { links: 1748, last_event_at: "2026-10-24T12:00:00Z" } }));
    expect(st.kind).toBe("receiving");
    expect(st.values?.count).toBe(formatNumber(1748));
    expect(installedStatus(e({ activity: { links: 0 } })).kind).toBe("none");
    expect(installedStatus(e({ connected: true, status: { enabled: false, source: "workspace" }, activity: { links: 5 } })).kind).toBe("off");
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
