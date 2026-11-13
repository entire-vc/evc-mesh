import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
vi.mock("@/lib/push", () => ({
  getPermissionState: vi.fn(),
  isSubscribed: vi.fn(),
  subscribeUser: vi.fn(),
  unsubscribeUser: vi.fn(),
}));

import { api } from "@/lib/api";
import { getPermissionState, isSubscribed, subscribeUser } from "@/lib/push";
import NotificationSettingsPage from "@/pages/notification-settings";
import { useNotificationStore } from "@/stores/notification";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import type { NotificationPreference, User, Workspace } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;
const mockedGetPermissionState = getPermissionState as unknown as ReturnType<
  typeof vi.fn
>;
const mockedIsSubscribed = isSubscribed as unknown as ReturnType<typeof vi.fn>;
const mockedSubscribeUser = subscribeUser as unknown as ReturnType<typeof vi.fn>;

const WORKSPACE: Workspace = {
  id: "ws1",
  name: "Acme",
  slug: "acme",
  owner_id: "u1",
  settings: {},
  billing_plan_id: "free",
  billing_customer_id: "",
  icon_url: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function pref(
  overrides: Partial<NotificationPreference>,
): NotificationPreference {
  return {
    id: "p1",
    workspace_id: "ws1",
    user_id: "u1",
    agent_id: null,
    channel: "browser_push",
    events: [],
    is_enabled: true,
    config: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function renderPage() {
  return render(
    <MemoryRouter>
      <NotificationSettingsPage />
    </MemoryRouter>,
  );
}

// Only the active tab's content is in the DOM — switch tabs before asserting
// on a channel's controls.
function switchToTab(name: RegExp) {
  fireEvent.click(screen.getByRole("tab", { name }));
}

// The row's toggle button carries no accessible name of its own (the label is
// a sibling <p>, not inside the <button>) — walk up to the row and pull the
// switch out of it. Throws (rather than returning undefined) so a bad
// occurrence index fails loudly instead of producing a false negative.
function switchInRowLabeled(label: string, occurrence = 0) {
  const rows = screen.getAllByText(label).map((el) => el.closest("div")?.parentElement);
  const row = rows[occurrence];
  const btn = row?.querySelector('button[role="switch"]');
  if (!btn) throw new Error(`no switch found in row for "${label}" [${occurrence}]`);
  return btn;
}

const USER: User = {
  id: "u1",
  email: "account@example.com",
  name: "Test User",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

beforeEach(() => {
  mockedApi.mockReset();
  mockedGetPermissionState.mockReset().mockResolvedValue("granted");
  mockedIsSubscribed.mockReset().mockResolvedValue(true);
  useNotificationStore.setState({
    notifications: [],
    unreadCount: 0,
    preferences: [],
    emailAvailable: true,
    telegramBotInfo: null,
  });
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE, workspaces: [WORKSPACE] });
  useAuthStore.setState({ user: USER, isAuthenticated: true, isLoading: false });
});
afterEach(() => vi.clearAllMocks());

describe("NotificationSettingsPage — tabs", () => {
  it("shows In-App by default, with the other channels' controls not mounted", async () => {
    mockedApi.mockResolvedValue({ preferences: [] });

    renderPage();

    expect(screen.getByRole("tab", { name: /In-App/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("Enable notifications")).toBeInTheDocument();
    expect(screen.queryByText("Enable email notifications")).not.toBeInTheDocument();
    expect(screen.queryByText("Enable Telegram notifications")).not.toBeInTheDocument();
  });

  it("switches tabs on click, mounting only the selected channel's content", async () => {
    mockApiByRoute({ preferences: [] });

    renderPage();
    expect(screen.queryByLabelText("Email address")).not.toBeInTheDocument();

    switchToTab(/Email/);

    expect(screen.getByRole("tab", { name: /Email/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await waitFor(() =>
      expect(screen.getByLabelText("Email address")).toBeInTheDocument(),
    );
    expect(screen.queryByText("Enable notifications")).not.toBeInTheDocument();
  });
});

describe("NotificationSettingsPage — Browser Push hydration (task 9d837f67)", () => {
  it("shows the saved browser_push event set on load, not the hardcoded default", async () => {
    // "task.assigned" is ON in the hardcoded default (notification-settings/index.tsx);
    // the saved pref below deliberately excludes it, so a stale default is
    // distinguishable from a correctly hydrated one.
    mockedApi.mockResolvedValue({
      preferences: [
        pref({ id: "bp1", channel: "browser_push", events: ["task.mentioned"] }),
        pref({ id: "wp1", channel: "web_push", events: ["comment.created"] }),
      ],
    });

    renderPage();
    switchToTab(/Browser Push/);

    // Poll on the condition the fix actually changes — "Mention" is already
    // true under the untouched default, so it can't distinguish pass/fail.
    await waitFor(() => {
      expect(switchInRowLabeled("Task assigned")).toHaveAttribute(
        "aria-checked",
        "false",
      );
    });
    expect(switchInRowLabeled("Mention")).toHaveAttribute("aria-checked", "true");
  });

  it("does not apply a browser_push preference belonging to a different workspace", async () => {
    // "Mention" is selected in the hardcoded default; this pref (for ws2)
    // deselects it. If workspace scoping is missing, it would leak in here.
    mockedApi.mockResolvedValue({
      preferences: [
        pref({ id: "other-ws", workspace_id: "ws2", channel: "browser_push", events: ["task.assigned"] }),
      ],
    });

    renderPage();
    switchToTab(/Browser Push/);
    await waitFor(() => expect(mockedApi).toHaveBeenCalled());

    // Falls back to the hardcoded default (task.mentioned stays selected)
    // since the fetched pref is scoped to a workspace the user isn't viewing.
    // Give the hydration effect a real chance to (wrongly) apply first.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(switchInRowLabeled("Mention")).toHaveAttribute("aria-checked", "true");
  });

  it("keeps the saved browser_push event set across a subscribe/unsubscribe cycle", async () => {
    // Subscription lost (e.g. browser revoked it) but the preference row —
    // written the last time the user was subscribed — must still hydrate,
    // and must not be reset back to the hardcoded default on re-subscribe.
    mockedIsSubscribed.mockResolvedValue(false);
    mockedSubscribeUser.mockResolvedValue({} as PushSubscription);
    mockedApi.mockResolvedValue({
      preferences: [
        pref({ id: "bp1", channel: "browser_push", events: ["task.mentioned"], is_enabled: false }),
      ],
    });

    renderPage();
    switchToTab(/Browser Push/);
    await waitFor(() => expect(mockedApi).toHaveBeenCalled());
    // Panel is gated behind live subscription, so the per-event toggle rows
    // aren't on screen while unsubscribed. Asserted on the browser-push
    // panel's own description wording rather than the row label "Mention":
    // each tab only mounts while active (see index.tsx), so nothing else on
    // screen could produce a false match here either way — this is just the
    // more specific assertion.
    expect(
      screen.queryByText("When someone @mentions you"),
    ).not.toBeInTheDocument();

    screen.getByRole("button", { name: /Enable/ }).click();

    await waitFor(() => {
      expect(switchInRowLabeled("Task assigned")).toHaveAttribute(
        "aria-checked",
        "false",
      );
    });
    expect(switchInRowLabeled("Mention")).toHaveAttribute("aria-checked", "true");
  });
});

// Route api() by URL/method so email-availability, telegram-bot-info,
// preference GET, and preference PUT can each answer differently within one
// test.
function mockApiByRoute(routes: {
  preferences?: NotificationPreference[];
  available?: boolean;
  telegramAvailable?: boolean;
  telegramBotUsername?: string;
  onSave?: (body: unknown) => void;
  onSaveResponse?: (body: unknown) => object;
  telegramInstanceDisabled?: boolean;
}) {
  mockedApi.mockImplementation(
    (url: string, opts?: { method?: string; body?: unknown }) => {
      if (url.endsWith("/extensions")) {
        return Promise.resolve({
          extensions: [{ id: "telegram", status: { enabled: true, source: "workspace", instance_disabled: routes.telegramInstanceDisabled === true } }],
        });
      }
      if (url === "/api/v1/notifications/email-availability") {
        return Promise.resolve({ available: routes.available ?? true });
      }
      if (url.startsWith("/api/v1/notifications/telegram-bot-info")) {
        return Promise.resolve({
          available: routes.telegramAvailable ?? false,
          bot_username: routes.telegramBotUsername ?? "",
        });
      }
      if (url === "/api/v1/notifications/preferences" && opts?.method === "PUT") {
        routes.onSave?.(opts.body);
        const extra = routes.onSaveResponse?.(opts.body) ?? {};
        return Promise.resolve({ id: "new", ...(opts.body as object), ...extra });
      }
      if (url === "/api/v1/notifications/preferences") {
        return Promise.resolve({ preferences: routes.preferences ?? [] });
      }
      return Promise.resolve({});
    },
  );
}

describe("NotificationSettingsPage — Email channel", () => {
  it("defaults the address field to the account email when no preference is saved", async () => {
    mockApiByRoute({ preferences: [] });

    renderPage();
    switchToTab(/Email/);

    await waitFor(() =>
      expect(screen.getByLabelText("Email address")).toHaveValue("account@example.com"),
    );
  });

  it("hydrates the saved custom address and event set from a stored email preference", async () => {
    mockApiByRoute({
      preferences: [
        pref({
          id: "e1",
          channel: "email",
          events: ["comment.created"],
          config: { email: "custom@example.com" },
        }),
      ],
    });

    renderPage();
    switchToTab(/Email/);

    await waitFor(() =>
      expect(screen.getByLabelText("Email address")).toHaveValue("custom@example.com"),
    );
    // "New comment" is the only subscribed event on the stored row — reusing
    // the account default here would silently mean "someone's edited address
    // came with an event set that isn't theirs."
    expect(switchInRowLabeled("New comment")).toHaveAttribute("aria-checked", "true");
    expect(switchInRowLabeled("Task assigned")).toHaveAttribute("aria-checked", "false");
  });

  it("shows an unavailable notice and hides the controls when SMTP is not configured", async () => {
    mockApiByRoute({ preferences: [], available: false });

    renderPage();
    switchToTab(/Email/);

    await waitFor(() =>
      expect(screen.getByText("Email notifications are not available")).toBeInTheDocument(),
    );
    expect(screen.queryByLabelText("Email address")).not.toBeInTheDocument();
    // The subscription level is still choosable and savable before the channel is connected.
    expect(screen.getByRole("radio", { name: /Only my tasks/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Save preferences/ })).toBeInTheDocument();
  });

  it("marks the Email tab itself as unavailable when SMTP is not configured", async () => {
    mockApiByRoute({ preferences: [], available: false });

    renderPage();

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: /Email/ })).toHaveTextContent("unavailable"),
    );
  });

  it("saves the email channel with the address wrapped in config", async () => {
    let savedBody: unknown;
    mockApiByRoute({ preferences: [], onSave: (body) => { savedBody = body; } });

    renderPage();
    switchToTab(/Email/);
    await waitFor(() =>
      expect(screen.getByLabelText("Email address")).toHaveValue("account@example.com"),
    );

    const emailSaveButton = screen.getByRole("button", { name: /Save preferences/ });
    emailSaveButton.click();

    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({
      channel: "email",
      config: { email: "account@example.com" },
      workspace_id: "ws1",
    });
  });
});

describe("NotificationSettingsPage — Telegram channel", () => {
  it("shows an unavailable notice when the workspace has no bot connected", async () => {
    mockApiByRoute({ preferences: [], telegramAvailable: false });

    renderPage();
    switchToTab(/Telegram/);

    await waitFor(() =>
      expect(screen.getByText("Telegram is not connected")).toBeInTheDocument(),
    );
    expect(screen.queryByLabelText("Telegram username")).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Only my tasks/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Save preferences/ })).toBeInTheDocument();
  });

  it("marks the Telegram tab itself as unavailable when no bot is connected", async () => {
    mockApiByRoute({ preferences: [], telegramAvailable: false });

    renderPage();

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: /Telegram/ })).toHaveTextContent("unavailable"),
    );
  });

  it("shows a connect link built from the bot username and a fresh bind token", async () => {
    mockApiByRoute({
      preferences: [
        pref({
          id: "t1",
          channel: "telegram",
          events: ["comment.created"],
          config: { telegram_username: "alice", telegram_bind_token: "tok123" },
        }),
      ],
      telegramAvailable: true,
      telegramBotUsername: "mesh_bot",
    });

    renderPage();
    switchToTab(/Telegram/);

    await waitFor(() => {
      const link = screen.getByRole("link", { name: /Open @mesh_bot to finish connecting/ });
      expect(link).toHaveAttribute("href", "https://t.me/mesh_bot?start=tok123");
    });
  });

  it("shows connected status instead of a link once chat_id is bound", async () => {
    mockApiByRoute({
      preferences: [
        pref({
          id: "t1",
          channel: "telegram",
          events: ["comment.created"],
          config: { telegram_username: "alice", telegram_chat_id: 555 },
        }),
      ],
      telegramAvailable: true,
      telegramBotUsername: "mesh_bot",
    });

    renderPage();
    switchToTab(/Telegram/);

    await waitFor(() =>
      expect(screen.getByText(/Connected — @mesh_bot can message you/)).toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("link", { name: /finish connecting/ }),
    ).not.toBeInTheDocument();
  });

  it("saves the telegram channel with the username wrapped in config", async () => {
    let savedBody: unknown;
    mockApiByRoute({
      preferences: [],
      telegramAvailable: true,
      telegramBotUsername: "mesh_bot",
      onSave: (body) => { savedBody = body; },
      onSaveResponse: () => ({ config: { telegram_username: "alice", telegram_bind_token: "new-token" } }),
    });

    renderPage();
    switchToTab(/Telegram/);
    await waitFor(() =>
      expect(screen.getByLabelText("Telegram username")).toBeInTheDocument(),
    );

    fireEvent.change(screen.getByLabelText("Telegram username"), {
      target: { value: "alice" },
    });

    const telegramSaveButton = screen.getByRole("button", { name: /Save preferences/ });
    telegramSaveButton.click();

    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({
      channel: "telegram",
      config: { telegram_username: "alice" },
      workspace_id: "ws1",
    });
  });
});

describe("NotificationSettingsPage — subscription scope", () => {
  const scopeRadio = (name: RegExp) => screen.getByRole("radio", { name });

  it("shows `mine` for a row without config.scope", async () => {
    mockApiByRoute({
      preferences: [pref({ channel: "email", config: { email: "a@b.co" } })],
    });
    renderPage();
    switchToTab(/Email/);
    await waitFor(() =>
      expect(scopeRadio(/Only my tasks/)).toHaveAttribute("aria-checked", "true"),
    );
    expect(scopeRadio(/All workspace events/)).toHaveAttribute("aria-checked", "false");
  });

  it("saves config.scope=all on the email tab and keeps the other config keys", async () => {
    let savedBody: unknown;
    mockApiByRoute({
      preferences: [pref({ channel: "email", config: { email: "a@b.co", digest: "daily" } })],
      onSave: (b) => { savedBody = b; },
    });
    renderPage();
    switchToTab(/Email/);
    await waitFor(() => expect(screen.getByLabelText("Email address")).toHaveValue("a@b.co"));

    fireEvent.click(scopeRadio(/All workspace events/));
    fireEvent.click(screen.getByRole("button", { name: /Save preferences/ }));

    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({
      channel: "email",
      config: { email: "a@b.co", digest: "daily", scope: "all" },
    });
  });

  it("saves config.scope on the telegram tab without echoing the server-owned chat id back", async () => {
    let savedBody: { config?: Record<string, unknown> } | undefined;
    mockApiByRoute({
      preferences: [pref({ channel: "telegram", config: { telegram_username: "alice", telegram_chat_id: 555, scope: "mentions" } })],
      telegramAvailable: true,
      telegramBotUsername: "mesh_bot",
      onSave: (b) => { savedBody = b as typeof savedBody; },
    });
    renderPage();
    switchToTab(/Telegram/);
    await waitFor(() => expect(scopeRadio(/Mentions and assignments only/)).toHaveAttribute("aria-checked", "true"));

    fireEvent.click(scopeRadio(/All workspace events/));
    fireEvent.click(screen.getByRole("button", { name: /Save preferences/ }));

    await waitFor(() => expect(savedBody).toBeDefined());
    // The API takes string values only; chat_id is written by the bot's /start, never by this form.
    expect(savedBody?.config).toEqual({ telegram_username: "alice", scope: "all" });
  });

  it("saves config.scope on the in-app and browser push tabs", async () => {
    const bodies: { channel: string; config?: Record<string, unknown> }[] = [];
    mockApiByRoute({ preferences: [], onSave: (b) => { bodies.push(b as (typeof bodies)[number]); } });
    renderPage();

    fireEvent.click(scopeRadio(/Mentions and assignments only/));
    fireEvent.click(screen.getByRole("button", { name: /Save preferences/ }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({ channel: "web_push", config: { scope: "mentions" } });

    switchToTab(/Browser/);
    await waitFor(() => expect(scopeRadio(/All workspace events/)).toBeInTheDocument());
    fireEvent.click(scopeRadio(/All workspace events/));
    fireEvent.click(screen.getByRole("button", { name: /Save preferences/ }));
    await waitFor(() => expect(bodies).toHaveLength(2));
    expect(bodies[1]).toMatchObject({ channel: "browser_push", config: { scope: "all" } });
  });

  it("lets an unsubscribed browser choose and save a Browser Push scope", async () => {
    mockedIsSubscribed.mockResolvedValue(false);
    let savedBody: { channel?: string; is_enabled?: boolean; config?: Record<string, unknown> } | undefined;
    mockApiByRoute({ preferences: [], onSave: (b) => { savedBody = b as typeof savedBody; } });
    renderPage();
    switchToTab(/Browser/);
    await waitFor(() => expect(scopeRadio(/Mentions and assignments only/)).toBeInTheDocument());

    fireEvent.click(scopeRadio(/Mentions and assignments only/));
    fireEvent.click(screen.getByRole("button", { name: /Save preferences/ }));

    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({ channel: "browser_push", is_enabled: false, config: { scope: "mentions" } });
  });

  it("saves the email scope while the channel is unavailable, without requiring an address", async () => {
    let savedBody: { channel?: string; is_enabled?: boolean; config?: Record<string, unknown> } | undefined;
    mockApiByRoute({ preferences: [], available: false, onSave: (b) => { savedBody = b as typeof savedBody; } });
    renderPage();
    switchToTab(/Email/);
    await waitFor(() => expect(screen.getByText("Email notifications are not available")).toBeInTheDocument());

    fireEvent.click(scopeRadio(/All workspace events/));
    fireEvent.click(screen.getByRole("button", { name: /Save preferences/ }));

    await waitFor(() => expect(savedBody).toBeDefined());
    // The account email prefills the address, but an unavailable channel must not be switched on by a scope save.
    expect(savedBody).toMatchObject({ channel: "email", is_enabled: false, config: { scope: "all" } });
  });

  it("keeps a stored enabled email preference enabled when only the scope is saved on an unavailable channel", async () => {
    let savedBody: { channel?: string; is_enabled?: boolean; config?: Record<string, unknown> } | undefined;
    mockApiByRoute({
      preferences: [pref({ channel: "email", is_enabled: true, config: { email: "a@b.co" } })],
      available: false,
      onSave: (b) => { savedBody = b as typeof savedBody; },
    });
    renderPage();
    switchToTab(/Email/);
    await waitFor(() => expect(screen.getByText("Email notifications are not available")).toBeInTheDocument());
    fireEvent.click(scopeRadio(/Mentions and assignments only/));
    fireEvent.click(screen.getByRole("button", { name: /Save preferences/ }));
    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({ channel: "email", is_enabled: true, config: { email: "a@b.co", scope: "mentions" } });
  });

  it("positive control: an available email channel still saves is_enabled:true", async () => {
    let savedBody: { is_enabled?: boolean } | undefined;
    mockApiByRoute({ preferences: [], onSave: (b) => { savedBody = b as typeof savedBody; } });
    renderPage();
    switchToTab(/Email/);
    await waitFor(() => expect(screen.getByLabelText("Email address")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: /Save preferences/ }));
    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody?.is_enabled).toBe(true);
  });

  it("saves the telegram scope while no bot is connected, with delivery left off and no username required", async () => {
    let savedBody: { channel?: string; is_enabled?: boolean; config?: Record<string, unknown> } | undefined;
    mockApiByRoute({ preferences: [], telegramAvailable: false, onSave: (b) => { savedBody = b as typeof savedBody; } });
    renderPage();
    switchToTab(/Telegram/);
    await waitFor(() => expect(screen.getByText("Telegram is not connected")).toBeInTheDocument());

    fireEvent.click(scopeRadio(/Mentions and assignments only/));
    fireEvent.click(screen.getByRole("button", { name: /Save preferences/ }));

    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({ channel: "telegram", is_enabled: false, config: { scope: "mentions" } });
    expect(savedBody?.config).not.toHaveProperty("telegram_username");
  });

  it("offers the scope choice on every channel tab", async () => {
    mockApiByRoute({ preferences: [], telegramAvailable: true, telegramBotUsername: "bot" });
    renderPage();
    for (const tab of [/In-App/, /Browser/, /Email/, /Telegram/]) {
      switchToTab(tab);
      await waitFor(() => expect(scopeRadio(/Mentions and assignments only/)).toBeInTheDocument());
    }
  });
});

describe("NotificationSettingsPage — Telegram instance switch", () => {
  it("shows the instance status even when the bot is not available", async () => {
    mockApiByRoute({ preferences: [], telegramAvailable: false, telegramInstanceDisabled: true });
    renderPage();
    switchToTab(/Telegram/);
    expect(await screen.findByText("Disabled at instance level")).toBeInTheDocument();
    expect(screen.getByText(/not connected|not available|isn.t connected/i)).toBeInTheDocument();
  });

  it("says the extension is disabled at instance level, and says nothing when it is on", async () => {
    mockApiByRoute({ preferences: [], telegramAvailable: true, telegramBotUsername: "bot", telegramInstanceDisabled: true });
    const first = renderPage();
    switchToTab(/Telegram/);
    expect(await screen.findByText("Disabled at instance level")).toBeInTheDocument();
    first.unmount();

    mockApiByRoute({ preferences: [], telegramAvailable: true, telegramBotUsername: "bot" });
    renderPage();
    switchToTab(/Telegram/);
    await screen.findByText("Enable Telegram notifications");
    expect(screen.queryByText("Disabled at instance level")).not.toBeInTheDocument();
  });
});
