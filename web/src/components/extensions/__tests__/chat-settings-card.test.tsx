import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { ChatSettingsCard } from "@/components/extensions/chat-settings-card";
import { api } from "@/lib/api";

vi.mock("@/lib/api", async () => ({ ...(await vi.importActual("@/lib/api")), api: vi.fn() }));
vi.mock("@/components/ui/toast", () => ({ toast: { error: vi.fn(), success: vi.fn() }, Toaster: () => null }));

const base = "/api/v1/projects/p1/chat";
const agent = (id: string, name: string) => ({
  agent_id: id, name, color_token: "avatar-1", presence: "online", active_sessions: 0, queue_length: 0,
});
let saved: { enabled: boolean; agents: string[]; allowed_roles?: string[] };
let puts: unknown[];

beforeEach(() => {
  saved = { enabled: false, agents: [] };
  puts = [];
  vi.mocked(api).mockImplementation(async (path: string, opts?: { method?: string; body?: unknown }) => {
    if (path === `${base}/agents`) return { items: [agent("a1", "Vera"), agent("a2", "Hana")] } as never;
    if (path === `${base}/settings` && opts?.method === "PUT") {
      puts.push(opts.body);
      saved = opts.body as typeof saved;
      return saved as never;
    }
    if (path === `${base}/settings`) return saved as never;
    throw new Error(`unexpected ${path}`);
  });
});

const renderCard = () =>
  render(
    <MemoryRouter>
      <ChatSettingsCard projectId="p1" chatPath="/w/acme/p/demo/chat" />
    </MemoryRouter>,
  );

describe("ChatSettingsCard", () => {
  it("will not save an enabled chat with no agent picked", async () => {
    renderCard();
    fireEvent.click(await screen.findByTestId("chat-settings-enabled"));
    expect(screen.getByTestId("chat-settings-save")).toBeDisabled();
    expect(screen.getByRole("status")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("chat-settings-agent-a1"));
    expect(screen.getByTestId("chat-settings-save")).toBeEnabled();
  });

  it("saves enabled, picked agents and roles through the settings endpoint", async () => {
    renderCard();
    fireEvent.click(await screen.findByTestId("chat-settings-enabled"));
    fireEvent.click(screen.getByTestId("chat-settings-agent-a2"));
    fireEvent.click(screen.getByTestId("chat-settings-role-viewer"));
    fireEvent.click(screen.getByTestId("chat-settings-save"));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({ enabled: true, agents: ["a2"], allowed_roles: ["admin", "member", "viewer"] });
    expect(await screen.findByRole("link")).toHaveAttribute("href", "/w/acme/p/demo/chat");
  });

  it("shows an error with retry when settings fail to load", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("boom"));
    renderCard();
    expect(await screen.findByTestId("chat-settings-error")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button"));
    expect(await screen.findByTestId("chat-settings")).toBeInTheDocument();
  });

  it("ignores a late answer for the project we switched away from", async () => {
    let releaseA!: (v: unknown) => void;
    const slowA = new Promise((r) => (releaseA = r));
    vi.mocked(api).mockImplementation(async (path: string) => {
      if (path === "/api/v1/projects/pa/chat/settings") return (await slowA) as never;
      if (path === "/api/v1/projects/pa/chat/agents") return { items: [agent("a1", "Vera")] } as never;
      if (path === "/api/v1/projects/pb/chat/settings") return { enabled: false, agents: [] } as never;
      if (path === "/api/v1/projects/pb/chat/agents") return { items: [agent("b1", "Bee")] } as never;
      throw new Error(`unexpected ${path}`);
    });
    const ui = (id: string) => (
      <MemoryRouter>
        <ChatSettingsCard projectId={id} chatPath="/chat" />
      </MemoryRouter>
    );
    const { rerender } = render(ui("pa"));
    rerender(ui("pb"));
    await screen.findByTestId("chat-settings-agent-b1");
    releaseA({ enabled: true, agents: ["a1"] });
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("chat-settings-agent-a1")).toBeNull();
    expect((screen.getByTestId("chat-settings-enabled") as HTMLInputElement).checked).toBe(false);
  });
});
