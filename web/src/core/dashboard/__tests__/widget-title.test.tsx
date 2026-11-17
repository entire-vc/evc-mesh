// §5.6 «The title replaces the type name»: the instance title the editor saves (settings.title) is what the
// dashboard shows in view mode, for every core widget that draws its own header, at any viewport.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { useMemberStore } from "@/stores/member";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import { Widget } from "../slots";
import { WIDGETS } from "../registry";
import { widgetTitle } from "../config";

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: () => null }));
import { api } from "@/lib/api";

const CUSTOM = "QA96 custom title";
// Widgets without a header of their own: an initiative card shows the initiative's name, the list shows cards.
const HEADERLESS = new Set(["initiative", "initiatives"]);
const HEADED = WIDGETS.map((d) => d.id).filter((id) => !HEADERLESS.has(id));

function mount(type: string, settings: Record<string, unknown>) {
  return render(
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <Routes>
        <Route path="/w/:wsSlug/dashboard" element={<Widget instance={{ id: `i-${type}`, type, x: 0, y: 0, w: 6, h: 4, settings } as never} />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {} })) as never;
  useMemberStore.setState({ myRole: "member" });
  useAuthStore.setState({ user: { id: "me" } as never });
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useProjectStore.setState({ projects: [{ id: "p1", slug: "core", name: "Core" } as never] });
  // The header does not wait for data: the request stays pending.
  vi.mocked(api).mockImplementation(() => new Promise(() => {}));
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("instance title replaces the type name in view mode (§5.6)", () => {
  it("covers the widgets the acceptance named", () => {
    expect(HEADED).toEqual(expect.arrayContaining(["mentions", "my-comments", "recent-comments", "my-tasks", "task-list", "triage-queue", "event-feed"]));
  });

  it.each(HEADED)("%s shows settings.title", async (type) => {
    mount(type, { title: CUSTOM });
    expect(await screen.findByText(CUSTOM)).toBeTruthy();
  });

  it.each(HEADED)("%s without a title, or with a blank one, keeps its type name", async (type) => {
    // The header is the first heading-like line of the card: an <h3>, or the link that the sessions card uses.
    const header = (c: HTMLElement) => (c.querySelector("[data-mesh-widget] h3, [data-mesh-widget] a")?.textContent ?? "").trim();
    const titled = mount(type, { title: CUSTOM });
    await screen.findByText(CUSTOM);
    expect(header(titled.container)).toBe(CUSTOM);
    cleanup();
    const plain = mount(type, {});
    await vi.waitFor(() => expect(header(plain.container)).not.toBe(""));
    const typeName = header(plain.container);
    expect(typeName).not.toBe(CUSTOM);
    cleanup();
    const blank = mount(type, { title: "   " });
    await vi.waitFor(() => expect(header(blank.container)).toBe(typeName));
  });

  it("mentions: the saved title is drawn instead of «Mentions», the default comes back without one", async () => {
    mount("mentions", { title: CUSTOM });
    await screen.findByText(CUSTOM);
    expect(screen.queryByText(/^(Mentions|Упоминания)$/)).toBeNull();
    cleanup();
    mount("mentions", {});
    expect(await screen.findByText(/^(Mentions|Упоминания)$/)).toBeTruthy();
  });
});

describe("widgetTitle", () => {
  it("takes a trimmed non-empty string, else the fallback", () => {
    expect(widgetTitle({ title: "  Mine  " }, "Type")).toBe("Mine");
    expect(widgetTitle({ title: "   " }, "Type")).toBe("Type");
    expect(widgetTitle({ title: 42 }, "Type")).toBe("Type");
    expect(widgetTitle({}, "Type")).toBe("Type");
  });
});
