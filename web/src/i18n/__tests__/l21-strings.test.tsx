import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { TaskCard } from "@/components/task-card";
import { RuntimeStateBadge } from "@/components/runtime-state-badge";
import i18n, { setLanguage } from "@/i18n";
import type { RuntimeState, Task } from "@/types";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

const text = () => screen.getByTestId("runtime-state").textContent;

describe("L2.1 strings: en by default, ru keeps the previous texts", () => {
  const cases: Array<[RuntimeState, RegExp, RegExp]> = [
    [{ kind: "working", lane: "Carl", since: "2026-10-10T10:00:00Z" }, /^Being worked on by Carl since \d\d:\d\d$/, /^В работе у Carl с \d\d:\d\d$/],
    [{ kind: "waiting", reason: "r" }, /^Waiting: r$/, /^Ждёт: r$/],
    [{ kind: "queued", lane: "Carl", position: 2 }, /^Queued in lane Carl, position 2$/, /^В очереди лейна Carl, позиция 2$/],
    [{ kind: "stopped", reason: "cap" }, /^Stopped: cap$/, /^Стоит: cap$/],
  ];

  it.each(cases)("%j", async (state, en, ru) => {
    const view = render(<RuntimeStateBadge state={state} />);
    expect(text()).toMatch(en);
    view.unmount();
    await act(async () => {
      await setLanguage("ru");
    });
    render(<RuntimeStateBadge state={state} />);
    expect(text()).toMatch(ru);
  });

  it("stale note follows the language", async () => {
    await act(async () => {
      await setLanguage("ru");
    });
    render(<RuntimeStateBadge state={{ kind: "waiting", stale: true }} />);
    expect(screen.getByTestId("runtime-state-stale").textContent).toContain("данные устарели");
  });

  describe("false-open tooltips", () => {
    const task = (fo: Partial<NonNullable<Task["false_open"]>>) =>
      ({
        id: "t1",
        project_id: "p1",
        status_id: "s1",
        title: "A card",
        assignee_type: "unassigned",
        priority: "urgent",
        position: 0,
        created_by: "c1",
        created_by_type: "agent",
        created_at: "2026-08-20T10:00:00Z",
        updated_at: "2026-08-20T10:00:00Z",
        human_gate: false,
        false_open: {
          all_children_closed: false,
          only_parked_children_left: false,
          open_children_count: 0,
          stale_days: 12,
          ...fo,
        },
      }) as Task;
    const title = () => screen.getByTestId("false-open-indicator").getAttribute("title");

    it("all_children_closed: en, then ru verbatim", async () => {
      const view = render(<TaskCard task={task({ all_children_closed: true })} />);
      expect(title()).toBe("False-open: all subtasks are closed, the card has not changed for 12 d. - consider revisiting its priority");
      view.unmount();
      await act(async () => {
        await setLanguage("ru");
      });
      render(<TaskCard task={task({ all_children_closed: true })} />);
      expect(title()).toBe("False-open: все подзадачи закрыты, карточка без изменений 12 дн. — стоит пересмотреть приоритет");
    });

    it("only_parked_children_left: en, then ru verbatim", async () => {
      const t = task({ only_parked_children_left: true, open_children_count: 3 });
      const view = render(<TaskCard task={t} />);
      expect(title()).toBe("Only backlog subtasks are left (3), the card has not changed for 12 d. - there is work, but it is not moving");
      view.unmount();
      await act(async () => {
        await setLanguage("ru");
      });
      render(<TaskCard task={t} />);
      expect(title()).toBe("Остались только backlog-подзадачи (3), карточка без изменений 12 дн. — работа есть, но не движется");
    });
  });
});
