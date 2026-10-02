import { test, expect } from "@playwright/test";

test("readiness waits for a fixture rendered after the old five-second deadline", async ({ page }) => {
  await page.setContent("<main></main>");
  await page.evaluate(() => {
    setTimeout(() => {
      document.querySelector("main")!.innerHTML =
        '<a href="/fixture">Perf fixture</a><form><input aria-label="Comment text" /></form>';
    }, 6_000);
  });
  const project = page.getByRole("link", { name: "Perf fixture", exact: true });
  try {
    await expect(project).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Comment text" })).toBeVisible();
  } finally {
    // Positive control: a readiness timeout must not be mistaken for an
    // incorrect selector or a fixture that never renders.
    await expect(project).toBeVisible({ timeout: 7_000 });
    console.log("[readiness control] delayed fixture rendered");
  }
});

test("readiness still rejects a fixture that never renders", async ({ page }) => {
  await page.setContent("<main></main>");
  await expect(
    expect(page.getByRole("link", { name: "Perf fixture", exact: true })).toBeVisible()
  ).rejects.toThrow(/element\(s\) not found/);
});
