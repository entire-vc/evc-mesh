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
import { getPermissionState, isSubscribed } from "@/lib/push";
import NotificationSettingsPage from "@/pages/notification-settings";
import { useNotificationStore } from "@/stores/notification";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import type { NotificationPreference, User, Workspace } from "@/types";

// Disconnecting a Telegram account is the half of the binding flow the settings
// page did not have: it could connect an account and show that one was
// connected, but offered no way to undo it — and because the server carries
// chat_id forward across saves, turning the channel off did not undo it either.

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;
const mockedGetPermissionState = getPermissionState as unknown as ReturnType<typeof vi.fn>;
const mockedIsSubscribed = isSubscribed as unknown as ReturnType<typeof vi.fn>;

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

const USER: User = {
  id: "u1",
  email: "account@example.com",
  name: "Test User",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function boundTelegramPref(
  overrides: Partial<NotificationPreference> = {},
): NotificationPreference {
  return {
    id: "t1",
    workspace_id: "ws1",
    user_id: "u1",
    agent_id: null,
    channel: "telegram",
    events: ["comment.created"],
    is_enabled: true,
    config: { telegram_username: "alice", telegram_chat_id: 555 },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

// mockTelegramApi answers the three GETs the page makes and records the PUT, so
// a test can assert on exactly what was sent to the server.
function mockTelegramApi(opts: {
  preferences?: NotificationPreference[];
  onSave?: (body: unknown) => void;
  saveResponse?: object;
  botInfo?: object;
}) {
  mockedApi.mockImplementation(
    (url: string, reqOpts?: { method?: string; body?: unknown }) => {
      if (url === "/api/v1/notifications/email-availability") {
        return Promise.resolve({ available: true });
      }
      if (url.startsWith("/api/v1/notifications/telegram-bot-info")) {
        return Promise.resolve({
          available: true,
          bot_username: "mesh_bot",
          reachable: true,
          unavailable_reason: "",
          ...(opts.botInfo ?? {}),
        });
      }
      if (url === "/api/v1/notifications/preferences" && reqOpts?.method === "PUT") {
        opts.onSave?.(reqOpts.body);
        return Promise.resolve({
          id: "t1",
          ...(reqOpts.body as object),
          ...(opts.saveResponse ?? {}),
        });
      }
      if (url === "/api/v1/notifications/preferences") {
        return Promise.resolve({ preferences: opts.preferences ?? [] });
      }
      return Promise.resolve({});
    },
  );
}

function renderPage() {
  return render(
    <MemoryRouter>
      <NotificationSettingsPage />
    </MemoryRouter>,
  );
}

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

describe("NotificationSettingsPage — Telegram disconnect", () => {
  it("offers Disconnect only once an account is actually bound", async () => {
    mockTelegramApi({
      preferences: [
        boundTelegramPref({
          config: { telegram_username: "alice", telegram_bind_token: "tok123" },
        }),
      ],
    });

    renderPage();

    await waitFor(() =>
      expect(
        screen.getByRole("link", { name: /Open @mesh_bot to finish connecting/ }),
      ).toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("button", { name: /Disconnect/ }),
    ).not.toBeInTheDocument();
  });

  it("disables the channel when Disconnect is pressed, which is what revokes the binding", async () => {
    let savedBody: unknown;
    mockTelegramApi({
      preferences: [boundTelegramPref()],
      onSave: (body) => {
        savedBody = body;
      },
      // What the server returns after a revoke: no chat, no fresh link.
      saveResponse: { config: { telegram_username: "alice" } },
    });

    renderPage();

    const disconnect = await waitFor(() =>
      screen.getByRole("button", { name: /Disconnect/ }),
    );
    fireEvent.click(disconnect);

    await waitFor(() => expect(savedBody).toBeDefined());
    // is_enabled:false IS the revocation — the server carries chat_id forward
    // only while the channel is on. No dedicated unbind flag exists, and one
    // was deliberately not added: it would be a second way to express what
    // this already means.
    expect(savedBody).toMatchObject({
      workspace_id: "ws1",
      channel: "telegram",
      is_enabled: false,
    });
  });

  it("stops claiming the account is connected after a successful disconnect", async () => {
    mockTelegramApi({
      preferences: [boundTelegramPref()],
      saveResponse: { config: { telegram_username: "alice" } },
    });

    renderPage();

    const disconnect = await waitFor(() =>
      screen.getByRole("button", { name: /Disconnect/ }),
    );
    fireEvent.click(disconnect);

    await waitFor(() =>
      expect(
        screen.queryByText(/Connected — @mesh_bot can message you/),
      ).not.toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("button", { name: /Disconnect/ }),
    ).not.toBeInTheDocument();
  });

  it("leaves the channel enabled on an ordinary save, so saving never revokes by accident", async () => {
    let savedBody: unknown;
    mockTelegramApi({
      preferences: [boundTelegramPref()],
      onSave: (body) => {
        savedBody = body;
      },
    });

    renderPage();

    const saveButtons = await waitFor(() =>
      screen.getAllByRole("button", { name: /Save preferences/ }),
    );
    const telegramSave = saveButtons[saveButtons.length - 1];
    if (!telegramSave) throw new Error("expected a telegram Save preferences button");
    fireEvent.click(telegramSave);

    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({ is_enabled: true });
  });

  it("disconnects without a username, since revoking is not enabling", async () => {
    let savedBody: unknown;
    mockTelegramApi({
      preferences: [boundTelegramPref({ config: { telegram_chat_id: 555 } })],
      onSave: (body) => {
        savedBody = body;
      },
      saveResponse: { config: {} },
    });

    renderPage();

    const disconnect = await waitFor(() =>
      screen.getByRole("button", { name: /Disconnect/ }),
    );
    fireEvent.click(disconnect);

    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({ is_enabled: false });
  });
});

// The banner below is a different failure from "no bot is connected", and the
// page has to tell them apart: a configured bot on a host with no route to
// api.telegram.org renders the full, healthy-looking connect UI and delivers
// nothing. Before the server reported reachability there was no way for the
// page to know, so it showed the happy path and the user found out by never
// receiving a message.
describe("NotificationSettingsPage — Telegram reachability", () => {
  it("warns when a configured bot cannot be reached, and says what to fix", async () => {
    mockTelegramApi({
      preferences: [boundTelegramPref()],
      botInfo: {
        reachable: false,
        unavailable_reason:
          "This server cannot reach api.telegram.org. Telegram notifications will not be delivered until the api container is allowed outbound HTTPS (port 443) to api.telegram.org, or an HTTPS_PROXY is configured for it.",
      },
    });

    renderPage();

    const alert = await waitFor(() => screen.getByRole("alert"));
    expect(alert).toHaveTextContent(/cannot be delivered right now/i);
    expect(alert).toHaveTextContent(/api\.telegram\.org/);
    expect(alert).toHaveTextContent(/HTTPS_PROXY/);
  });

  it("keeps the controls usable — the bot is configured, the fix is server-side", async () => {
    mockTelegramApi({
      preferences: [boundTelegramPref()],
      botInfo: { reachable: false, unavailable_reason: "cannot reach api.telegram.org" },
    });

    renderPage();

    // The bound account resolves from the preferences fetch, which lands after
    // the bot-info fetch that raises the banner — so wait on the later of the
    // two rather than asserting into a half-loaded page.
    await waitFor(() => screen.getByRole("button", { name: /Disconnect/ }));
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });

  it("says nothing when the channel is healthy", async () => {
    mockTelegramApi({ preferences: [boundTelegramPref()] });

    renderPage();

    await waitFor(() => screen.getByRole("button", { name: /Disconnect/ }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
