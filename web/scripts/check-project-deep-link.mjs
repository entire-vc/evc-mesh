import { chromium, expect } from '@playwright/test';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve, extname, sep } from 'node:path';

// Read-only real-auth/API check. Saves rendered UI only, never session state,
// traces, passwords, tokens or raw API responses. Candidate assets are optional.
for (const key of ['APP_BASE_URL', 'E2E_USER_EMAIL', 'E2E_USER_PASSWORD', 'E2E_PROJECT_ID', 'E2E_TASK_ID', 'E2E_COMMENT_ID']) {
  if (!process.env[key]) throw new Error(`Missing ${key}`);
}
const base = process.env.APP_BASE_URL;
const output = process.env.DEEP_LINK_OUTPUT_DIR || 'project-deep-link-artifacts';
const assets = process.env.DEEP_LINK_ASSET_DIR && resolve(process.env.DEEP_LINK_ASSET_DIR);
const failures = [];
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: true, ...(process.env.DEEP_LINK_BROWSER_CHANNEL ? { channel: process.env.DEEP_LINK_BROWSER_CHANNEL } : {}) });
try {
  const context = await browser.newContext({ serviceWorkers: 'block' });
  if (assets) {
    const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2', '.json': 'application/json', '.webmanifest': 'application/manifest+json' };
    await context.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (url.origin !== new URL(base).origin || url.pathname.startsWith('/api/')) return route.continue();
      const path = resolve(assets, route.request().resourceType() === 'document' ? 'index.html' : url.pathname.slice(1));
      if (!path.startsWith(assets + sep)) throw new Error('Asset outside candidate directory');
      await route.fulfill({ body: await readFile(path), contentType: mime[extname(path)] || 'application/octet-stream' });
    });
  }
  const login = await context.request.post(`${base}/api/v1/auth/login`, {
    data: { email: process.env.E2E_USER_EMAIL, password: process.env.E2E_USER_PASSWORD }, timeout: 20_000,
  }).catch(() => { throw new Error('Login transport failed (details suppressed)'); });
  if (login.status() !== 200) throw new Error(`Login failed (${login.status()})`);
  const token = (await login.json()).tokens?.access_token;
  if (!token) throw new Error('Login returned no access token');
  const get = async path => {
    const res = await context.request.get(`${base}${path}`, { headers: { Authorization: `Bearer ${token}` } });
    if (!res.ok()) throw new Error(`Fixture request failed (${res.status()})`);
    return res.json();
  };
  const project = await get(`/api/v1/projects/${process.env.E2E_PROJECT_ID}`);
  const workspace = await get(`/api/v1/workspaces/${project.workspace_id}`);
  const canonical = `/w/${workspace.slug}/p/${project.slug}`;
  // Assert the requested comment exists in the real API before trusting a UI check.
  const comments = await get(`/api/v1/tasks/${process.env.E2E_TASK_ID}/comments`);
  const items = Array.isArray(comments) ? comments : comments.items;
  if (!items?.some(c => c.id === process.env.E2E_COMMENT_ID)) throw new Error('Comment fixture is absent');

  const save = async (page, label, width) => {
    await page.screenshot({ path: `${output}/${label}-${width}.png` });
    // Static DOM/CSS with restored scroll offsets for page-shot: no scripts,
    // auth cookies or API bodies are replayed. UI text is the rendered fixture.
    const html = await page.evaluate(() => {
      const clone = document.documentElement.cloneNode(true);
      const source = [document.documentElement, ...document.documentElement.querySelectorAll('*')];
      const target = [clone, ...clone.querySelectorAll('*')];
      source.forEach((node, i) => {
        if (!node.scrollTop && !node.scrollLeft) return;
        for (const child of target[i].children) {
          child.style.translate = `${-node.scrollLeft}px ${-node.scrollTop}px`;
        }
      });
      clone.querySelectorAll('script, link').forEach(node => node.remove());
      const css = [...document.styleSheets].flatMap(sheet => [...sheet.cssRules].map(rule => rule.cssText)).join('\n');
      const style = document.createElement('style');
      style.textContent = `${css}\n*,*::before,*::after{animation:none!important;transition:none!important}`;
      clone.querySelector('head').append(style);
      return '<!doctype html>' + clone.outerHTML;
    });
    await writeFile(`${output}/${label}-${width}.html`, html);
  };
  for (const width of [1440, 393]) {
    for (const label of ['project', 'missing', 'comment']) {
      const page = await context.newPage();
      await page.setViewportSize({ width, height: width === 393 ? 852 : 900 });
      const path = label === 'project' ? `/p/${project.id}` : label === 'missing'
        ? '/p/00000000-0000-4000-8000-000000000000'
        : `/t/${process.env.E2E_TASK_ID}?comment=${process.env.E2E_COMMENT_ID}`;
      let stage = 'navigation';
      try {
        await page.goto(`${base}${path}`, { waitUntil: 'domcontentloaded' });
        if (label === 'project') {
          stage = 'canonical URL';
          await expect(page).toHaveURL(`${base}${canonical}`, { timeout: 20_000 });
          stage = 'board content';
          await expect(page.locator('[data-testid="board-column"]').first()).toBeVisible({ timeout: 20_000 });
        } else if (label === 'missing') {
          stage = 'not found content';
          await expect(page.getByRole('heading', { name: 'Project not found', exact: true })).toBeVisible({ timeout: 20_000 });
          await expect(page.getByText("Project not found or you don't have access.", { exact: true })).toBeVisible();
          await expect(page).toHaveURL(`${base}${path}`);
        } else {
          const comment = page.locator(`[data-comment-id="${process.env.E2E_COMMENT_ID}"]:visible`).first();
          stage = 'comment viewport';
          await expect(comment).toBeInViewport({ timeout: 30_000 });
          stage = 'comment highlight';
          await expect(comment).toHaveClass(/border-yellow-400/);
          stage = 'comment query';
          await expect(page).toHaveURL(new RegExp(`\\?comment=${process.env.E2E_COMMENT_ID}$`));
        }
        stage = 'horizontal overflow';
        if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error('Horizontal overflow');
        stage = 'snapshot';
        await save(page, label, width);
        console.log(`${label} ${width}: PASS`);
      } catch {
        // Do not print Playwright's call log: intercepted requests can contain auth.
        failures.push(`${label} ${width}: ${stage}`);
        console.log(`${label} ${width}: FAIL (${stage})`);
        await page.screenshot({ path: `${output}/${label}-${width}-failed.png` });
      } finally { await page.close(); }
    }
  }
  await context.close();
} finally { await browser.close(); }
await writeFile(`${output}/verdict.json`, JSON.stringify({ failures }, null, 2));
console.log(`PROJECT DEEP-LINK: ${failures.length ? 'FAIL' : 'PASS'} (${failures.length} violations)`);
if (failures.length) process.exitCode = 1;
