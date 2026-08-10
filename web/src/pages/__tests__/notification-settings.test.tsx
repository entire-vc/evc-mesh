import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
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
import type { NotificationPreference, Workspace } from "@/types";

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

beforeEach(() => {
  mockedApi.mockReset();
  mockedGetPermissionState.mockReset().mockResolvedValue("granted");
  mockedIsSubscribed.mockReset().mockResolvedValue(true);
  useNotificationStore.setState({ notifications: [], unreadCount: 0, preferences: [] });
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE, workspaces: [WORKSPACE] });
});
afterEach(() => vi.clearAllMocks());

describe("NotificationSettingsPage — Browser Push hydration (task 9d837f67)", () => {
  it("shows the saved browser_push event set on load, not the hardcoded default", async () => {
    // "task.assigned" is ON in the hardcoded default (notification-settings.tsx:85-89);
    // the saved pref below deliberately excludes it, so a stale default is
    // distinguishable from a correctly hydrated one.
    mockedApi.mockResolvedValue({
      preferences: [
        pref({ id: "bp1", channel: "browser_push", events: ["task.mentioned"] }),
        pref({ id: "wp1", channel: "web_push", events: ["comment.created"] }),
      ],
    });

    renderPage();

    // Poll on the condition the fix actually changes — "Mention" is already
    // true under the untouched default, so it can't distinguish pass/fail.
    await waitFor(() => {
      expect(switchInRowLabeled("Task assigned", 1)).toHaveAttribute(
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
    await waitFor(() => expect(mockedApi).toHaveBeenCalled());
    // Panel is gated behind live subscription, so the toggles aren't on
    // screen while unsubscribed.
    expect(screen.queryByText("Mention")).not.toBeInTheDocument();

    screen.getByRole("button", { name: /Enable/ }).click();

    await waitFor(() => {
      expect(switchInRowLabeled("Task assigned", 1)).toHaveAttribute(
        "aria-checked",
        "false",
      );
    });
    expect(switchInRowLabeled("Mention")).toHaveAttribute("aria-checked", "true");
  });
});
