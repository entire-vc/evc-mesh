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
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  const api = vi.fn(async (path: string, opts: { method?: string; body?: Record<string, unknown> } = {}) => {
    const method = opts.method ?? "GET";
    calls.list.push({ path, method, body: opts.body });
    if (server.forbidden) throw new actual.ApiRequestError("forbidden", "403", 403);
    if (path === "/api/v1/extensions/instance/overview") {
      return {
        auto_update: server.autoUpdate,
        items: [
          { id: "gitlab", name: "GitLab", kind: "integration", trust_level: "builtin", allowed: server.allowed.gitlab, allow_explicit: false, has_instance_form: true, enabled: true, settings_source: server.settingsSource },
          { id: "community", name: "Community Tool", kind: "plugin", trust_level: "community", allowed: server.allowed.community, allow_explicit: false, has_instance_form: false, enabled: false, settings_source: "none" },
          ...(server.withGithub ? [{ id: "github", name: "GitHub", kind: "integration", trust_level: "builtin", allowed: true, allow_explicit: false, has_instance_form: true, enabled: true, settings_source: "admin" }] : []),
        ],
      };
    }
    if (path === "/api/v1/extensions/instance/journal") return { entries: server.journal };
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
import { ExtensionInstanceSettingsPage } from "@/pages/extension-instance-settings";
import { envMigratedDate } from "@/lib/extension-instance";

function renderPage(url = "/w/acme/instance/extensions") {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="w/:wsSlug/instance/extensions" element={<ExtensionInstanceSettingsPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(async () => {
  cleanup();
  calls.list = [];
  server.forbidden = false;
  server.allowed = { gitlab: true, community: false };
  server.autoUpdate = false;
  server.journal = [];
  server.settingsSource = "env";
  server.withGithub = false;
  await setLanguage("en");
});

describe("Instance extension settings page", () => {
  it("opens on the extension named in ?ext=, as linked from a workspace's Settings dialog", async () => {
    server.withGithub = true;
    renderPage("/w/acme/instance/extensions?ext=github");
    await waitFor(() => expect((screen.getByLabelText("Extension") as HTMLSelectElement).value).toBe("github"));
    await waitFor(() => expect(calls.list.some((c) => c.path === "/api/v1/extensions/github/instance-settings")).toBe(true));
    expect(calls.list.some((c) => c.path === "/api/v1/extensions/gitlab/instance-settings")).toBe(false);
  });

  it("without ?ext= it opens on the first extension with a form", async () => {
    server.withGithub = true;
    renderPage();
    await waitFor(() => expect((screen.getByLabelText("Extension") as HTMLSelectElement).value).toBe("gitlab"));
  });

  it("shows the allow-list from data: official on, community off, by default", async () => {
    renderPage();
    const gitlab = await screen.findByTestId("allow-row-gitlab");
    expect(within(gitlab).getByTestId("allow-state-gitlab").textContent).toBe("Allowed");
    expect(within(screen.getByTestId("allow-row-community")).getByTestId("allow-state-community").textContent).toBe("Not allowed");
    expect(within(gitlab).getByText("Official")).toBeTruthy();
    expect(within(screen.getByTestId("allow-row-community")).getByText("Community")).toBeTruthy();
  });

  it("allowing a community extension sends the PUT and the row flips", async () => {
    renderPage();
    const row = await screen.findByTestId("allow-row-community");
    fireEvent.click(within(row).getByRole("switch", { name: "Allow Community Tool" }));
    await waitFor(() => expect(within(screen.getByTestId("allow-state-community").parentElement as HTMLElement).getByText("Allowed")).toBeTruthy());
    expect(calls.list.find((c) => c.method === "PUT" && c.path === "/api/v1/extensions/community/allowed")?.body).toEqual({ allowed: true });
    expect(server.allowed.community).toBe(true);
  });

  it("marks settings moved from env with the DD.MM date, and drops the mark after an admin save", async () => {
    renderPage();
    const badge = await screen.findByTestId("env-badge");
    expect(badge.textContent).toBe("Moved from environment variables 03.09");
    expect((await screen.findByLabelText(/Base URL/) as HTMLInputElement).value).toBe("https://git.old");

    fireEvent.change(screen.getByLabelText(/Base URL/), { target: { value: "https://git.new" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(calls.list.some((c) => c.method === "PUT" && c.path === "/api/v1/extensions/gitlab/instance-settings")).toBe(true));
    const put = calls.list.find((c) => c.method === "PUT" && c.path === "/api/v1/extensions/gitlab/instance-settings");
    expect(put?.body).toEqual({ enabled: true, settings: { base_url: "https://git.new" } });
    await waitFor(() => expect(screen.queryByTestId("env-badge")).toBeNull());
  });

  it("turns auto-update on through the API", async () => {
    renderPage();
    const sw = await screen.findByRole("switch", { name: "Update extensions automatically" });
    expect((sw as HTMLInputElement).checked).toBe(false);
    fireEvent.click(sw);
    await waitFor(() => expect(server.autoUpdate).toBe(true));
  });

  it("renders the journal: who, what, in order, and the empty state", async () => {
    server.journal = [
      { id: "2", actor_name: "Ann Admin", extension_id: "community", action: "allow_changed", details: { allowed: true }, created_at: "2026-09-03T12:00:00Z" },
      { id: "1", actor_name: "Ann Admin", extension_id: "gitlab", action: "settings_changed", details: { keys: ["base_url", "token"] }, created_at: "2026-09-03T11:00:00Z" },
    ];
    renderPage();
    const items = within(await screen.findByTestId("journal")).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]?.textContent).toContain("Community Tool allowed");
    expect(items[1]?.textContent).toContain("Settings of GitLab changed: base_url, token");
    expect(items[0]?.textContent).toContain("Ann Admin");

    cleanup();
    server.journal = [];
    renderPage();
    expect(await screen.findByText("Nothing has been changed yet.")).toBeTruthy();
  });

  it("journal shows an enabled flip on its own and together with keys", async () => {
    server.journal = [
      { id: "4", actor_name: "Ann Admin", extension_id: "gitlab", action: "settings_changed", details: { keys: [], enabled: false }, created_at: "2026-09-03T13:00:00Z" },
      { id: "3", actor_name: "Ann Admin", extension_id: "gitlab", action: "settings_changed", details: { keys: ["base_url"], enabled: true }, created_at: "2026-09-03T12:00:00Z" },
    ];
    renderPage();
    const items = within(await screen.findByTestId("journal")).getAllByRole("listitem");
    expect(items[0]?.textContent).toContain("GitLab turned off at instance level");
    expect(items[0]?.textContent).not.toContain("changed:");
    expect(items[1]?.textContent).toContain("GitLab turned on at instance level. Settings of GitLab changed: base_url");
  });

  it("a non-administrator sees the no-access state and none of the settings", async () => {
    server.forbidden = true;
    renderPage();
    expect(await screen.findByTestId("state-forbidden")).toBeTruthy();
    expect(screen.getByText("Instance administrators only")).toBeTruthy();
    expect(screen.queryByTestId("allow-list")).toBeNull();
    expect(screen.queryByTestId("instance-settings")).toBeNull();
  });

  it("formats the migration date as DD.MM and tolerates a missing one", () => {
    expect(envMigratedDate("2026-01-09T23:59:00Z")).toBe("09.01");
    expect(envMigratedDate(undefined)).toBe("");
    expect(envMigratedDate("garbage")).toBe("");
  });
});
