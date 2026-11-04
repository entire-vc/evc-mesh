import { test, expect, type Locator, type Page } from "@playwright/test";
import { mkdirSync, appendFileSync } from "node:fs";
// Operational constraints: one renderer on the board card (compact) and task detail (full). Loopback fixture; the
// runtime_state is injected on the way through, nothing is written. $CONSTRAINT_SHOTS names the screenshot/walk-log dir.
const API = "http://127.0.0.1:8019";
const SHOTS = process.env.CONSTRAINT_SHOTS ?? (process.env.CI ? "constraint-shots" : undefined);
const BOOT_MS = 60_000;
const LONG_REF = `decision:@${"very-long-recipient-name-".repeat(8)}`;
const k = (id: string, cls: string, over: Record<string, unknown> = {}) => ({
  id, class: cls, reason_code: "local_wait", next_actor: { kind: "agent", name: "Developer One" },
  wake: { predicate: "card:#00000000", ref: "card:#00000000" }, source: { kind: "mesh" },
  observed_at: "2026-11-03T10:00:00Z", stale: false, ...over,
});
const STORIES: Record<string, { constraints: unknown[] }> = {
  "fixture-task-315": { constraints: [
    k("a", "mesh_dependency", { reason_code: "mesh_dependency" }), k("b", "local_wait", { wake: { predicate: "mr:group/example!3", ref: "mr:group/example!3" } }),
    k("c", "ci_capacity", { reason_code: "ci_capacity", ci: { state: "capacity", scope: "runner-pool", position: 3 } }), k("d", "review_rejection", { reason_code: "local_wait" }),
  ] },
  "fixture-task-316": { constraints: [
    k("e", "expired_authority", { stale: true }), k("f", "write_acl", { reason_code: "access_denied", wake: { predicate: "card:#deadbeef", ref: "card:#deadbeef" } }),
  ] },
  "fixture-task-317": { constraints: [
    k("g", "brand_new_class", { reason_code: "brand_new_code", reason: "free text reason", next_actor: { kind: "unknown" }, wake: { predicate: "unknown" }, wake_ref: null }),
  ] },
  "fixture-task-318": { constraints: [
    k("h", "local_wait", { wake: { predicate: LONG_REF, ref: LONG_REF }, source: { kind: "mesh", ref: "x".repeat(120) } }),
    k("i", "ci_capacity", { ci: { state: "unavailable", scope: "must-not-show", position: 9 } }),
  ] },
};

// Independent oracle: what a person must see for every constraint, written down by hand (not read back from the renderer).
interface Want {
  cls: string; reason: string; ref: string | null; href: string | RegExp | null; stale: boolean; ci: string | null;
  actor: string | null; wake: string | null; source: string | null; timeKnown: boolean;
}
const L: Record<string, { cls: Record<string, string>; reason: Record<string, string>; unknown: string; timeUnknown: string; observed: string; stale: string; ci: [string, string, string, string]; restricted: string }> = {
  "en-US": {
    cls: { mesh_dependency: "Dependency", local_wait: "Wait", ci_capacity: "CI", review_rejection: "Review", expired_authority: "Authority", write_acl: "Access", unknown: "Constraint" },
    reason: { mesh_dependency: "Blocked by another card", local_wait: "Waiting for an event", ci_capacity: "Waiting for CI" },
    unknown: "unknown", timeUnknown: "time unknown", observed: "observed", stale: "data is stale", ci: ["Waiting for CI capacity", "CI status unavailable", "scope", "position"], restricted: "Details are restricted",
  },
  "ru-RU": {
    cls: { mesh_dependency: "Зависимость", local_wait: "Ожидание", ci_capacity: "CI", review_rejection: "Ревью", expired_authority: "Полномочия", write_acl: "Доступ", unknown: "Ограничение" },
    reason: { mesh_dependency: "Заблокировано другой карточкой", local_wait: "Ждёт события", ci_capacity: "Ждёт CI" },
    unknown: "неизвестно", timeUnknown: "время неизвестно", observed: "замечено", stale: "данные устарели", ci: ["Ждёт свободный CI", "Статус CI недоступен", "область", "позиция"], restricted: "Подробности скрыты",
  },
};
const CARD_HREF = "/t/00000000";
const MR_HREF = /^https:\/\/gitlab\.com\/group\/example\/-\/merge_requests\/3$/;
const wants = (lng: string): Record<string, Want[]> => {
  const l = L[lng];
  const base = { actor: "Developer One", wake: "card:#00000000", ref: "#00000000", href: CARD_HREF as string | RegExp | null, source: "mesh", stale: false, ci: null, timeKnown: true };
  return {
    "fixture-task-315": [
      { ...base, cls: "mesh_dependency", reason: l.reason.mesh_dependency },
      { ...base, cls: "local_wait", reason: l.reason.local_wait, wake: "mr:group/example!3", ref: "group/example!3", href: MR_HREF },
      { ...base, cls: "ci_capacity", reason: l.reason.ci_capacity, ci: `${l.ci[0]}, ${l.ci[2]} runner-pool, ${l.ci[3]} 3` },
      { ...base, cls: "review_rejection", reason: l.reason.local_wait },
    ],
    "fixture-task-316": [
      { ...base, cls: "expired_authority", reason: l.reason.local_wait, stale: true },
      // redacted: nothing the server restricted is shown, even though the payload carried an owner and a ref
      { cls: "write_acl", reason: l.restricted, ref: null, href: null, stale: false, ci: null, actor: null, wake: null, source: null, timeKnown: false },
    ],
    "fixture-task-317": [
      { ...base, cls: "unknown", reason: "free text reason", actor: null, wake: null, ref: null, href: null },
    ],
    "fixture-task-318": [
      { ...base, cls: "local_wait", reason: l.reason.local_wait, wake: LONG_REF, ref: LONG_REF, href: null, source: `mesh ${"x".repeat(120)}` },
      // fixture default reason_code is local_wait; only the 315 CI entry sets ci_capacity
      { ...base, cls: "ci_capacity", reason: l.reason.local_wait, ci: l.ci[1] },
    ],
  };
};

const errors: string[] = [];
const walk = (line: string) => { if (SHOTS) { mkdirSync(SHOTS, { recursive: true }); appendFileSync(`${SHOTS}/walk.log`, `${line}\n`); } };

const arm = async (page: Page) => {
  errors.length = 0;
  page.on("console", (m) => { if (m.type() === "error") errors.push(`console: ${m.text()}`); });
  page.on("pageerror", (e) => errors.push(`pageerror: ${e.message}`));
  page.on("response", (r) => { if (r.status() >= 500 && r.url().startsWith(API)) errors.push(`5xx ${r.url()}`); });
  const inject = (t: Record<string, unknown>) => {
    const s = STORIES[String(t.id)];
    return s ? { ...t, assignee_type: "agent", assignee_name: "Developer Two", assignee_id: "aaaaaaaa-1111-1111-1111-111111111111", runtime_state: { kind: "waiting", constraints: s.constraints } } : t;
  };
  await page.route(/\/projects\/[^/]+\/tasks(\?|$)/, async (route) => {
    const res = await route.fetch(); const body = await res.json();
    body.items = body.items.map(inject);
    await route.fulfill({ response: res, json: body });
  });
  await page.route(/\/api\/v1\/tasks\/fixture-task-\d+$/, async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const res = await route.fetch(); await route.fulfill({ response: res, json: inject(await res.json()) });
  });
};

interface Seen {
  cls: string | null; label: string | null; reason: string | null; ref: string | null; href: string | null; stale: string | null;
  staleLabel: string | null; ci: string | null; compact: string | null; actor: string | null; wake: string | null; sourceTime: string | null; labels: string[];
}
// Reads every user-visible field of every constraint inside ONE visible root (a board card or the visible detail row).
const values = (root: Locator): Promise<Seen[]> => root.evaluate((el) => {
  const text = (n: Element | null | undefined) => n?.textContent ?? null;
  return Array.from(el.querySelectorAll('[data-testid="runtime-constraint"]')).map((li) => {
    const refEl = li.querySelector('[data-testid="constraint-ref"]');
    return {
      cls: li.getAttribute("data-constraint-class"),
      label: text(li.querySelector('[data-testid="constraint-class"]')),
      reason: text(li.querySelector('[data-testid="constraint-reason"]')),
      ref: text(refEl),
      href: refEl?.getAttribute("href") ?? null,
      stale: li.getAttribute("data-constraint-stale"),
      staleLabel: text(li.querySelector('[data-testid="constraint-stale"]')),
      ci: text(li.querySelector('[data-testid="constraint-ci"]')),
      compact: text(li.querySelector('[data-testid="constraint-who-wake"]')),
      actor: text(li.querySelector('[data-testid="constraint-next-actor"]')),
      wake: text(li.querySelector('[data-testid="constraint-wake"]')),
      sourceTime: text(li.querySelector('[data-testid="constraint-source"]')),
      labels: Array.from(li.querySelectorAll("dt")).map((d) => d.textContent ?? ""),
    };
  });
});

// The surface-independent view of a constraint: board and detail must agree on all of it.
const common = (v: Seen) => ({ cls: v.cls, label: v.label, reason: v.reason, ref: v.ref, href: v.href, stale: v.stale, staleLabel: v.staleLabel, ci: v.ci });
// The compact board line is "<Next>: a · <Wakes on>: b · <source> · <observed t>"; rebuild it from the detail rows and compare exactly.
const compactOf = (d: Seen) => `${d.labels[0]}: ${d.actor} · ${d.labels[1]}: ${d.wake} · ${d.sourceTime}`;

const checkAgainstWant = (id: string, lng: string, seen: Seen[], want: Want[]) => {
  const l = L[lng];
  expect(seen.length, `${id} constraint count`).toBe(want.length);
  seen.forEach((v, i) => {
    const w = want[i]; const at = `${id}[${i}] ${w.cls}`;
    expect(v.cls, `${at} class`).toBe(w.cls);
    expect(v.label, `${at} class label`).toBe(l.cls[w.cls]);
    expect(v.reason, `${at} reason`).toBe(w.reason);
    expect(v.ref, `${at} ref`).toBe(w.ref);
    if (w.href instanceof RegExp) expect(v.href, `${at} href`).toMatch(w.href); else expect(v.href, `${at} href`).toBe(w.href);
    expect(v.stale, `${at} stale flag`).toBe(String(w.stale));
    expect(v.staleLabel, `${at} stale label`).toBe(w.stale ? l.stale : null);
    expect(v.ci, `${at} ci`).toBe(w.ci);
    expect(v.actor, `${at} next actor`).toBe(w.actor ?? l.unknown);
    expect(v.wake, `${at} wake`).toBe(w.wake ?? l.unknown);
    const [src, time] = (v.sourceTime ?? "").split(" · ");
    expect(src, `${at} source`).toBe(w.source ?? l.unknown);
    // fixture observed_at is 2026-11-03T10:00:00Z, shown as 10:00 in the pinned UTC timezone
    expect(time, `${at} observed time`).toBe(w.timeKnown ? `${l.observed} 10:00` : l.timeUnknown);
  });
};

for (const lng of ["en-US", "ru-RU"]) for (const vp of [{ width: 1440, height: 900 }, { width: 393, height: 852 }]) {
  test.describe(`${lng} ${vp.width}`, () => {
    // timezone is pinned so the clock printed for observed_at (10:00Z) is deterministic
    test.use({ viewport: vp, locale: lng, timezoneId: "UTC" });

    test("board card and detail render the same constraints; refs safe; no overflow, no errors", async ({ page }) => {
      await arm(page);
      const tasks = page.waitForResponse((r) => r.url().startsWith(API) && /\/tasks(\?|$)/.test(r.url()), { timeout: BOOT_MS });
      await page.goto("/w/board-fixture/p/contenthub");
      await tasks;
      await expect(page.locator('[data-testid="task-card"]').first()).toBeVisible({ timeout: BOOT_MS });
      const tag = `${lng}-${vp.width}`;
      const want = wants(lng);
      for (const id of Object.keys(STORIES)) {
        const card = page.locator(`[data-testid="task-card"][data-task-id="${id}"]`);
        await card.scrollIntoViewIfNeeded();
        await expect(card.locator('[data-testid="runtime-constraint"]').first()).toBeVisible();
        const more = card.getByTestId("constraint-more");
        if (await more.count()) await more.click();
        const board = await values(card);
        // every constraint is reachable on the board (compact expands in place); the card itself does not overflow with long refs
        expect(board.length, `${id} constraints on board`).toBe(want[id].length);
        const boxed = await card.evaluate((el) => ({ card: el.scrollWidth - el.clientWidth, list: (el.querySelector('[data-testid="runtime-constraints"]') as HTMLElement | null)?.scrollWidth ?? 0, listBox: (el.querySelector('[data-testid="runtime-constraints"]') as HTMLElement | null)?.clientWidth ?? 0 }));
        expect(boxed.card, `${id} board card overflow`).toBeLessThanOrEqual(1);
        expect(boxed.list - boxed.listBox, `${id} board constraint list overflow`).toBeLessThanOrEqual(1);
        if (id === "fixture-task-315") await page.screenshot({ path: SHOTS ? `${SHOTS}/board-${tag}.png` : undefined });
        await page.goto(`/t/${id}`);
        // the task panel mounts the property row twice (one per layout); read the one that is actually visible
        const row = page.locator('[data-testid="runtime-state-row"]:visible').first();
        await expect(row).toBeVisible({ timeout: BOOT_MS });
        const detail = await values(row);
        // 1) detail matches the hand-written oracle (class, reason, ref, safe href, stale, CI, actor, wake, source, time)
        checkAgainstWant(id, lng, detail, want[id]);
        // 2) board shows exactly the same values as detail, including the compact actor / wake / source / time line
        expect(board.map(common), `${id} board == detail`).toEqual(detail.map(common));
        expect(board.map((v) => v.compact), `${id} board who/wake/source/time == detail`).toEqual(detail.map(compactOf));
        if (id === "fixture-task-316") {
          // stale shown as stale; redacted write_acl leaks no ref/owner even though the payload carried them
          await expect(row.getByTestId("constraint-stale")).toHaveCount(1);
          const redacted = row.locator('[data-constraint-class="write_acl"]');
          await expect(redacted.getByTestId("constraint-ref")).toHaveCount(0);
          await expect(redacted).not.toContainText("deadbeef");
        }
        if (id === "fixture-task-317") await expect(row.getByTestId("constraint-reason")).toHaveText("free text reason");
        if (id === "fixture-task-318") {
          // unsafe/long ref is plain text, never a link; unavailable CI shows neither scope nor position
          await expect(row.getByTestId("constraint-ref").first()).not.toHaveAttribute("href", /.*/);
          await expect(row.locator('[data-constraint-class="ci_capacity"]')).not.toContainText("must-not-show");
        }
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        expect(overflow, `${id} no horizontal scroll`).toBeLessThanOrEqual(0);
        if (SHOTS) { mkdirSync(SHOTS, { recursive: true }); await page.screenshot({ path: `${SHOTS}/detail-${id}-${tag}.png` }); }
        walk(`${tag} ${id}: board==detail (${board.length} constraints), classes ${board.map((v) => v.cls).join(",")} — ok`);
        await page.goto("/w/board-fixture/p/contenthub");
        await expect(page.locator('[data-testid="task-card"]').first()).toBeVisible({ timeout: BOOT_MS });
      }
      expect(errors, "console.error / pageerror / 5xx").toEqual([]);
    });
  });
}
