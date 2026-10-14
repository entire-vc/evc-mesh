import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import i18n, { setLanguage } from "@/i18n";
import { InAppTab } from "@/pages/notification-settings/in-app-tab";
import { TelegramTab } from "@/pages/notification-settings/telegram-tab";
import type { TelegramBotInfo } from "@/types";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

const RAW_KEY = /\b(settings|events|actions|tabs|inApp|telegram)\.[a-zA-Z]+/;

function renderInApp() {
  return render(
    <InAppTab
      isLoaded
      isEnabled
      setIsEnabled={() => {}}
      selectedEvents={new Set(["task.assigned"])}
      toggleEvent={() => {}}
      isSaving={false}
      saved={false}
      onSave={() => {}}
    />,
  );
}

function renderTelegramNotConnected() {
  const info = { available: false } as TelegramBotInfo;
  return render(
    <TelegramTab
      telegramBotInfo={info}
      telegramEnabled={false}
      setTelegramEnabled={() => {}}
      telegramUsername=""
      onTelegramUsernameChange={() => {}}
      telegramChatID={null}
      telegramBindLink={null}
      telegramEvents={new Set()}
      toggleTelegramEvent={() => {}}
      telegramSaving={false}
      telegramSaved={false}
      telegramUnbinding={false}
      onSave={() => {}}
      onDisconnect={() => {}}
    />,
  );
}

describe("L2.3 notification settings: en unchanged, ru translated, no raw keys", () => {
  it("in-app tab renders the same English as before, then Russian", async () => {
    const en = renderInApp();
    expect(screen.getByText("In-App Notifications")).toBeTruthy();
    expect(screen.getByText("Task assigned")).toBeTruthy();
    expect(
      screen.getByText("When a task is assigned to you or someone in your workspace"),
    ).toBeTruthy();
    expect(screen.getByText("Save preferences")).toBeTruthy();
    en.unmount();

    await act(async () => {
      await setLanguage("ru");
    });
    renderInApp();
    expect(screen.getByText("Уведомления в приложении")).toBeTruthy();
    expect(screen.getByText("Назначена задача")).toBeTruthy();
    expect(screen.getByText("Сохранить настройки")).toBeTruthy();
    expect(document.body.textContent).not.toMatch(RAW_KEY);
    expect(document.body.textContent).not.toMatch(/Task assigned|Save preferences/);
  });

  it("telegram 'not connected' hint keeps the bold link text in both languages", async () => {
    const en = renderTelegramNotConnected();
    const hint = screen.getByText("Integrations").parentElement;
    expect(hint?.textContent).toBe(
      "Ask a workspace owner or admin to connect a bot from the Integrations page.",
    );
    en.unmount();

    await act(async () => {
      await setLanguage("ru");
    });
    renderTelegramNotConnected();
    expect(screen.getByText("Интеграции").className).toContain("font-medium");
    expect(screen.getByText("Telegram не подключён")).toBeTruthy();
    expect(document.body.textContent).not.toMatch(RAW_KEY);
  });
});
