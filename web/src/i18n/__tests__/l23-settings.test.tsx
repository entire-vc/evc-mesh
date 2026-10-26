import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mockedApi = vi.fn();
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => mockedApi(...(args as [string, unknown])),
  getAccessToken: vi.fn(() => null),
}));

import i18n, { setLanguage } from "@/i18n";
import { WorkspaceSecrets } from "@/components/workspace-secrets";
import { PriorityFlag } from "@/components/priority-flag";
import { DelegationLevelSelect } from "@/components/delegation-level-select";
import type { Secret } from "@/types";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

const WS = "11111111-1111-1111-1111-111111111111";
const secret: Secret = {
  id: "22222222-2222-2222-2222-222222222222",
  name: "GITHUB_TOKEN",
  scope: "workspace",
  value_sha256_prefix: "3b1f9a02",
  value_length: 40,
  value_char_class: "a-z+A-Z+0-9",
  created_by: "33333333-3333-3333-3333-333333333333",
  created_by_type: "user",
  created_at: "2026-08-20T10:00:00Z",
};

mockedApi.mockImplementation((path: string) =>
  Promise.resolve(path.endsWith(`/workspaces/${WS}/secrets`) ? [secret] : []),
);

// Raw i18n keys of the settings namespace look like `secrets.columns.name`.
const RAW_KEY = /\b(secrets|priority|delegation|install|avatar)\.[a-zA-Z]+/;

async function renderScreen() {
  const view = render(
    <>
      <WorkspaceSecrets workspaceId={WS} canManage />
      <PriorityFlag priority="urgent" showLabel />
      <DelegationLevelSelect value="auto" onChange={() => {}} />
    </>,
  );
  await screen.findByTestId("secret-list");
  return view;
}

describe("L2.3 settings: en by default, ru translated, no raw keys", () => {
  it("renders the secrets section, priority and delegation labels in en, then ru", async () => {
    const view = await renderScreen();
    expect(screen.getByText("Built-in secrets")).toBeTruthy();
    expect(screen.getByText("Fingerprint")).toBeTruthy();
    expect(screen.getByText("Urgent")).toBeTruthy();
    expect(screen.getByText("Agent completes task autonomously, notifies only.")).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/[а-яё]/i);
    expect(document.body.textContent).not.toMatch(RAW_KEY);
    view.unmount();

    await act(async () => {
      await setLanguage("ru");
    });
    await renderScreen();
    expect(screen.getByText("Встроенные секреты")).toBeTruthy();
    expect(screen.getByText("Отпечаток")).toBeTruthy();
    expect(screen.getByText("Срочный")).toBeTruthy();
    expect(screen.getByRole("option", { name: "Автовыполнение" })).toBeTruthy();
    expect(screen.queryByText("Fingerprint")).toBeNull();
    expect(document.body.textContent).not.toMatch(RAW_KEY);
  });
});
