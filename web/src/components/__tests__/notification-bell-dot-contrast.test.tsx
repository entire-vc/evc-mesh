/// <reference types="node" />
/**
 * The unread indicator dot in the bell dropdown (`notification-bell.tsx`,
 * `BellRowItem`) is coloured `bg-primary`. `--primary` and `--accent` are the
 * SAME token value in both themes (brandkit.css) — and the row's hover state
 * is `hover:bg-accent`, solid. So the instant an unread row is hovered, the
 * dot and its background become the identical color: contrast collapses to
 * exactly 1.00, and the one visual cue "you have not read this" disappears
 * at precisely the moment a reader points at the row to deal with it.
 *
 * Two things this file checks, per the card's own acceptance criteria:
 * 1. Token math (`WCAG 1.4.11`, `AA_NON_TEXT = 3`), same helper the toolbar
 *    contrast tests already use — a measurement, not a screenshot.
 * 2. A negative control proving the probe actually discriminates: run the
 *    SAME comparison against the pairing this component used before the fix
 *    and confirm it reports the reported defect (1.00), not a passing number.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import {
  AA_NON_TEXT,
  brandkitThemes,
  contrast,
  contrastOverBlend,
} from "@/test-utils/brandkit-contrast";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: vi.fn(() => null) }));

import { api } from "@/lib/api";
import { NotificationBell } from "@/components/notification-bell";
import { useAuthStore } from "@/stores/auth";
import { useNotificationStore } from "@/stores/notification";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Mention, Project, User } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const PROJECT: Project = {
  id: "proj-1",
  workspace_id: "ws-1",
  name: "Demo",
  description: "",
  slug: "demo",
  icon: "",
  settings: {},
  default_assignee_type: "none",
  is_archived: false,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

const USER: User = {
  id: "u1",
  email: "hugh@entire.vc",
  name: "Hugh",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const UNREAD_MENTION: Mention = {
  comment_id: "c-task-1",
  mentioned_id: "u1",
  mentioned_kind: "user",
  mentioned_slug: "hugh",
  extracted_at: "2026-08-31T10:00:00Z",
  seen_at: null,
  task_id: "task-1",
  task_title: "Unread row for the dot probe",
  project_id: "proj-1",
  comment_body: "@hugh look here",
  author_id: "a-1",
  author_name: "Ann Author",
};

function mockBackend() {
  mockedApi.mockImplementation((path: string) => {
    if (path === "/api/v1/notifications") {
      return Promise.resolve({ items: [], unread_count: 0 });
    }
    if (path === "/api/v1/me/mentions") return Promise.resolve([UNREAD_MENTION]);
    if (path === "/api/v1/me/document-mentions") return Promise.resolve([]);
    if (path === "/api/v1/me/mentions/unseen_count") return Promise.resolve({ count: 1 });
    if (path === "/api/v1/me/document-mentions/unseen_count") return Promise.resolve({ count: 0 });
    return Promise.reject(new Error(`unexpected path ${path}`));
  });
}

function renderBell() {
  return render(
    <MemoryRouter>
      <NotificationBell />
    </MemoryRouter>,
  );
}

// Mirrors notification-bell.test.tsx's own setup — same fixtures, same
// stores, so this file renders the exact same component tree the rest of
// the suite already exercises rather than a hand-trimmed stand-in.
beforeEach(() => {
  mockedNavigate.mockReset();
  mockedApi.mockReset();
  mockBackend();
  useNotificationStore.setState({ notifications: [], unreadCount: 0, pollingHandle: null });
  useProjectStore.setState({ projects: [PROJECT] });
  useAuthStore.setState({ user: USER, isAuthenticated: true, isLoading: false });
  useWorkspaceStore.setState({
    currentWorkspace: {
      id: "ws-1",
      name: "Acme",
      slug: "acme",
      owner_id: "u1",
      billing_plan_id: null,
      billing_customer_id: null,
      icon_url: null,
      settings: {},
      created_at: "2026-08-01T00:00:00Z",
      updated_at: "2026-08-01T00:00:00Z",
    } as never,
  });
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("BellRowItem unread dot — rendered classes", () => {
  it("carries group-hover:bg-accent-foreground on the dot, and group on the row", async () => {
    renderBell();
    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    const row = (await screen.findByText(/Unread row for the dot probe/)).closest("li")!;

    expect(row).toHaveClass("group");
    expect(row).toHaveClass("hover:bg-accent");

    const dot = row.querySelector("span.rounded-full")!;
    expect(dot).toHaveClass("bg-primary");
    expect(dot).toHaveClass("group-hover:bg-accent-foreground");
  });
});

/**
 * Pure token math — same helper and same discipline as
 * rich-text-editor-toolbar-contrast.test.tsx. Numbers pinned with
 * `toBeCloseTo` so a token value drifting away from what was actually
 * measured fails loudly instead of the assertion silently widening.
 */
describe("Unread dot contrast — token math (WCAG 1.4.11, non-text, 3:1)", () => {
  const { light, dark } = brandkitThemes();

  it("--primary and --accent really are the same token value — the root cause, stated as a fact the fix does not change", () => {
    expect(contrast(light, "--primary", "--accent")).toBeCloseTo(1, 2);
    expect(contrast(dark, "--primary", "--accent")).toBeCloseTo(1, 2);
  });

  it("negative control: the dot's OLD pairing (bg-primary against a hovered bg-accent row) measures exactly the reported defect", () => {
    // This is literally what BellRowItem rendered before this fix: the dot
    // stayed bg-primary unconditionally, and the row's hover state replaces
    // bg-accent/30 with solid bg-accent. If this ever stops reporting ~1.00,
    // the probe below has stopped discriminating and must not be trusted.
    const oldHoveredLight = contrast(light, "--primary", "--accent");
    const oldHoveredDark = contrast(dark, "--primary", "--accent");
    expect(oldHoveredLight).toBeCloseTo(1, 2);
    expect(oldHoveredDark).toBeCloseTo(1, 2);
    expect(oldHoveredLight).toBeLessThan(AA_NON_TEXT);
    expect(oldHoveredDark).toBeLessThan(AA_NON_TEXT);
  });

  it("at rest (unhovered, bg-accent/30), the dot was already fine and the fix leaves it untouched", () => {
    const restLight = contrastOverBlend(light, "--primary", "--accent", 0.3, "--popover");
    const restDark = contrastOverBlend(dark, "--primary", "--accent", 0.3, "--popover");
    expect(restLight).toBeCloseTo(3.95, 1);
    expect(restDark).toBeCloseTo(4.57, 1);
    expect(restLight).toBeGreaterThanOrEqual(AA_NON_TEXT);
    expect(restDark).toBeGreaterThanOrEqual(AA_NON_TEXT);
  });

  it("hovered (solid bg-accent), the FIXED dot (accent-foreground) clears 3:1 with margin in both themes", () => {
    const hoveredLight = contrast(light, "--accent-foreground", "--accent");
    const hoveredDark = contrast(dark, "--accent-foreground", "--accent");
    expect(hoveredLight).toBeCloseTo(6.33, 1);
    expect(hoveredDark).toBeCloseTo(10.38, 1);
    expect(hoveredLight).toBeGreaterThanOrEqual(AA_NON_TEXT);
    expect(hoveredDark).toBeGreaterThanOrEqual(AA_NON_TEXT);
  });
});
