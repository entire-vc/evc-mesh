import { test, expect } from "@playwright/test";
import { mkdirSync } from "node:fs";

// The Task list settings form is taller than a 768 px laptop window. The dialog is capped to the
// window and scrolls the form inside: the title and Save stay on screen with a gap to the edges,
// and Save submits. Screenshots go to e2e-widget-settings/shots/.

const SHOTS = "e2e-widget-settings/shots";
mkdirSync(SHOTS, { recursive: true });

for (const [width, height] of [[1366, 768], [1440, 900], [393, 852]] as const) {
  test(`${width}x${height}: title and Save inside the window, form scrolls, Save submits`, async ({ page }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/e2e-widget-settings/harness.html?type=task-list");
    const dialog = page.getByRole("dialog", { name: "Widget settings" });
    await expect(dialog).toBeVisible();

    const box = (await dialog.boundingBox())!;
    expect(box.y).toBeGreaterThanOrEqual(8);
    expect(box.y + box.height).toBeLessThanOrEqual(height - 8);

    const title = (await page.getByRole("heading", { name: "Widget settings" }).boundingBox())!;
    expect(title.y).toBeGreaterThanOrEqual(0);
    const save = page.getByRole("button", { name: "Save", exact: true });
    const saveBox = (await save.boundingBox())!;
    expect(saveBox.y + saveBox.height).toBeLessThanOrEqual(height);
    await page.screenshot({ path: `${SHOTS}/task-list-settings-${width}x${height}.png` });

    // the form is longer than the room it has, and scrolls to its last field
    const body = page.getByTestId("widget-settings-body");
    const [scrollH, clientH] = await body.evaluate((el) => [el.scrollHeight, el.clientHeight]);
    if (height <= 768) expect(scrollH).toBeGreaterThan(clientH);
    await body.evaluate((el) => el.scrollTo(0, el.scrollHeight));
    await expect(page.getByRole("heading", { name: "Widget settings" })).toBeInViewport();

    // a real pointer click on Save (no force): it must be the topmost element at its spot
    await save.click({ trial: true });
    await save.click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByTestId("saved")).toContainText("auto_refresh");
  });
}
