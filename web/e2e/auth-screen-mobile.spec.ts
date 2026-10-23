import { test, expect } from "@playwright/test";

/**
 * CT-auth-08 in a real browser: at 393 px the sign-in and register screens
 * (auth.screen, mesh-default view) have no horizontal scroll and the submit
 * button is inside the viewport width. Unauthenticated: no login needed.
 * The instance config is stubbed open: on a CI instance with registration
 * closed /register shows the «closed» card, which has no form to measure.
 */
for (const path of ["/login", "/register"]) {
  test(`auth.screen ${path} fits 393px without horizontal scroll`, async ({ page }) => {
    await page.setViewportSize({ width: 393, height: 852 });
    await page.route("**/api/v1/auth/config", (route) =>
      route.fulfill({ json: { registration_enabled: true } }),
    );
    await page.goto(path);
    const submit = page.locator("form button[type=submit]");
    await expect(submit).toBeVisible();
    const box = await submit.boundingBox();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(393);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });
}

// CT-auth-02 in a real browser: Enter in a field submits the form exactly once.
// The error is matched by its text, not by role="alert": authed-e2e runs against
// a deployed instance that may still serve the pre-slice page, whose error
// block has no alert role.
test("auth.screen /login: Enter submits once", async ({ page }) => {
  let posts = 0;
  await page.route("**/api/v1/auth/login", async (route) => {
    posts += 1;
    await route.fulfill({ status: 401, json: { error: "invalid credentials" } });
  });
  await page.goto("/login");
  // /login is first painted by the static shell in index.html, whose form is
  // not submittable: Enter before React mounts does nothing. Retry until the
  // React form has taken over; the shell never POSTs, so the count stays 1.
  await expect(async () => {
    await page.fill("#email", "e2e@example.com");
    await page.fill("#password", "not-a-real-password");
    await page.press("#password", "Enter");
    await expect(page.getByText("invalid credentials")).toBeVisible({ timeout: 1000 });
  }).toPass({ timeout: 15000 });
  expect(posts).toBe(1);
});
