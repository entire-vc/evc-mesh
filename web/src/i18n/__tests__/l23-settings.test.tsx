import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import i18n, { setLanguage } from "@/i18n";
import { PriorityFlag } from "@/components/priority-flag";
import { DelegationLevelSelect } from "@/components/delegation-level-select";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

// Raw i18n keys of the settings namespace look like `priority.urgent`.
const RAW_KEY = /\b(priority|delegation|install|avatar)\.[a-zA-Z]+/;

function renderScreen() {
  return render(
    <>
      <PriorityFlag priority="urgent" showLabel />
      <DelegationLevelSelect value="auto" onChange={() => {}} />
    </>,
  );
}

describe("L2.3 settings: en by default, ru translated, no raw keys", () => {
  it("renders priority and delegation labels in en, then ru", async () => {
    const view = renderScreen();
    expect(screen.getByText("Urgent")).toBeTruthy();
    expect(screen.getByText("Agent completes task autonomously, notifies only.")).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/[а-яё]/i);
    expect(document.body.textContent).not.toMatch(RAW_KEY);
    view.unmount();

    await act(async () => {
      await setLanguage("ru");
    });
    renderScreen();
    expect(screen.getByText("Срочный")).toBeTruthy();
    expect(screen.getByRole("option", { name: "Автовыполнение" })).toBeTruthy();
    expect(screen.queryByText("Urgent")).toBeNull();
    expect(document.body.textContent).not.toMatch(RAW_KEY);
  });
});
