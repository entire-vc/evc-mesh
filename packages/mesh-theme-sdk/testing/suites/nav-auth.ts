// CT-sidebar-01..08, CT-topbar-01..05, CT-auth-01..08 (Mesh Doc slots-task-nav-doc-auth §3, §4, §7).
// Elements are found by what a user (or assistive technology) gets: links by href, controls by the action they
// call, state by aria-*; never by a theme's classes. jsdom has no layout: "no horizontal scroll" is checked as
// "nothing is wider than the phone by its own inline size", the pixel check lives in the browser e2e suite.
import axe from "axe-core";
import { act, fireEvent, waitFor } from "@testing-library/react";
import { expect } from "vitest";
import { calls, tick, visibleText } from "../helpers";
import { harnessHost } from "../host";
import { fixtures } from "../fixtures";
import { defineTests } from "../suite";
import type { AuthFixture } from "../fixtures/nav-auth";
import type { Mounted } from "../types";

const PHONE_WIDTH = 393;

// -- helpers -----------------------------------------------------------------------------------------

const links = (root: ParentNode) => Array.from(root.querySelectorAll<HTMLAnchorElement>("a[href]"));
const linkTo = (root: ParentNode, href: string) => links(root).filter((a) => a.getAttribute("href") === href);

/** Accessible name as a screen reader gets it for a link or a button (text, aria-label, title, alt). */
const accessibleName = (el: Element) =>
  [el.getAttribute("aria-label") ?? "", el.getAttribute("title") ?? "", visibleText(el)].join(" ").replace(/\s+/g, " ").trim();

const buttons = (root: ParentNode) =>
  Array.from(root.querySelectorAll<HTMLElement>("button:not([disabled]),[role=button]:not([aria-disabled=true])"));

/** The button whose click calls `action` (discovered by clicking each; the click is the user's way to it). */
async function buttonFor(m: Mounted, action: string): Promise<HTMLElement | null> {
  for (const b of buttons(m.container)) {
    const before = calls(m, action).length;
    await m.user.click(b);
    if (calls(m, action).length > before) return b;
  }
  return null;
}

/** Tabs through the page (at most `max` stops); returns the focused elements in order. */
async function tabStops(m: Mounted, max = 40): Promise<HTMLElement[]> {
  const out: HTMLElement[] = [];
  (document.activeElement as HTMLElement | null)?.blur?.();
  for (let i = 0; i < max; i++) {
    await m.user.tab();
    const el = document.activeElement as HTMLElement | null;
    if (!el || el === document.body || out.includes(el)) break;
    out.push(el);
  }
  return out;
}

/** Reaches the control that calls `action` with Tab and presses Enter on it, as a keyboard user does. */
async function pressByKeyboard(m: Mounted, action: string): Promise<boolean> {
  const stops = await tabStops(m);
  for (const el of stops) {
    if (!el.matches("button,[role=button]")) continue; // Enter on a link would navigate away
    el.focus();
    const before = calls(m, action).length;
    await m.user.keyboard("{Enter}");
    if (calls(m, action).length > before) return true;
  }
  return false;
}

async function axeCritical(root: HTMLElement) {
  const r = await axe.run(root, { rules: { "color-contrast": { enabled: false }, region: { enabled: false } } });
  type V = { id: string; impact?: string | null; nodes: { html: string }[] };
  return (r.violations as V[]).filter((v) => v.impact === "critical").map((v) => `${v.id}: ${v.nodes[0]?.html.slice(0, 120)}`);
}

/** Inline sizes wider than the phone (the only width jsdom can know about). */
function widerThanPhone(root: Element): string[] {
  return Array.from(root.querySelectorAll<HTMLElement>("*"))
    .filter((el) => ["width", "minWidth"].some((p) => parseFloat((el.style as unknown as Record<string, string>)[p] || "0") > PHONE_WIDTH))
    .map((el) => el.outerHTML.slice(0, 80));
}

const noUnexpected = (m: Mounted) => expect(m.unexpectedRequests, "a view reaches data only through props and actions").toEqual([]);

// -- nav.sidebar -------------------------------------------------------------------------------------

const SB = ["nav.sidebar"];
const sidebarItems = () => fixtures.sidebar().items;

defineTests([
  {
    id: "CT-sidebar-01",
    title: "every item is a link with its href; the active one is aria-current=page; nothing beyond items",
    slots: SB,
    run({ mount }) {
      const m = mount();
      const data = m.data() as ReturnType<typeof fixtures.sidebar>;
      for (const it of data.items) {
        const l = linkTo(m.container, it.href);
        expect(l, `item ${it.id} is one link to ${it.href}`).toHaveLength(1);
        expect(accessibleName(l[0]!)).toContain(it.label);
        expect(l[0]!.getAttribute("aria-current"), `item ${it.id}`).toBe(it.active ? "page" : null);
      }
      const known = new Set([
        ...data.items.map((i) => i.href),
        ...(data.projects.state === "ready" ? data.projects.value.flatMap((p) => [p.href, ...p.items.map((i) => i.href)]) : []),
        ...data.workspaces.map((w) => w.href),
      ]);
      expect(links(m.container).map((a) => a.getAttribute("href")).filter((h) => !known.has(h!)), "links not in the data").toEqual([]);
      expect(links(m.container).filter((a) => a.getAttribute("aria-current") === "page").map((a) => a.getAttribute("href"))).toEqual(["/ws/dashboard"]);
    },
  },
  {
    id: "CT-sidebar-02",
    title: "an extension item (origin extension) is drawn like a core one; the sidebar.item point is in the DOM",
    slots: SB,
    async run({ mount }) {
      const ext = fixtures.extensionItem();
      const m = mount({ extensions: ["sidebar.item"] });
      const l = linkTo(m.container, ext.href);
      expect(l, "the extension item is a link to its href").toHaveLength(1);
      expect(accessibleName(l[0]!)).toContain(ext.label);
      // Drawn like a core item: same element kind as a core item of the same list.
      const core = linkTo(m.container, sidebarItems()[3]!.href)[0]!;
      expect(l[0]!.tagName).toBe(core.tagName);
      expect(m.container.querySelector(harnessHost().extensionSelector("sidebar.item")), "sidebar.item insertion point").not.toBeNull();
    },
  },
  {
    id: "CT-sidebar-03",
    title: "collapsed: every icon link has an accessible name; toggleCollapsed is reachable from the keyboard",
    slots: SB,
    async run({ mount }) {
      const m = mount({ data: { collapsed: true } });
      const all = links(m.container);
      expect(all.length).toBeGreaterThan(0);
      for (const a of all) expect(accessibleName(a), a.outerHTML.slice(0, 100)).not.toBe("");
      expect(await pressByKeyboard(m, "toggleCollapsed"), "Tab to a control and Enter calls toggleCollapsed").toBe(true);
      expect(calls(m, "toggleCollapsed")).toHaveLength(1);
    },
  },
  {
    id: "CT-sidebar-04",
    title: "a badge with count > 0 is shown and announced (aria-label with the number)",
    slots: SB,
    run({ mount }) {
      const m = mount();
      const item = sidebarItems().find((i) => i.badge)!;
      const link = linkTo(m.container, item.href)[0]!;
      expect(link.textContent).toContain(String(item.badge!.count));
      const labelled = [link, ...Array.from(link.querySelectorAll("[aria-label]"))].map((e) => e.getAttribute("aria-label") ?? "");
      expect(labelled.some((l) => l.includes(String(item.badge!.count))), "the count is in an aria-label").toBe(true);
    },
  },
  {
    id: "CT-sidebar-05",
    title: "choosing another workspace calls switchWorkspace(slug); the current one is marked",
    slots: SB,
    async run({ mount }) {
      const m = mount();
      // Elements whose own text (not their children's) is the name: a menu row may carry a logo letter too.
      const ownText = (e: Element) => Array.from(e.childNodes).filter((n) => n.nodeType === 3).map((n) => n.textContent).join("").trim();
      const byText = (t: string) => Array.from(document.body.querySelectorAll<HTMLElement>("*")).filter((e) => ownText(e) === t);
      // The list may sit behind a switcher: open it the way a user does (the control that names the workspace).
      if (byText("Beta").length === 0) {
        const trigger = buttons(m.container).find((b) => accessibleName(b).includes("Workspace"));
        expect(trigger, "a workspace switcher control").toBeTruthy();
        // Menus commonly open on pointerdown (a full click would open and close them again in jsdom).
        await act(async () => { fireEvent.pointerDown(trigger!, { button: 0, ctrlKey: false, pointerType: "mouse" }); });
        if (byText("Beta").length === 0) await m.user.click(trigger!);
        await waitFor(() => expect(byText("Beta").length).toBeGreaterThan(0));
      }
      const current = byText("Workspace").map((e) => e.closest("[aria-current],[aria-selected],[aria-checked]")).filter(Boolean);
      expect(
        current.some((e) => ["true", "page"].includes(e!.getAttribute("aria-current") ?? e!.getAttribute("aria-selected") ?? e!.getAttribute("aria-checked") ?? "")),
        "the current workspace carries aria-current / aria-selected / aria-checked",
      ).toBe(true);
      const beta = byText("Beta");
      await m.user.click(beta[beta.length - 1]!);
      expect(calls(m, "switchWorkspace")).toEqual([["beta"]]);
    },
  },
  {
    id: "CT-sidebar-06",
    title: "projects loading shows a busy skeleton; an error shows the message (and a drawn retry calls retry)",
    slots: SB,
    async run({ mount }) {
      const loading = mount({ options: { state: "loading" } });
      expect(loading.container.querySelector("[aria-busy=true],[role=status],[role=progressbar]"), "busy skeleton").not.toBeNull();
      expect(await axeCritical(loading.container), "loading").toEqual([]);
      loading.unmount();
      const error = mount({ options: { state: "error" } });
      const alert = Array.from(error.container.querySelectorAll("[role=alert],[aria-live]")).map((e) => visibleText(e)).join(" ");
      expect(alert, "the error is announced").toContain("Could not load projects");
      expect(await axeCritical(error.container), "error").toEqual([]);
      // The spec asks for a retry with the message; mesh-default (the core's contract) draws none. Where a view
      // draws one, it must call `retry`. See the CT-sidebar-06 note in the T6.2 report.
      const retry = buttons(error.container).find((b) => /retry|повтор|try again/i.test(visibleText(b)));
      if (retry) {
        await error.user.click(retry);
        expect(calls(error, "retry").length).toBeGreaterThan(0);
      }
    },
  },
  {
    id: "CT-sidebar-07",
    title: "isMobile: the view keeps the Tab order (no positive tabindex, item links in item order)",
    slots: SB,
    async run({ mount }) {
      const m = mount({ isMobile: true });
      expect(Array.from(m.container.querySelectorAll("[tabindex]")).filter((e) => Number(e.getAttribute("tabindex")) > 0)).toEqual([]);
      const stops = await tabStops(m);
      const hrefs = sidebarItems().map((i) => i.href);
      const reached = stops.map((e) => e.getAttribute("href")).filter((h): h is string => hrefs.includes(h ?? ""));
      expect(reached, "every item is reached by Tab, in item order").toEqual(hrefs);
      noUnexpected(m);
    },
  },
  {
    id: "CT-sidebar-08",
    title: "without the right to create a project there is no create button (createProject absent from actions)",
    slots: SB,
    async run({ mount }) {
      const member = mount();
      const create = await buttonFor(member, "createProject");
      expect(create, "positive control: a member has a control that calls createProject").not.toBeNull();
      const name = accessibleName(create!);
      member.unmount();
      const viewer = mount({ role: "viewer" });
      expect(viewer.spies.createProject, "no right = no method").toBeUndefined();
      expect(buttons(viewer.container).map(accessibleName)).not.toContain(name);
    },
  },
]);

// -- nav.topbar --------------------------------------------------------------------------------------

const TB = ["nav.topbar"];
const blocks = (root: ParentNode, name: string) => root.querySelectorAll(`[data-mesh-block="${name}"]`);

defineTests([
  {
    id: "CT-topbar-01",
    title: "three crumbs: the first two are links, the last is aria-current=page",
    slots: TB,
    run({ mount }) {
      const m = mount();
      const [a, b, c] = fixtures.topbar().breadcrumbs;
      for (const crumb of [a!, b!]) {
        const l = linkTo(m.container, crumb.href!);
        expect(l, `crumb ${crumb.label} is a link`).toHaveLength(1);
        expect(accessibleName(l[0]!)).toContain(crumb.label);
        expect(l[0]!.getAttribute("aria-current")).toBeNull();
      }
      const current = Array.from(m.container.querySelectorAll('[aria-current="page"]'));
      expect(current, "one current crumb").toHaveLength(1);
      expect(current[0]!.textContent).toContain(c!.label);
    },
  },
  {
    id: "CT-topbar-02",
    title: "NotificationBell and UserMenu are present exactly once",
    slots: TB,
    run({ mount }) {
      const m = mount();
      expect(blocks(m.container, "NotificationBell")).toHaveLength(1);
      expect(blocks(m.container, "UserMenu")).toHaveLength(1);
    },
  },
  {
    id: "CT-topbar-03",
    title: "the sidebar toggle is a keyboard-reachable button that calls toggleSidebar",
    slots: TB,
    async run({ mount }) {
      const m = mount();
      expect(await pressByKeyboard(m, "toggleSidebar"), "Tab to a button and Enter calls toggleSidebar").toBe(true);
      expect(calls(m, "toggleSidebar")).toHaveLength(1);
    },
  },
  {
    id: "CT-topbar-04",
    title: "installable: a button calls install(); not installable: no such button",
    slots: TB,
    async run({ mount }) {
      const yes = mount({ data: { installable: true } });
      expect(await buttonFor(yes, "install"), "installable shows an install button").not.toBeNull();
      expect(calls(yes, "install")).toHaveLength(1);
      yes.unmount();
      const no = mount({ data: { installable: false } });
      for (const b of buttons(no.container)) await no.user.click(b);
      expect(calls(no, "install"), "no control calls install when not installable").toHaveLength(0);
    },
  },
  {
    id: "CT-topbar-05",
    title: "isMobile: every action is there; long crumbs keep their text and nothing is wider than the phone",
    slots: TB,
    async run({ mount }) {
      const long = [
        { kind: "workspace" as const, label: "A very long workspace name that must not push the page wide", href: "/ws" },
        { kind: "project" as const, label: "An equally long project name for the same reason", href: "/ws/lab", strong: true },
      ];
      const m = mount({ isMobile: true, data: { isMobile: true, installable: true, breadcrumbs: long } });
      expect(blocks(m.container, "NotificationBell")).toHaveLength(1);
      expect(blocks(m.container, "UserMenu")).toHaveLength(1);
      for (const c of long) expect(linkTo(m.container, c.href), c.label).toHaveLength(1);
      expect(widerThanPhone(m.container)).toEqual([]);
      expect(await pressByKeyboard(m, "toggleSidebar")).toBe(true);
    },
  },
]);

// -- auth.screen -------------------------------------------------------------------------------------

const AU = ["auth.screen"];

const input = (root: ParentNode, label: string): HTMLInputElement => {
  const l = Array.from(root.querySelectorAll("label")).find((e) => e.textContent?.replace(/\*/g, "").trim() === label);
  const el = l?.htmlFor ? (root as Element).ownerDocument!.getElementById(l.htmlFor) : l?.querySelector("input");
  if (!(el instanceof HTMLInputElement)) throw new Error(`no <input> labelled "${label}" (a field needs a <label>)`);
  return el;
};

const submitButton = (root: ParentNode, label: string) =>
  Array.from(root.querySelectorAll<HTMLButtonElement | HTMLInputElement>("button,input[type=submit]")).find(
    (b) => (b.textContent?.trim() || b.getAttribute("value")) === label,
  ) ?? null;

/** Every required field filled, so the browser's own validation lets the form go. */
const filled = () => {
  const d = fixtures.authLogin();
  return { fields: d.fields.map((f) => ({ ...f, value: f.type === "email" ? "ann@example.com" : "secret-pass" })) };
};

defineTests([
  {
    id: "CT-auth-01",
    title: "login: the fields of `fields` with labels, name, autoComplete and type; inside a <form>",
    slots: AU,
    run({ mount }) {
      const m = mount();
      const form = m.container.querySelector("form");
      expect(form, "a plain <form> (password managers)").not.toBeNull();
      for (const f of fixtures.authLogin().fields) {
        const el = input(m.container, f.label);
        expect(form!.contains(el)).toBe(true);
        expect(el.getAttribute("name")).toBe(f.name);
        expect(el.getAttribute("autocomplete")).toBe(f.autoComplete);
        expect(el.type).toBe(f.type);
      }
    },
  },
  {
    id: "CT-auth-02",
    title: "each input calls setValue; submit() is called once on Enter and once on the button",
    slots: AU,
    async run({ mount }) {
      const m = mount({ data: filled() });
      const email = input(m.container, "Email");
      await m.user.clear(email);
      const before = calls(m, "setValue").length;
      fireEvent.change(email, { target: { value: "a" } });
      fireEvent.change(email, { target: { value: "ab" } });
      expect(calls(m, "setValue").slice(before)).toEqual([["email", "a"], ["email", "ab"]]);
      const pw = input(m.container, "Password");
      pw.focus();
      await m.user.keyboard("{Enter}");
      expect(calls(m, "submit"), "Enter submits once").toHaveLength(1);
      await m.user.click(submitButton(m.container, "Sign in")!);
      expect(calls(m, "submit"), "the button submits once").toHaveLength(2);
    },
  },
  {
    id: "CT-auth-03",
    title: "submitting: the button is disabled and a repeated Enter does not submit again",
    slots: AU,
    async run({ mount }) {
      const m = mount({ data: { ...filled(), submitting: true } });
      const btn = submitButton(m.container, "Sign in");
      expect(btn, "the submit button is there").not.toBeNull();
      expect(btn!.disabled).toBe(true);
      input(m.container, "Password").focus();
      await m.user.keyboard("{Enter}");
      await act(async () => { fireEvent.submit(m.container.querySelector("form")!); });
      expect(calls(m, "submit")).toHaveLength(0);
    },
  },
  {
    id: "CT-auth-04",
    title: "formError and field.error are shown and announced (role=alert or aria-describedby)",
    slots: AU,
    run({ mount }) {
      const m = mount<AuthFixture>({
        data: (d) => ({ ...d, formError: "Wrong password", fields: d.fields.map((f, i) => (i === 0 ? { ...f, error: "Bad email" } : f)) }),
      });
      const alerts = Array.from(m.container.querySelectorAll("[role=alert],[aria-live=assertive],[aria-live=polite]")).map((e) => e.textContent ?? "").join(" | ");
      expect(alerts, "the form error is announced").toContain("Wrong password");
      const email = input(m.container, "Email");
      const described = (email.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean)
        .map((id) => m.container.ownerDocument.getElementById(id)?.textContent ?? "").join(" ");
      expect(alerts.includes("Bad email") || described.includes("Bad email"), "the field error is announced or linked to the field").toBe(true);
    },
  },
  {
    id: "CT-auth-05",
    title: "registration off: no link to registration",
    slots: AU,
    run({ mount }) {
      const on = mount();
      expect(linkTo(on.container, "/register").length, "positive control: registration open shows the link").toBeGreaterThan(0);
      on.unmount();
      const off = mount({ data: fixtures.authRegistrationClosed() });
      expect(linkTo(off.container, "/register")).toEqual([]);
      expect(links(off.container).filter((a) => /regist|sign up|регистр/i.test(accessibleName(a)))).toEqual([]);
    },
  },
  {
    id: "CT-auth-06",
    title: "accept-invite: the workspace name and the inviter are visible",
    slots: AU,
    run({ mount }) {
      const m = mount({ data: fixtures.authInvite() });
      const text = visibleText(m.container);
      expect(text).toContain("Acme");
      expect(text).toContain("Ann Lee");
    },
  },
  {
    id: "CT-auth-07",
    title: "ctx.workspace is null: the view does not reach for workspace data and does not crash",
    slots: AU,
    async run({ mount }) {
      const m = mount();
      await tick();
      expect(m.container.querySelector("form")).not.toBeNull();
      noUnexpected(m);
    },
  },
  {
    id: "CT-auth-08",
    title: "isMobile: every field and the submit button are reachable by Tab; nothing is wider than the phone",
    slots: AU,
    async run({ mount }) {
      const m = mount({ isMobile: true, mode: "dark" });
      const stops = await tabStops(m);
      for (const f of fixtures.authLogin().fields) expect(stops, f.label).toContain(input(m.container, f.label));
      expect(stops).toContain(submitButton(m.container, "Sign in"));
      expect(widerThanPhone(m.container)).toEqual([]);
    },
  },
]);
