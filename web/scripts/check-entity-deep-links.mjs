#!/usr/bin/env node
// Acceptance probe. Reads real API data; never creates or edits entities.
// node scripts/check-entity-deep-links.mjs <baseURL> <outputDir> [baseline]
// For direct live runs provide DEEP_LINK_EMAIL / DEEP_LINK_PASSWORD.
import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';

const baseURL = process.argv[2];
const out = resolve(process.argv[3]);
const baseline = process.argv[4] === 'baseline';
const artifactId = '32f1eaa5-9922-4d32-9f8c-109d659038bb';
const taskPath = '/t/cccc90d0-bb94-4251-bafa-566b648bd0cf';
const settingsPath = '/w/ws-006901c7/p/lab/settings';
const missing = '00000000-0000-0000-0000-000000000000';
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ channel: process.env.DEEP_LINK_BROWSER_CHANNEL });
const context = await browser.newContext({ baseURL, serviceWorkers: 'block' });
const results = [];
try {
  if (process.env.DEEP_LINK_EMAIL && process.env.DEEP_LINK_PASSWORD) {
    const login = await context.request.post('/api/v1/auth/login', { data: {
      email: process.env.DEEP_LINK_EMAIL, password: process.env.DEEP_LINK_PASSWORD,
    } });
    if (!login.ok()) throw new Error(`Service-user login failed: ${login.status()}`);
  }
  const page = await context.newPage();
  page.on('requestfailed', request => console.error(`TRANSPORT ${new URL(request.url()).pathname}: ${request.failure()?.errorText}`));
  for (const width of [1440, 393]) {
    await page.setViewportSize({ width, height: width === 393 ? 852 : 900 });
    await page.goto(`${taskPath}?artifact=${artifactId}`);
    const row = page.locator(`[data-artifact-id="${artifactId}"]:visible`);
    if (baseline) {
      await expect(page.getByRole('button', { name: /^Artifacts/ }).first()).toBeVisible({ timeout: 30000 });
      await page.screenshot({ path: `${out}/artifact-${width}.png` });
      await expect(row).toHaveCount(0);
      results.push(`RED artifact ${width}: URL did not open or highlight target`);
      const artifactsResponse = page.waitForResponse(r => new URL(r.url()).pathname.endsWith('/artifacts') && r.request().method() === 'GET');
      await page.getByRole('button', { name: /^Artifacts/ }).first().click();
      const artifacts = await (await artifactsResponse).json();
      const artifact = artifacts.items.find(item => item.id === artifactId);
      if (!artifact) throw new Error('Reference task does not expose the expected artifact');
      await expect(page.getByText(artifact.name, { exact: true }).first()).toBeVisible({ timeout: 30000 });
      results.push(`CONTROL artifact ${width}: manual Artifacts tab renders real entity`);
    } else {
      await expect(row).toHaveAttribute('data-focused', 'true', { timeout: 30000 });
      await expect(row).toBeInViewport();
      await expect(row.getByRole('link', { name: 'Link to artifact' })).toHaveAttribute('href', `${taskPath}?artifact=${artifactId}`);
      await page.screenshot({ path: `${out}/artifact-${width}.png` });
      results.push(`PASS artifact ${width}: target highlighted and visible after short-link redirect`);
      await page.goto(`${taskPath}?artifact=${missing}`);
      await expect(page.locator('[role="alert"]:visible').filter({ hasText: /Artifact not found/ })).toBeVisible({ timeout: 30000 });
      await expect(page.locator('[data-artifact-id][data-focused="true"]:visible')).toHaveCount(0);
      await page.screenshot({ path: `${out}/artifact-missing-${width}.png` });
      results.push(`PASS artifact ${width}: nonexistent ID shows not found`);
    }
    const schedulesResponse = page.waitForResponse(r => new URL(r.url()).pathname.endsWith('/recurring') && r.request().method() === 'GET');
    await page.goto(`${settingsPath}?tab=recurring`);
    const schedules = await schedulesResponse.then(r => r.json());
    const scheduleId = schedules.items?.[0]?.id;
    if (!scheduleId) throw new Error('Lab needs an existing schedule as a positive control');
    if (baseline) {
      await expect(page.getByText('General', { exact: true }).first()).toBeVisible();
      await expect(page.getByText('Recurring Schedules', { exact: true })).toHaveCount(0);
      await page.screenshot({ path: `${out}/schedule-${width}.png` });
      results.push(`RED recurring ${width}: tab=recurring still opens General`);
      await page.getByRole('button', { name: 'Recurring', exact: true }).click();
      await expect(page.getByText('Recurring Schedules', { exact: true })).toBeVisible();
      await expect(page.getByText(schedules.items[0].title_template, { exact: true }).first()).toBeVisible();
      results.push(`CONTROL recurring ${width}: manual Recurring tab renders real schedule`);
    } else {
      await expect(page.getByText('Recurring Schedules', { exact: true })).toBeVisible();
      await page.goto(`${settingsPath}?tab=recurring&schedule=${scheduleId}`);
      const scheduleRow = page.locator(`[data-schedule-id="${scheduleId}"]`);
      await expect(scheduleRow).toHaveAttribute('data-focused', 'true', { timeout: 30000 });
      await expect(scheduleRow).toBeInViewport();
      await page.screenshot({ path: `${out}/schedule-${width}.png` });
      results.push(`PASS recurring ${width}: tab selected, schedule ${scheduleId} highlighted and visible`);
      await page.goto(`${settingsPath}?tab=recurring&schedule=${missing}`);
      await expect(page.getByText(/Schedule not found/)).toBeVisible({ timeout: 30000 });
      await expect(page.locator('[data-schedule-id][data-focused="true"]')).toHaveCount(0);
      await page.screenshot({ path: `${out}/schedule-missing-${width}.png` });
      results.push(`PASS recurring ${width}: nonexistent ID shows not found`);
    }
  }
} finally {
  await writeFile(`${out}/results.json`, JSON.stringify(results, null, 2));
  console.log(results.join('\n'));
  await browser.close();
}
