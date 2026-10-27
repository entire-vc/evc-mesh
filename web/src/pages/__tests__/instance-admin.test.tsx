import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

const calls = vi.hoisted(() => ({ list: [] as { path: string; method: string; body?: unknown }[] }));
const server = vi.hoisted(() => ({
  forbidden: false,
  allowed: { gitlab: true, community: false } as Record<string, boolean>,
  autoUpdate: false,
  journal: [] as unknown[],
  settingsSource: "env" as "env" | "admin",
  withGithub: false,
  journalQueries: [] as Record<string, unknown>[],
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  const api = vi.fn(async (path: string, opts: { method?: string; body?: Record<string, unknown>; params?: Record<string, unknown> } = {}) => {
    const method = opts.method ?? "GET";
    calls.list.push({ path, method, body: opts.body });
    if (server.forbidden) throw new actual.ApiRequestError("forbidden", "403", 403);
    if (path === "/api/v1/extensions/instance/overview") {
      return {
        auto_update: server.autoUpdate,
        items: [
          { id: "gitlab", name: "GitLab", kind: "integration", source: "builtin", version: "1.0.0", trust_level: "builtin", allowed: server.allowed.gitlab, allow_explicit: false, has_instance_form: true, enabled: true, settings_source: server.settingsSource, enabled_workspaces: 4 },
          { id: "community", name: "Community Tool", kind: "plugin", source: "repo", trust_level: "community", allowed: server.allowed.community, allow_explicit: false, has_instance_form: false, enabled: false, settings_source: "none", enabled_workspaces: 0 },
          { id: "graphite", name: "Graphite", kind: "theme", source: "catalog", trust_level: "official", allowed: true, allow_explicit: false, has_instance_form: false, enabled: false, settings_source: "none", enabled_workspaces: 1 },
          { id: "withdrawn", name: "Withdrawn Tool", kind: "plugin", source: "catalog", trust_level: "verified", blocked: true, blocked_reason: "withdrawn by the publisher", allowed: true, allow_explicit: false, has_instance_form: false, enabled: true, settings_source: "none", enabled_workspaces: 2 },
          ...(server.withGithub ? [{ id: "github", name: "GitHub", kind: "integration", source: "builtin", trust_level: "builtin", allowed: true, allow_explicit: false, has_instance_form: true, enabled: true, settings_source: "admin" }] : []),
        ],
      };
    }
    if (path === "/api/v1/extensions/instance/journal") {
      const q = opts.params ?? {};
      server.journalQueries.push(q);
      const entries = (server.journal as { extension_id: string; action: string }[]).filter(
        (e) => (!q.extension || e.extension_id === q.extension) && (!q.action || e.action === q.action),
      );
      return {
        entries,
        ...(q.facets ? { facets: { actions: ["allow_changed", "settings_changed", "theme_selected"], actors: [{ id: "u1", name: "Ann Admin" }], extensions: ["community", "gitlab", "mesh-dark"] } } : {}),
      };
    }
    if (path === "/api/v1/extensions") {
      return { extensions: [{ id: "gitlab", name: "GitLab", manifest: { settings: { instance: { type: "object", properties: { base_url: { type: "string", title: "Base URL" } }, required: ["base_url"] } } } }] };
    }
    if (path === "/api/v1/extensions/gitlab/instance-settings" && method === "GET") {
      return { extension_id: "gitlab", settings: { base_url: "https://git.old" }, enabled: true, source: server.settingsSource, updated_at: "2026-09-03T10:00:00Z" };
    }
    if (path === "/api/v1/extensions/github/instance-settings" && method === "GET") {
      return { extension_id: "github", settings: {}, enabled: true, source: "admin", updated_at: "2026-10-20T10:00:00Z" };
    }
    if (path === "/api/v1/extensions/gitlab/instance-settings" && method === "PUT") {
      server.settingsSource = "admin";
      return {};
    }
    if (path.endsWith("/allowed") && method === "PUT") {
      const id = path.split("/")[4] ?? "";
      server.allowed[id] = Boolean(opts.body?.allowed);
      return {};
    }
    if (path === "/api/v1/extensions/instance/auto-update") {
      server.autoUpdate = Boolean(opts.body?.auto_update);
      return {};
    }
    throw new Error(`unexpected ${method} ${path}`);
  });
  return { ...actual, api };
});

import { setLanguage } from "@/i18n";
import { InstanceAdminPage, InstanceAdminRedirect } from "@/pages/instance-admin";
import { envMigratedDate } from "@/lib/extension-instance";

function renderPage(url = "/w/acme/instance") {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="w/:wsSlug/instance" element={<InstanceAdminPage />} />
        <Route path="w/:wsSlug/instance/extensions" element={<InstanceAdminRedirect tab="extensions" />} />
      </Routes>
    </MemoryRouter>,
  );
}

const openJournal = async () => fireEvent.click(await screen.findByRole("tab", { name: "Journal" }));

beforeEach(async () => {
  cleanup();
  calls.list = [];
  server.forbidden = false;
  server.allowed = { gitlab: true, community: false };
  server.autoUpdate = false;
  server.journal = [];
  server.settingsSource = "env";
  server.withGithub = false;
  server.journalQueries = [];
  await setLanguage("en");
});

describe("Instance administration", () => {
  it("one table: name, type, source, trust, allowed, workspaces, keys", async () => {
    renderPage();
    const gitlab = await screen.findByTestId("ext-row-gitlab");
    // type and trust are a meta line on a phone and cells on a desktop: both are in the DOM
    expect(within(gitlab).getAllByText("Integration").length).toBeGreaterThan(0);
    expect(within(gitlab).getByTestId("ext-source-gitlab").textContent).toBe("Built-in");
    expect(within(gitlab).getByTestId("ext-workspaces-gitlab").textContent).toBe("4");
    expect(within(gitlab).getByTestId("allow-state-gitlab").textContent).toBe("Enabled on the instance");
    // "Built-in" is both the source and the trust of a built-in extension
    expect(within(gitlab).getAllByText("Built-in").length).toBeGreaterThan(1);
    expect(within(gitlab).getByText("From env")).toBeTruthy();
    expect(within(gitlab).getByRole("button", { name: "Configure" })).toBeTruthy();
    const community = screen.getByTestId("ext-row-community");
    expect(within(community).getByTestId("ext-source-community").textContent).toBe("Repository");
    expect(within(community).getByTestId("allow-state-community").textContent).toBe("Not allowed");
    expect(within(community).getAllByText("Community").length).toBeGreaterThan(0);
    // a catalog block beats the allow-list and an enabled row, and says why
    const withdrawn = screen.getByTestId("ext-row-withdrawn");
    expect(within(withdrawn).getByTestId("allow-state-withdrawn").textContent).toBe("Blocked by the catalog");
    expect(within(withdrawn).getByText("withdrawn by the publisher")).toBeTruthy();
    expect(within(community).getByText("No instance keys")).toBeTruthy();
    expect(within(community).queryByRole("button", { name: "Configure" })).toBeNull();
    // no form is loaded until asked for
    expect(calls.list.some((c) => c.path.endsWith("/instance-settings"))).toBe(false);
  });

  it("filters by type, source and name, and says when nothing matches", async () => {
    renderPage();
    await screen.findByTestId("ext-row-gitlab");
    fireEvent.change(screen.getByRole("combobox", { name: "Type" }), { target: { value: "theme" } });
    expect(screen.getByTestId("ext-row-graphite")).toBeTruthy();
    expect(screen.queryByTestId("ext-row-gitlab")).toBeNull();
    expect(screen.getByText("1 of 4")).toBeTruthy();

    fireEvent.click(screen.getAllByRole("button", { name: "Reset filters" })[0]!);
    fireEvent.change(screen.getByRole("combobox", { name: "Source" }), { target: { value: "repo" } });
    expect(screen.getByTestId("ext-row-community")).toBeTruthy();
    expect(screen.queryByTestId("ext-row-graphite")).toBeNull();

    fireEvent.change(screen.getByRole("searchbox", { name: "Search by name" }), { target: { value: "zzz" } });
    expect(screen.getByText("Nothing matches these filters.")).toBeTruthy();
    fireEvent.click(screen.getAllByRole("button", { name: "Reset filters" })[0]!);
    expect(screen.getAllByRole("listitem")).toHaveLength(4);
  });

  it("?ext= opens that row's instance form, as linked from the wizard and from Installed", async () => {
    server.withGithub = true;
    renderPage("/w/acme/instance?ext=github");
    const row = await screen.findByTestId("ext-row-github");
    expect(within(row).getByRole("button", { name: "Close" }).getAttribute("aria-expanded")).toBe("true");
    expect(within(row).getByTestId("instance-settings")).toBeTruthy();
    await waitFor(() => expect(calls.list.some((c) => c.path === "/api/v1/extensions/github/instance-settings")).toBe(true));
    expect(calls.list.some((c) => c.path === "/api/v1/extensions/gitlab/instance-settings")).toBe(false);
  });

  it("the old /instance/extensions address keeps ?ext= and lands on the same row", async () => {
    server.withGithub = true;
    renderPage("/w/acme/instance/extensions?ext=github");
    const row = await screen.findByTestId("ext-row-github");
    expect(within(row).getByTestId("instance-settings")).toBeTruthy();
  });

  it("allowing a community extension sends the PUT and the row flips", async () => {
    renderPage();
    const row = await screen.findByTestId("ext-row-community");
    fireEvent.click(within(row).getByRole("switch", { name: "Allow Community Tool" }));
    await waitFor(() => expect(screen.getByTestId("allow-state-community").textContent).toBe("Available"));
    expect(calls.list.some((c) => c.path === "/api/v1/extensions/community/allowed" && c.method === "PUT")).toBe(true);
  });

  it("Configure opens the form; settings moved from env carry the DD.MM mark until an admin save", async () => {
    renderPage();
    const row = await screen.findByTestId("ext-row-gitlab");
    fireEvent.click(within(row).getByRole("button", { name: "Configure" }));
    const badge = await screen.findByTestId("env-badge");
    expect(badge.textContent).toBe("Moved from environment variables 03.09");
    expect((await screen.findByLabelText(/Base URL/) as HTMLInputElement).value).toBe("https://git.old");
    fireEvent.change(screen.getByLabelText(/Base URL/), { target: { value: "https://git.new" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(calls.list.some((c) => c.path === "/api/v1/extensions/gitlab/instance-settings" && c.method === "PUT")).toBe(true));
    await waitFor(() => expect(screen.queryByTestId("env-badge")).toBeNull());
    await waitFor(() => expect(within(screen.getByTestId("ext-row-gitlab")).getByText("Set")).toBeTruthy());
  });

  it("turns auto-update on through the API", async () => {
    renderPage();
    const sw = await screen.findByRole("switch", { name: "Update extensions automatically" });
    expect((sw as HTMLInputElement).checked).toBe(false);
    fireEvent.click(sw);
    await waitFor(() => expect(server.autoUpdate).toBe(true));
  });

  it("journal: who, what, in order, and the empty state", async () => {
    server.journal = [
      { id: "2", actor_name: "Ann Admin", extension_id: "community", action: "allow_changed", details: { allowed: true }, created_at: "2026-09-03T12:00:00Z" },
      { id: "1", actor_name: "Ann Admin", extension_id: "gitlab", action: "settings_changed", details: { keys: ["base_url", "token"] }, created_at: "2026-09-03T11:00:00Z" },
    ];
    renderPage();
    await openJournal();
    await waitFor(() => expect(within(screen.getByTestId("journal")).getAllByRole("listitem")).toHaveLength(2));
    const items = within(screen.getByTestId("journal")).getAllByRole("listitem");
    expect(items[0]?.textContent).toContain("Community Tool allowed");
    expect(items[1]?.textContent).toContain("Settings of GitLab changed: base_url, token");
    expect(items[0]?.textContent).toContain("Ann Admin");

    cleanup();
    server.journal = [];
    renderPage();
    await openJournal();
    expect(await screen.findByText("Nothing has been changed yet.")).toBeTruthy();
  });

  it("journal shows an enabled flip on its own and together with keys", async () => {
    server.journal = [
      { id: "4", actor_name: "Ann Admin", extension_id: "gitlab", action: "settings_changed", details: { keys: [], enabled: false }, created_at: "2026-09-03T13:00:00Z" },
      { id: "3", actor_name: "Ann Admin", extension_id: "gitlab", action: "settings_changed", details: { keys: ["base_url"], enabled: true }, created_at: "2026-09-03T12:00:00Z" },
    ];
    renderPage();
    await openJournal();
    await waitFor(() => expect(within(screen.getByTestId("journal")).getAllByRole("listitem")).toHaveLength(2));
    const items = within(screen.getByTestId("journal")).getAllByRole("listitem");
    expect(items[0]?.textContent).toContain("GitLab turned off at instance level");
    expect(items[0]?.textContent).not.toContain("changed:");
    expect(items[1]?.textContent).toContain("GitLab turned on at instance level. Settings of GitLab changed: base_url");
  });

  it("journal filters go to the server and narrow the list: theme noise drops out", async () => {
    server.journal = [
      { id: "9", actor_name: "Ann Admin", extension_id: "mesh-dark", action: "theme_selected", details: { level: "workspace" }, created_at: "2026-10-27T10:00:00Z" },
      { id: "8", actor_name: "Ann Admin", extension_id: "mesh-dark", action: "theme_selected", details: { level: "instance" }, created_at: "2026-10-27T09:00:00Z" },
      { id: "7", actor_name: "Ann Admin", extension_id: "community", action: "allow_changed", details: { allowed: false }, created_at: "2026-10-26T09:00:00Z" },
    ];
    renderPage();
    await openJournal();
    await waitFor(() => expect(within(screen.getByTestId("journal")).getAllByRole("listitem")).toHaveLength(3));
    expect(screen.getByText(/A workspace switched to the mesh-dark theme/)).toBeTruthy();
    expect(screen.getByText(/Default theme set to mesh-dark/)).toBeTruthy();
    expect(server.journalQueries[0]?.facets).toBe(1);

    fireEvent.change(screen.getByRole("combobox", { name: "Action" }), { target: { value: "allow_changed" } });
    await waitFor(() => expect(within(screen.getByTestId("journal")).getAllByRole("listitem")).toHaveLength(1));
    expect(screen.getByText(/Community Tool no longer allowed/)).toBeTruthy();

    fireEvent.change(screen.getByRole("combobox", { name: "Who" }), { target: { value: "u1" } });
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-10-01" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-10-26" } });
    await waitFor(() => expect(server.journalQueries[server.journalQueries.length - 1]).toMatchObject({ action: "allow_changed", actor: "u1", since: "2026-10-01", until: "2026-10-27" }));

    fireEvent.change(screen.getByRole("combobox", { name: "Extension" }), { target: { value: "gitlab" } });
    expect(await screen.findByText("No entries match these filters.")).toBeTruthy();
    fireEvent.click(screen.getAllByRole("button", { name: "Reset filters" })[0]!);
    await waitFor(() => expect(within(screen.getByTestId("journal")).getAllByRole("listitem")).toHaveLength(3));
  });

  it("a non-administrator sees the no-access state and none of the settings", async () => {
    server.forbidden = true;
    renderPage();
    expect(await screen.findByTestId("state-forbidden")).toBeTruthy();
    expect(screen.getByText("Instance administrators only")).toBeTruthy();
    expect(screen.queryByRole("tablist")).toBeNull();
    expect(screen.queryByTestId("allow-list")).toBeNull();
  });

  it("formats the migration date as DD.MM and tolerates a missing one", () => {
    expect(envMigratedDate("2026-01-09T23:59:00Z")).toBe("09.01");
    expect(envMigratedDate(undefined)).toBe("");
    expect(envMigratedDate("garbage")).toBe("");
  });
});
