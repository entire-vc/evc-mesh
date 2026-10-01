#!/usr/bin/env node
// Read/write only on an isolated scripts/local-stack.sh stand. Never target prod.
// Real API + real router/stores; route interception adds latency, not fixtures.
// Usage: node scripts/local-stack/project-navigation.mjs http://localhost:3007 /tmp/proof
import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';

const base = new URL(process.argv[2] || 'http://localhost:3007');
if (!['localhost', '127.0.0.1'].includes(base.hostname) || base.protocol !== 'http:') {
  throw new Error('This test creates local fixtures: only an isolated localhost stand is allowed');
}
const out = process.argv[3] || '/tmp/project-navigation';
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ channel: 'chrome' });
const context = await browser.newContext({ baseURL: base.origin, serviceWorkers: 'block' });
const page = await context.newPage();
const failures = [];
page.on('pageerror', e => failures.push(e.message));
const login = await context.request.post('/api/v1/auth/login', {
  data: { email: 'local-stack@example.test', password: 'LocalStack1' },
});
expect(login.ok(), 'local seed account must authenticate').toBeTruthy();
const { tokens } = await login.json();
const headers = { Authorization: `Bearer ${tokens.access_token}` };
async function api(path, body) {
  const res = body
    ? await context.request.post(path, { headers, data: body })
    : await context.request.get(path, { headers });
  expect(res.ok(), `${body ? 'POST' : 'GET'} ${path}: ${res.status()}`).toBeTruthy();
  return res.json();
}
const workspace = (await api('/api/v1/workspaces'))[0];
expect(workspace, 'the isolated local-stack workspace must exist').toBeTruthy();
const projects = (await api(`/api/v1/workspaces/${workspace.id}/projects`)).items;
async function fixture(slug, count) {
  const project = projects.find(p => p.slug === slug) || await api(
    `/api/v1/workspaces/${workspace.id}/projects`, { name: slug === 'lab' ? 'Lab' : 'Keep', slug },
  );
  const statuses = await api(`/api/v1/projects/${project.id}/statuses`);
  const status = statuses.find(s => s.category === 'in_progress');
  expect(status, `${slug} must have an In Progress status`).toBeTruthy();
  const existing = (await api(`/api/v1/projects/${project.id}/tasks?page_size=200`)).items;
  for (let i = existing.length; i < count; i++) {
    await api(`/api/v1/projects/${project.id}/tasks`, {
      title: `${slug === 'lab' ? 'Lab foreign card' : 'Keep active task'} ${i + 1}`,
      status_id: status.id,
    });
  }
  return { project, statuses, status, tasks: (await api(`/api/v1/projects/${project.id}/tasks?page_size=200`)).items };
}
try {
  const lab = await fixture('lab', 1);
  const keep = await fixture('keep', 3);
  const keepPath = `/w/${workspace.slug}/p/keep`;
  const labPath = `/w/${workspace.slug}/p/lab`;
  await writeFile(`${out}/api-fixtures.json`, JSON.stringify({ workspace, lab, keep }, null, 2));
  await page.route(`**/api/v1/projects/${keep.project.id}/**`, async route => {
    // Delayed *real* response exposes stale Lab cards before Keep's data arrives.
    if (/\/(tasks|statuses)(\?|$)/.test(route.request().url())) {
      await new Promise(r => setTimeout(r, 600));
    }
    await route.continue();
  });
  const cardIds = () => page.locator('[data-testid="task-card"]').evaluateAll(
    els => els.map(e => e.getAttribute('data-task-id')).sort(),
  );
  const visibleStatuses = keep.statuses.filter(s => !['done', 'cancelled'].includes(s.category)).map(s => s.id);
  const expected = keep.tasks.filter(t => visibleStatuses.includes(t.status_id)).map(t => t.id).sort();
  const active = keep.tasks.filter(t => t.status_id === keep.status.id).map(t => t.id).sort();
  const foreign = lab.tasks.map(t => t.id);
  const results = [];
  for (const width of [1440, 393]) {
    await page.setViewportSize({ width, height: width === 393 ? 852 : 900 });
    await page.goto(labPath);
    await expect.poll(cardIds).toEqual(foreign.sort());
    await page.evaluate(({ foreign, keepPath }) => {
      window.__foreignFrames = [];
      const inspect = () => {
        if (location.pathname !== keepPath) return;
        const ids = [...document.querySelectorAll('[data-testid="task-card"]')]
          .map(e => e.getAttribute('data-task-id')).filter(id => foreign.includes(id));
        if (ids.length) window.__foreignFrames.push(ids);
      };
      new MutationObserver(inspect).observe(document.body, { subtree: true, childList: true });
    }, { foreign, keepPath });
    // Use the real sidebar link and React Router, including at mobile width.
    await page.locator(`a[href="${keepPath}"]`).first().evaluate(a => a.click());
    await expect(page).toHaveURL(new URL(keepPath, base.origin).href);
    await expect.poll(cardIds).toEqual(expected);
    expect(await page.evaluate(() => window.__foreignFrames), 'no Lab card under the Keep URL, even while loading').toEqual([]);
    const columnIds = await page.locator(`[data-status-id="${keep.status.id}"] [data-testid="task-card"]`)
      .evaluateAll(els => els.map(e => e.getAttribute('data-task-id')).sort());
    expect(columnIds, 'Keep In Progress must equal the API').toEqual(active);
    if (width === 393) await page.locator(`[data-status-id="${keep.status.id}"]`).scrollIntoViewIfNeeded();
    await page.screenshot({ path: `${out}/keep-navigation-${width}.png` });
    results.push({ width, scenario: 'Lab -> Keep sidebar', project_id: keep.project.id, card_ids: expected, foreign_cards: 0 });

    await page.goto(`/t/${lab.tasks[0].id}`);
    await expect(page).toHaveURL(new RegExp(`/p/lab/t/${lab.tasks[0].id}`));
    await page.locator(`a[href="${keepPath}"]`).first().evaluate(a => a.click());
    await expect.poll(cardIds).toEqual(expected);
    results.push({ width, scenario: 'Lab task deep-link -> Keep', card_ids: expected });

    await page.reload();
    await expect.poll(cardIds).toEqual(expected);
    if (width === 393) await page.locator(`[data-status-id="${keep.status.id}"]`).scrollIntoViewIfNeeded();
    await page.screenshot({ path: `${out}/keep-cold-${width}.png` });
    results.push({ width, scenario: 'cold Keep', card_ids: expected });

    // Keep must survive a Lab request started before navigation but completed
    // afterwards. Hold the real HTTP request until Keep has rendered.
    let releaseLab;
    let labRequested;
    const requested = new Promise(r => { labRequested = r; });
    const gate = new Promise(r => { releaseLab = r; });
    const labTasks = new RegExp(`/api/v1/projects/${lab.project.id}/(tasks\\?|statuses)`);
    const holdLab = async route => {
      labRequested();
      await gate;
      await route.continue();
    };
    await page.route(labTasks, holdLab);
    await page.locator(`a[href="${labPath}"]`).first().evaluate(a => a.click());
    await requested;
    await page.locator(`a[href="${keepPath}"]`).first().evaluate(a => a.click());
    await expect.poll(cardIds).toEqual(expected);
    const lateResponse = page.waitForResponse(r => r.url().includes(`/projects/${lab.project.id}/tasks?`));
    releaseLab();
    await (await lateResponse).finished();
    // Allow React's response update and the next browser paint to complete.
    await page.waitForTimeout(200);
    expect(await cardIds(), 'late Lab response must not overwrite Keep').toEqual(expected);
    await page.unroute(labTasks, holdLab);
    results.push({ width, scenario: 'late Lab response after Keep', card_ids: expected });
  }
  expect(failures, 'no browser runtime errors').toEqual([]);
  await writeFile(`${out}/navigation-result.json`, JSON.stringify(results, null, 2));
  console.log(`PASS: ${results.length} scenarios; widths 1440/393; every Keep card matches real local API; no foreign frames`);
} catch (error) {
  await page.screenshot({ path: `${out}/failure.png` });
  console.error('Failure page:', await page.locator('body').innerText());
  console.error('Runtime errors:', failures);
  throw error;
} finally {
  await context.close();
  await browser.close();
}
