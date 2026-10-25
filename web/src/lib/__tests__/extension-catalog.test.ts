import { describe, expect, it } from "vitest";
import { mergeCatalog, trustOf, type WorkspaceDto } from "@/hooks/useExtensionCatalog";
import { KINDS, applyFilters, filtersFromParams, filtersToParams } from "@/lib/extension-catalog-filter";
import { allowedDomains, describeCapabilities } from "@/lib/extension-capabilities";

const summary = (id: string, over: Record<string, unknown> = {}) => ({
  id, name: id.toUpperCase(), version: "1.0.0", kind: "integration", runtime: "builtin",
  publisher: "Entire VC", description: `${id} description`, category: "development",
  source: "builtin", trust_level: "builtin", blocked: false, blocked_reason: "", ...over,
});

const ws = (id: string, over: Partial<WorkspaceDto> = {}): WorkspaceDto => ({
  id, name: id.toUpperCase(), version: "1.0.0", kind: "integration", category: "development",
  manifest: { runtime: "builtin", publisher: "Entire VC", description: `${id} description` } as WorkspaceDto["manifest"],
  ...over,
});

const entries = mergeCatalog(
  [
    summary("gitlab"),
    summary("telegram", { category: "communication", kind: "plugin" }),
    summary("runner", { category: "agents", created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-10T00:00:00Z" }),
    summary("custom-x", { category: "security", source: "repo", trust_level: "custom", runtime: "external" }),
  ],
  [
    ws("gitlab"), ws("runner"),
    ws("telegram", { connection: {}, status: { enabled: true, source: "workspace" } }),
  ],
);

describe("catalog filters", () => {
  it("rubric filter changes the set by data, not presence", () => {
    const all = applyFilters(entries, filtersFromParams(new URLSearchParams()));
    expect(all.map((e) => e.id).sort()).toEqual(["custom-x", "gitlab", "runner", "telegram"]);
    const dev = applyFilters(entries, filtersFromParams(new URLSearchParams("cat=development")));
    expect(dev.map((e) => e.id)).toEqual(["gitlab"]);
    const two = applyFilters(entries, filtersFromParams(new URLSearchParams("cat=agents,security")));
    expect(two.map((e) => e.id).sort()).toEqual(["custom-x", "runner"]);
  });

  it("filters by kind and trust and searches name/description", () => {
    expect(applyFilters(entries, filtersFromParams(new URLSearchParams("kind=plugin"))).map((e) => e.id)).toEqual(["telegram"]);
    expect(applyFilters(entries, filtersFromParams(new URLSearchParams("trust=custom"))).map((e) => e.id)).toEqual(["custom-x"]);
    expect(applyFilters(entries, filtersFromParams(new URLSearchParams("q=RUNNER"))).map((e) => e.id)).toEqual(["runner"]);
    expect(applyFilters(entries, filtersFromParams(new URLSearchParams("q=nothing-like-this")))).toEqual([]);
  });

  it("offers theme as a third kind and filters to exactly the themes", () => {
    expect(KINDS).toEqual(["integration", "plugin", "theme"]);
    const withThemes = mergeCatalog(
      [
        summary("gitlab"),
        summary("telegram", { kind: "plugin" }),
        summary("graphite", { kind: "theme", category: "appearance" }),
        summary("board-studio", { kind: "theme", category: "appearance" }),
      ],
      [],
    );
    const themes = applyFilters(withThemes, filtersFromParams(new URLSearchParams("kind=theme")));
    expect(themes.map((e) => e.id).sort()).toEqual(["board-studio", "graphite"]);
    const integrations = applyFilters(withThemes, filtersFromParams(new URLSearchParams("kind=integration")));
    expect(integrations.map((e) => e.id)).toEqual(["gitlab"]);
  });

  it("sorts popular (connected first), newest and recently updated", () => {
    const ids = (sort: string) => applyFilters(entries, filtersFromParams(new URLSearchParams(`sort=${sort}`))).map((e) => e.id);
    expect(ids("popular")[0]).toBe("telegram");
    expect(ids("new")[0]).toBe("runner");
    expect(ids("updated")[0]).toBe("runner");
  });

  it("round-trips through the URL and keeps unrelated params", () => {
    const f = filtersFromParams(new URLSearchParams("cat=agents&sort=new&q=ru"));
    const p = filtersToParams(new URLSearchParams("tab=installed"), f);
    expect(p.get("tab")).toBe("installed");
    expect(filtersFromParams(p)).toEqual(f);
    expect(filtersToParams(new URLSearchParams("sort=new"), { ...f, sort: "popular", categories: [], q: "" }).toString()).toBe("");
  });

  it("lists registered built-ins even when the instance list is empty", () => {
    // Defect seen on a fresh stack: installed_extensions was never synced, so
    // GET /extensions answered [] and the catalog showed nothing.
    const rows = mergeCatalog([], [ws("gitlab"), ws("github")]);
    expect(rows.map((r) => r.id)).toEqual(["gitlab", "github"]);
    expect(rows[0]).toMatchObject({ trust: "official", runtime: "builtin", publisher: "Entire VC", installed: true });
  });

  it("maps trust levels", () => {
    expect(trustOf("builtin", "builtin")).toBe("official");
    expect(trustOf("", "repo")).toBe("custom");
    expect(trustOf("verified", "catalog")).toBe("verified");
  });

  it("derives allowed from blocked when the API omits the field, explicit value wins", () => {
    const rows = mergeCatalog(
      [summary("a"), summary("b", { blocked: true }), summary("c", { blocked: true, allowed: true })],
      [],
    );
    expect(rows.map((r) => r.allowed)).toEqual([true, false, true]);
  });

  it("marks connected from the workspace row and installed from the instance list", () => {
    expect(entries.find((e) => e.id === "telegram")?.connected).toBe(true);
    expect(entries.find((e) => e.id === "gitlab")?.connected).toBe(false);
    const withExtra = mergeCatalog([summary("gitlab")], [ws("gitlab")], [summary("remote", { source: "catalog", trust_level: "verified" })]);
    expect(withExtra.find((e) => e.id === "remote")?.installed).toBe(false);
    expect(withExtra.find((e) => e.id === "gitlab")?.installed).toBe(true);
  });
});

describe("capabilities dictionary", () => {
  it("describes every group, flags unknown tokens, lists domains apart", () => {
    const caps = {
      api: ["tasks:read", "comments:write", "bogus:read"],
      events: ["task.updated"], webhooks_in: ["gitlab"], ui: ["task.panel"], secrets: ["token"],
      network: { allowed_domains: ["gitlab.com"] },
    };
    const keys = describeCapabilities(caps).map((l) => l.key);
    expect(keys).toEqual([
      "capabilities.api.tasks.read", "capabilities.api.comments.write", "capabilities.api.unknown",
      "capabilities.events.listen", "capabilities.webhooks.receive", "capabilities.ui.task_panel",
      "capabilities.secrets.store",
    ]);
    expect(allowedDomains(caps)).toEqual(["gitlab.com"]);
    expect(describeCapabilities(undefined)).toEqual([]);
  });
});
