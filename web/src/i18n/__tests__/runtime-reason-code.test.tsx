import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { RuntimeStateBadge } from "@/components/runtime-state-badge";
import i18n, { setLanguage } from "@/i18n";
import enBoard from "@/locales/en/board.json";
import ruBoard from "@/locales/ru/board.json";
import type { RuntimeState } from "@/types";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

const text = () => screen.getByTestId("runtime-state").textContent;

describe("runtime_state reason_code", () => {
  const cases: Array<[RuntimeState, RegExp, RegExp]> = [
    [
      { kind: "stopped", reason_code: "review_refeed_hold", reason: "ревью уже было" },
      /^Stopped: Reviewed; awaits commit$/,
      /^Стоит: ревью было; ждёт коммита$/,
    ],
    [
      { kind: "stopped", reason_code: "review_cooldown", reason_params: { minutes: "4" } },
      /^Stopped: In review in 4 min$/,
      /^Стоит: в ревью через 4 мин$/,
    ],
    [
      { kind: "waiting", reason_code: "umbrella", reason: "free text is ignored when the code is known" },
      /^Waiting: Epic: awaits children$/,
      /^Ждёт: эпик: ждёт дочерних$/,
    ],
  ];

  it.each(cases)("%j renders in en and ru", async (state, en, ru) => {
    const view = render(<RuntimeStateBadge state={state} />);
    expect(text()).toMatch(en);
    view.unmount();
    await act(async () => {
      await setLanguage("ru");
    });
    render(<RuntimeStateBadge state={state} />);
    expect(text()).toMatch(ru);
  });

  it("without a code the free-text reason is shown as before", () => {
    render(<RuntimeStateBadge state={{ kind: "stopped", reason: "refeed-cap" }} />);
    expect(text()).toBe("Stopped: refeed-cap");
  });

  it("a waiting state with a ref shows the coded reason next to the ref; without a code it stays ref-only", () => {
    const ref = "entire-vc/evc-mesh#10918";
    const view = render(<RuntimeStateBadge state={{ kind: "waiting", ref, reason_code: "umbrella" }} />);
    expect(text()).toMatch(/^Waiting: Epic: awaits children/);
    view.unmount();
    render(<RuntimeStateBadge state={{ kind: "waiting", ref, reason: "free text" }} />);
    expect(text()).toMatch(/^Waiting:/);
    expect(text()).not.toContain("free text");
  });

  it("an unknown code (newer server) falls back to the free-text reason", () => {
    render(<RuntimeStateBadge state={{ kind: "stopped", reason_code: "from_the_future", reason: "some note" }} />);
    expect(text()).toBe("Stopped: some note");
  });

  it("en and ru cover the same reason codes", () => {
    expect(Object.keys(ruBoard.runtimeState.reasons).sort()).toEqual(Object.keys(enBoard.runtimeState.reasons).sort());
  });
});
