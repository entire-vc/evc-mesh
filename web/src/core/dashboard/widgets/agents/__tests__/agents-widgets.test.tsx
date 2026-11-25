import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { CoreActionsProvider } from "@/core/primitives";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type { AgentStatusEntry } from "../agent-card";
import { descriptors } from "..";
import { views } from "../views";
import { filterAgents, summarizeAgents, ACTIVE_LIMIT, AGENT_STATUSES, AGENTS_POLL_MS } from "../use-agents";

// The network is the only mock: every widget runs its real data hook.
vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";

const agent = (id: string, o: Partial<AgentStatusEntry> = {}): AgentStatusEntry => ({
  id,
  name: `Agent ${id}`,
  status: "online",
  agent_type: "codex",
  last_heartbeat_at: new Date().toISOString(),
  seconds_since_heartbeat: 5,
  is_stale: false,
  ...o,
});

// Production shape: busy lives in heartbeat_status, agents.status is online/offline.
const FLEET: AgentStatusEntry[] = [
  agent("a1", { name: "Liam", status: "online", heartbeat_status: "busy", agent_type: "claude_code", heartbeat_message: "Working on Fix login", current_task_id: "t1", current_task_title: "Fix login" }),
  agent("a2", { name: "Vera", status: "online", heartbeat_status: "idle", agent_type: "codex" }),
  agent("a3", { name: "Ghost", status: "online", agent_type: "", is_stale: true, last_heartbeat_at: new Date(Date.now() - 3600_000).toISOString() }),
  agent("a4", { name: "Broken", status: "error", agent_type: "cursor" }),
];

type Mode = "normal" | "empty" | "error";
let mode: Mode = "normal";
function serve() {
  vi.mocked(api).mockImplementation(async (url: string) => {
    if (mode === "error") throw new Error("boom");
    if (url.endsWith("/agents/status")) return { agents: mode === "empty" ? [] : FLEET };
    if (url.endsWith("/team")) {
      return {
        agents:
          mode === "empty"
            ? []
            : [
                { id: "a1", name: "Liam", status: "online", heartbeat_status: "busy", heartbeat_message: "Working on Fix login", is_stale: false, short_tag: "mesh-dev" },
                { id: "a2", name: "Vera", status: "online", is_stale: false },
                { id: "a3", name: "Ghost", status: "online", is_stale: true },
                { id: "a5", name: "Hugh", status: "online", is_stale: false },
                // A guest of this workspace: in /team, not in /agents/status.
                { id: "g1", name: "Guest", status: "online", heartbeat_status: "busy", is_stale: false, is_home: false },
              ],
      };
    }
    return {};
  });
}

type Id = keyof typeof views;
function mount(id: Id, settings: Record<string, unknown> = {}) {
  const View = views[id];
  return render(
    <MemoryRouter>
      <View settings={settings} />
    </MemoryRouter>,
  );
}
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 20)); });
const retryButton = (c: HTMLElement) => [...c.querySelectorAll("button")].find((b) => /retry|повтор/i.test(b.textContent ?? "")) ?? null;

beforeEach(() => {
  mode = "normal";
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useRulesStore.setState({ teamDirectory: null });
  serve();
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("agent widgets: descriptors", () => {
  it("declare the three ids with PRD §5.8 sizes on the 12-column grid", () => {
    expect(Object.fromEntries(descriptors.map((d) => [d.id, d.size]))).toEqual({
      "agents-active": { default: { w: 6, h: 4 }, min: { w: 3, h: 2 }, max: { w: 12, h: 8 } },
      "agents-status": { default: { w: 12, h: 6 }, min: { w: 4, h: 3 }, max: { w: 12, h: 12 } },
      "agents-summary": { default: { w: 4, h: 2 }, min: { w: 3, h: 1 }, max: { w: 6, h: 2 } },
    });
    expect(Object.keys(views).sort()).toEqual(descriptors.map((d) => d.id).sort());
  });

  it("settings: limit for agents-active, status filter and only-with-task for agents-status, none for the summary", () => {
    const byId = Object.fromEntries(descriptors.map((d) => [d.id, d]));
    expect(Object.keys(byId["agents-active"]!.settings!.properties!)).toEqual(["limit"]);
    expect(Object.keys(byId["agents-status"]!.settings!.properties!)).toEqual(["statuses", "onlyWithTask"]);
    expect((byId["agents-status"]!.settings!.properties!.statuses!.items as { enum: string[] }).enum).toEqual(["online", "busy", "offline", "error"]);
    expect(byId["agents-summary"]!.settings).toBeUndefined();
  });

  it("schema.ts repeats the hook's bounds and statuses (contract C keeps use-agents out of index.ts)", () => {
    const byId = Object.fromEntries(descriptors.map((d) => [d.id, d]));
    const limit = byId["agents-active"]!.settings!.properties!.limit as { minimum: number; maximum: number; default: number };
    expect({ min: limit.minimum, max: limit.maximum, default: limit.default }).toEqual(ACTIVE_LIMIT);
    const items = byId["agents-status"]!.settings!.properties!.statuses!.items as { enum: string[]; "x-options": Record<string, string> };
    expect(items.enum).toEqual([...AGENT_STATUSES]);
    expect(Object.keys(items["x-options"])).toEqual([...AGENT_STATUSES]);
  });
});

describe.each(["agents-active", "agents-status", "agents-summary"] as const)("%s", (id) => {
  it("CT-widget-01 loading, error and empty are three different views; retry only in error and it reloads", async () => {
    let release: () => void = () => {};
    vi.mocked(api).mockImplementationOnce(() => new Promise((r) => { release = () => r({ agents: [] }); }));
    const loading = mount(id);
    expect(loading.container.querySelector('[data-testid="widget-loading"]')).not.toBeNull();
    const html = { loading: loading.container.innerHTML };
    expect(retryButton(loading.container)).toBeNull();
    release();
    cleanup();

    mode = "empty";
    const empty = mount(id);
    await settle();
    html["empty" as "loading"] = empty.container.innerHTML;
    expect(retryButton(empty.container)).toBeNull();
    cleanup();

    mode = "error";
    const error = mount(id);
    await settle();
    expect(new Set([html.loading, (html as Record<string, string>).empty, error.container.innerHTML]).size).toBe(3);
    expect(screen.getByRole("alert").textContent).toMatch(/could not load agents/i);
    const retry = retryButton(error.container);
    expect(retry).not.toBeNull();
    mode = "normal";
    const before = vi.mocked(api).mock.calls.length;
    fireEvent.click(retry!);
    await settle();
    expect(vi.mocked(api).mock.calls.length).toBeGreaterThan(before);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("CT-widget-08 a narrow cell drops nothing and nothing pins the widget wider than its cell", async () => {
    const { container } = mount(id);
    await settle();
    const pinned = [...container.querySelectorAll<HTMLElement>("[style]")].filter((el) => /(min-)?width\s*:\s*\d/.test(el.getAttribute("style") ?? ""));
    expect(pinned).toEqual([]);
    // Layout follows the cell (container queries), never the window.
    expect(container.querySelector(".\\@container")).not.toBeNull();
    expect(container.innerHTML).not.toMatch(/(?<![@\w-])(sm|md|lg|xl|2xl):grid-cols/);
  });
});

describe("agents-status", () => {
  it("CT-widget-07 a busy agent shows its status, its current work and a real link to the task", async () => {
    mount("agents-status");
    await settle();
    const card = screen.getByText("Liam").closest("div.flex.flex-col")!;
    expect(card.textContent).toMatch(/busy/i);
    expect(card.textContent).toContain("Working on Fix login");
    expect(screen.getByRole("link", { name: "Fix login" }).getAttribute("href")).toBe("/t/t1");
  });

  it("every task link goes through the host (PRD 5.8): a plain click calls openTask, a modified click keeps the host href", async () => {
    const long = "Refactor the agent heartbeat pipeline so stale sessions drop out of the dashboard without a manual refresh";
    vi.mocked(api).mockImplementation(async (url: string) =>
      url.endsWith("/agents/status")
        ? { agents: [agent("a1", { name: "Short", status: "busy", current_task_id: "t1", current_task_title: "Fix login" }), agent("a2", { name: "Long", status: "busy", current_task_id: "t2", current_task_title: long })] }
        : {},
    );
    const openTask = vi.fn();
    const View = views["agents-status"];
    render(
      <MemoryRouter>
        <CoreActionsProvider actions={{ openTask, taskHref: (id) => `/w/acme/p/core/t/${id}` }}>
          <View settings={{}} />
        </CoreActionsProvider>
      </MemoryRouter>,
    );
    await settle();
    // Short title: one link. Long title: the mobile open-task icon and the desktop title link.
    const links = [screen.getByRole("link", { name: "Fix login" }), screen.getByRole("link", { name: "Open task" }), screen.getByRole("link", { name: long })];
    expect(links.map((l) => l.getAttribute("href"))).toEqual(["/w/acme/p/core/t/t1", "/w/acme/p/core/t/t2", "/w/acme/p/core/t/t2"]);
    for (const l of links) {
      openTask.mockClear();
      for (const mod of [{ ctrlKey: true }, { metaKey: true }]) expect(fireEvent.click(l, mod)).toBe(true);
      expect(openTask).not.toHaveBeenCalled();
      expect(fireEvent.click(l)).toBe(false);
      expect(openTask).toHaveBeenCalledWith(l.getAttribute("href")!.split("/").pop());
    }
  });

  it("cards carry the role tag as on Sessions, loading the team directory the dashboard otherwise never loads", async () => {
    mount("agents-status");
    await settle();
    expect(vi.mocked(api).mock.calls.some(([u]) => String(u).endsWith("/workspaces/ws1/team"))).toBe(true);
    expect(screen.getByText("Liam").closest("p")!.textContent).toBe("Liam · mesh-dev");
  });

  it("labels each card with the agent type from the server; an untyped agent gets the fallback, never an empty badge", async () => {
    mount("agents-status");
    await settle();
    for (const label of ["Claude Code", "Codex", "Cursor"]) expect(screen.getByText(label)).toBeTruthy();
    const ghost = screen.getByText("Ghost").closest("div.min-w-0")!;
    expect(ghost.textContent).toContain("Agent");
  });

  // On a narrow card a fixed right-hand status column squeezed the
  // name to "L…" and wrapped the type badge. jsdom has no layout, so this pins
  // the structure that lets the browser do it: name and status share one
  // wrapping row (the status drops under the name when both don't fit), and
  // the badge and the presence label never break inside.
  it("name and status share a wrapping row; the type badge and presence label never wrap", async () => {
    mount("agents-status");
    await settle();
    const name = screen.getByText("Ghost").closest("p")!;
    const head = name.parentElement!;
    expect(head.getAttribute("data-testid")).toBe("agent-card-head");
    expect(head.className).toMatch(/\bflex-wrap\b/);
    expect(name.className).toMatch(/\bmax-w-full\b/);
    expect(head.textContent).toMatch(/last run/i);
    expect(within(head).getByText(/last run/i).className).toMatch(/\bwhitespace-nowrap\b/);
    expect(within(screen.getByText("Liam").closest("div.min-w-0")!).getByText("Claude Code").className).toMatch(/\bwhitespace-nowrap\b/);
  });

  it("an offline agent shows 'last run' instead of a status (ephemeral presence)", async () => {
    mount("agents-status");
    await settle();
    const ghostCard = screen.getByText("Ghost").closest("div.flex.flex-col")!;
    expect(ghostCard.textContent).toMatch(/last run/i);
  });

  it("busy comes from heartbeat_status: the grid shows Busy where Active agents does", async () => {
    mount("agents-status");
    await settle();
    const liam = screen.getByText("Liam").closest("div.flex.flex-col")!;
    expect(liam.textContent).toMatch(/busy/i);
    expect(liam.textContent).not.toMatch(/online/i);
  });

  it("an offline row with a fresh heartbeat (the runner's poll, not a run) says Offline, not 'last run'", async () => {
    FLEET.push(agent("a6", { name: "Polled", status: "offline", heartbeat_status: "", seconds_since_heartbeat: 6 }));
    try {
      mount("agents-status");
      await settle();
      const card = screen.getByText("Polled").closest("div.flex.flex-col")!;
      expect(card.textContent).toMatch(/offline/i);
      expect(card.textContent).not.toMatch(/last run/i);
    } finally {
      FLEET.pop();
    }
  });

  it("filters by effective status (stale is offline) and by 'only with a task'", async () => {
    mount("agents-status", { statuses: ["offline"] });
    await settle();
    expect(screen.getByText("Ghost")).toBeTruthy();
    expect(screen.queryByText("Vera")).toBeNull();
    cleanup();
    mount("agents-status", { onlyWithTask: true });
    await settle();
    expect(screen.getByText("Liam")).toBeTruthy();
    expect(screen.queryByText("Vera")).toBeNull();
  });

  it("a filter that hides everyone says so; it is not 'no agents registered'", async () => {
    mount("agents-status", { statuses: ["busy"], onlyWithTask: false });
    await settle();
    cleanup();
    mount("agents-status", { statuses: ["error"], onlyWithTask: true });
    await settle();
    expect(screen.getByText(/4 agents, none match the filter/)).toBeTruthy();
    expect(screen.queryByText(/No agents registered/)).toBeNull();
  });

  it("polls every 30 s and keeps the last good data when a poll fails", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mount("agents-status");
    await act(async () => { await vi.advanceTimersByTimeAsync(10); });
    expect(screen.getByText("Liam")).toBeTruthy();
    const calls = () => vi.mocked(api).mock.calls.filter(([u]) => String(u).endsWith("/agents/status")).length;
    const first = calls();
    mode = "error";
    await act(async () => { await vi.advanceTimersByTimeAsync(AGENTS_POLL_MS); });
    expect(calls()).toBe(first + 1);
    expect(screen.getByText("Liam")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("agents-summary", () => {
  it("counts online, busy and offline+error; stale counts as offline", async () => {
    const { container } = mount("agents-summary");
    await settle();
    const n = (k: string) => container.querySelector(`[data-agents-count="${k}"]`)!.textContent;
    expect(n("online")).toMatch(/^1/); // Vera; Ghost is stale
    expect(n("busy")).toMatch(/^1/);
    expect(n("offline")).toMatch(/^2/); // Ghost (stale) + Broken (error)
    expect(summarizeAgents(FLEET)).toEqual({ online: 1, busy: 1, offline: 2, total: 4 });
  });

  it("busy equals the Busy rows of agents-active for the same agents", async () => {
    const { container } = mount("agents-summary");
    await settle();
    const busy = Number(container.querySelector('[data-agents-count="busy"]')!.textContent!.match(/^\d+/)![0]);
    cleanup();
    mount("agents-active", { limit: 50 });
    await settle();
    const busyRows = screen.getAllByRole("listitem").filter((li) => /busy/i.test(li.textContent ?? ""));
    expect(busy).toBe(1);
    expect(busyRows).toHaveLength(busy);
    expect(screen.queryByText("Guest")).toBeNull(); // guests are not in the summary's set
  });
});

describe("agents-active", () => {
  it("lists agents that are not offline, up to the limit, and says how many more there are", async () => {
    mount("agents-active", { limit: 2 });
    await settle();
    expect(screen.getByText("Liam")).toBeTruthy();
    expect(screen.getByText("Vera")).toBeTruthy();
    expect(screen.queryByText("Hugh")).toBeNull();
    expect(screen.queryByText("Ghost")).toBeNull();
    expect(screen.getByText("+1 more active")).toBeTruthy();
  });

  it("CT-widget-07 a busy agent shows its status and what it works on", async () => {
    mount("agents-active");
    await settle();
    const row = screen.getByText("Liam").closest("li")!;
    expect(row.textContent).toMatch(/busy/i);
    expect(row.textContent).toContain("Working on Fix login");
  });

  it("an out-of-range limit is clamped, not trusted", async () => {
    mount("agents-active", { limit: 0 });
    await settle();
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
  });
});

describe("filterAgents", () => {
  it("no statuses picked keeps everyone", () => {
    expect(filterAgents(FLEET, { statuses: [] })).toHaveLength(4);
  });
});
