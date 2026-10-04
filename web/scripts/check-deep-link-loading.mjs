import { chromium } from '@playwright/test';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve, extname, sep } from 'node:path';

// Read-only authed regression check. No cookies, tokens, traces or API bodies
// are saved. Optional candidate assets use the same real API and auth origin.
const base = process.env.APP_BASE_URL;
for (const name of ['APP_BASE_URL', 'E2E_USER_EMAIL', 'E2E_USER_PASSWORD']) {
  if (!process.env[name]) throw new Error(`Missing ${name}`);
}
const output = process.env.DEEP_LINK_OUTPUT_DIR || 'deep-link-artifacts';
const assetRoot = process.env.DEEP_LINK_ASSET_DIR && resolve(process.env.DEEP_LINK_ASSET_DIR);
const missing = '00000000-0000-4000-8000-000000000000';
const failures = [];
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: true, ...(process.env.DEEP_LINK_BROWSER_CHANNEL ? { channel: process.env.DEEP_LINK_BROWSER_CHANNEL } : {}) });
try {
  const context = await browser.newContext({ serviceWorkers: 'block' });
  if (assetRoot) {
    const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2', '.json': 'application/json', '.webmanifest': 'application/manifest+json' };
    await context.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (url.origin !== new URL(base).origin || url.pathname.startsWith('/api/')) return route.continue();
      const path = resolve(assetRoot, route.request().resourceType() === 'document' ? 'index.html' : url.pathname.slice(1));
      if (!path.startsWith(assetRoot + sep)) throw new Error('Asset path outside candidate directory');
      await route.fulfill({ body: await readFile(path), contentType: mime[extname(path)] || 'application/octet-stream' });
    });
  }
  const login = await context.request.post(`${base}/api/v1/auth/login`, {
    data: { email: process.env.E2E_USER_EMAIL, password: process.env.E2E_USER_PASSWORD }, timeout: 20_000,
  }).catch(() => { throw new Error('Login transport failed (details suppressed)'); });
  if (login.status() !== 200 || !(await login.json()).tokens?.access_token) throw new Error(`Login failed (${login.status()})`);

  // Hold the REAL entity response for at least four seconds, so a fast API
  // cannot hide the bad loading state. The real API may report 403 for an
  // inaccessible document or 404 for a missing task; both are valid dead ends.
  await context.route(new RegExp(`/api/v1/(tasks|documents)/${missing}$`), async route => {
    const [response] = await Promise.all([route.fetch(), new Promise(resolve => setTimeout(resolve, 4000))]);
    if (![403, 404].includes(response.status())) failures.push(`Missing entity returned unexpected ${response.status()}`);
    await route.fulfill({ response });
  });
  for (const width of [1440, 393]) {
    for (const [kind, label] of [['t', 'Task'], ['d', 'Document']]) {
      const page = await context.newPage();
      await page.setViewportSize({ width, height: width === 393 ? 852 : 900 });
      await page.addInitScript(() => {
        window.__emptyProjectSeen = false;
        new MutationObserver(() => {
          if (document.body?.textContent.includes('No projects yet')) window.__emptyProjectSeen = true;
        }).observe(document, { childList: true, subtree: true, characterData: true });
      });
      await page.goto(`${base}/${kind}/${missing}`, { waitUntil: 'domcontentloaded' });
      // Proves the authenticated app shell has rendered, before timing the
      // three-second DOM observation (which already watches from navigation).
      await page.locator('aside').waitFor({ timeout: 30_000 });
      await page.screenshot({ path: `${output}/${kind}-${width}-loading.png` });
      // Static DOM/CSS snapshot lets page-shot reproduce the pending state
      // without saving or replaying a refresh cookie.
      const html = await page.evaluate(() => {
        const clone = document.documentElement.cloneNode(true);
        clone.querySelectorAll('script, link').forEach(node => node.remove());
        const css = [...document.styleSheets].flatMap(sheet => [...sheet.cssRules].map(rule => rule.cssText)).join('\n');
        const style = document.createElement('style');
        style.textContent = `${css}\n*,*::before,*::after{animation:none!important}`;
        clone.querySelector('head').append(style);
        return '<!doctype html>' + clone.outerHTML;
      });
      await writeFile(`${output}/${kind}-${width}-loading.html`, html);
      await page.waitForTimeout(3000);
      const emptySeen = await page.evaluate(() => window.__emptyProjectSeen);
      if (emptySeen) failures.push(`${kind} ${width}: No projects yet appeared during first 3s`);
      await page.getByRole('heading', { name: `${label} not found`, exact: true }).waitFor({ timeout: 30_000 });
      if (!await page.getByText(`${label} not found or you don't have access.`, { exact: true }).isVisible()) failures.push(`${kind} ${width}: final error missing`);
      await page.waitForTimeout(5000);
      const loadingGone = await page.getByTestId('sidebar-projects-loading').count() === 0;
      if (!loadingGone) failures.push(`${kind} ${width}: project skeleton remains 5s after not found`);
      if (await page.getByText('No projects yet', { exact: true }).count()) failures.push(`${kind} ${width}: unselected workspace shows empty project state`);
      if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) failures.push(`${kind} ${width}: horizontal overflow`);
      await page.screenshot({ path: `${output}/${kind}-${width}-not-found.png` });
      const finalHtml = await page.evaluate(() => {
        const clone = document.documentElement.cloneNode(true);
        clone.querySelectorAll('script, link').forEach(node => node.remove());
        clone.querySelectorAll('img').forEach(node => { if (node.getAttribute('src')) node.src = new URL(node.getAttribute('src'), location.href).href; });
        const css = [...document.styleSheets].flatMap(sheet => [...sheet.cssRules].map(rule => rule.cssText)).join('\n');
        const style = document.createElement('style');
        style.textContent = `${css}\n*,*::before,*::after{animation:none!important}`;
        clone.querySelector('head').append(style);
        return '<!doctype html>' + clone.outerHTML;
      });
      await writeFile(`${output}/${kind}-${width}-not-found.html`, finalHtml);
      console.log(`${kind} ${width}: first 3s=${emptySeen ? 'FAIL' : 'PASS'}; final not found=PASS; final +5s skeleton absent=${loadingGone ? 'PASS' : 'FAIL'}`);
      await page.close();
    }
  }
  await context.close();
} finally { await browser.close(); }
await writeFile(`${output}/verdict.json`, JSON.stringify({ failures }, null, 2));
console.log(`DEEP-LINK: ${failures.length ? 'FAIL' : 'PASS'} (${failures.length} violations)`);
if (failures.length) process.exitCode = 1;
