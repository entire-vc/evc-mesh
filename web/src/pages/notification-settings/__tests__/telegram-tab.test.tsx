import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { TelegramTab } from "../telegram-tab";
import type { TelegramBotInfo } from "@/types";

// With Telegram switched off for the whole instance, the tab must say exactly
// that and nothing else: the old "not connected — ask an admin to connect a
// bot" block sent admins to set up a bot the instance would never use.

function renderTab(opts: { instanceDisabled: boolean; botInfo: Partial<TelegramBotInfo> }) {
  return render(
    <TelegramTab
      instanceDisabled={opts.instanceDisabled}
      scope="all"
      onScopeChange={vi.fn()}
      telegramBotInfo={{ bot_username: "", reachable: true, ...opts.botInfo } as TelegramBotInfo}
      telegramEnabled={false}
      setTelegramEnabled={vi.fn()}
      telegramUsername=""
      onTelegramUsernameChange={vi.fn()}
      telegramChatID={null}
      telegramBindLink={null}
      telegramEvents={new Set()}
      toggleTelegramEvent={vi.fn()}
      telegramSaving={false}
      telegramSaved={false}
      telegramUnbinding={false}
      onSave={vi.fn()}
      onDisconnect={vi.fn()}
    />,
  );
}

describe("TelegramTab — instance switch", () => {
  it("shows only the instance banner when the instance has Telegram off and no bot", () => {
    renderTab({ instanceDisabled: true, botInfo: { available: false } });
    expect(screen.getByText("Disabled at instance level")).toBeInTheDocument();
    expect(screen.queryByText("Telegram is not connected")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Save preferences/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
  });

  it("shows only the instance banner even when a bot is configured", () => {
    renderTab({ instanceDisabled: true, botInfo: { available: true, bot_username: "mesh_bot" } });
    expect(screen.getByText("Disabled at instance level")).toBeInTheDocument();
    expect(screen.queryByText("Enable Telegram notifications")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Save preferences/ })).not.toBeInTheDocument();
  });

  it("keeps the not-connected block, scope and save when the instance has Telegram on", () => {
    renderTab({ instanceDisabled: false, botInfo: { available: false } });
    expect(screen.queryByText("Disabled at instance level")).not.toBeInTheDocument();
    expect(screen.getByText("Telegram is not connected")).toBeInTheDocument();
    expect(screen.getAllByRole("radio").length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: /Save preferences/ })).toBeInTheDocument();
  });
});
