import { describe, expect, it } from "vitest";
import { mergeCatalog, type SummaryDto, type WorkspaceDto } from "@/hooks/useExtensionCatalog";

const summary = (id: string): SummaryDto => ({
  id, name: id, version: "0.1.0", kind: "plugin", runtime: "external", publisher: "p", description: "",
  category: "development", source: "catalog", trust_level: "official", blocked: false, blocked_reason: "",
});

const ws = (id: string, over: Partial<WorkspaceDto>): WorkspaceDto => ({
  id, name: id, version: "0.1.0", kind: "plugin", category: "development", ...over,
});

describe("mergeCatalog connected", () => {
  it("an instance-level setting is not a workspace connection", () => {
    const row = mergeCatalog(
      [summary("keep")],
      [ws("keep", { status: { enabled: true, source: "instance" } as WorkspaceDto["status"], connection: null })],
    )[0]!;
    expect(row.installed).toBe(true);
    expect(row.connected).toBe(false);
  });

  it("a workspace connection row is connected", () => {
    const row = mergeCatalog(
      [summary("keep-rotator")],
      [ws("keep-rotator", {
        status: { enabled: true, source: "workspace" } as WorkspaceDto["status"],
        connection: { id: "c1" } as WorkspaceDto["connection"],
      })],
    )[0]!;
    expect(row.connected).toBe(true);
  });

  it("a built-in instance-managed extension still reads as connected", () => {
    const row = mergeCatalog(
      [{ ...summary("gitlab"), runtime: "builtin" }],
      [ws("gitlab", { status: { enabled: true, source: "instance" } as WorkspaceDto["status"], connection: null })],
    )[0]!;
    expect(row.connected).toBe(true);
  });
});
