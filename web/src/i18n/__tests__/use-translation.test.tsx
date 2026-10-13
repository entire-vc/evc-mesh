import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { useTranslation } from "react-i18next";
import i18n, { setLanguage } from "@/i18n";

function Probe() {
  const { t } = useTranslation("common");
  return <p data-testid="p">{t("i18nDemo.greeting", { name: "Ann" })}</p>;
}

afterEach(async () => {
  await i18n.changeLanguage("en");
});

it("a mounted component re-renders en -> ru without a reload", async () => {
  render(<Probe />);
  expect(screen.getByTestId("p").textContent).toBe("Hello, Ann");
  await act(async () => {
    await setLanguage("ru");
  });
  expect(screen.getByTestId("p").textContent).toBe("Привет, Ann");
});
