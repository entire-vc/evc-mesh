import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { TaskCard } from "@/components/task-card";
import i18n, { setLanguage } from "@/i18n";
import type { Task } from "@/types";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

const task = {
  id: "t1",
  project_id: "p1",
  status_id: "s1",
  title: "A card",
  assignee_type: "unassigned",
  priority: "medium",
  position: 0,
  created_by: "c1",
  created_by_type: "agent",
  created_at: "2026-08-20T10:00:00Z",
  updated_at: "2026-08-20T10:00:00Z",
  human_gate: false,
  description: "has text",
} as Task;

describe("L2.2 board card: en by default, ru translated, no raw keys", () => {
  it("renders the description marker label in en, then ru", async () => {
    const view = render(<TaskCard task={task} />);
    const en = screen.getByLabelText(i18n.t("board:card.hasDescription"));
    expect(en).toBeTruthy();
    expect(i18n.t("board:card.hasDescription")).not.toMatch(/[а-яё]/i);
    view.unmount();
    await act(async () => {
      await setLanguage("ru");
    });
    render(<TaskCard task={task} />);
    const ru = i18n.t("board:card.hasDescription");
    expect(ru).toMatch(/[а-яё]/i);
    expect(screen.getByLabelText(ru)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/\bcard\.[a-zA-Z]+/);
  });
});
