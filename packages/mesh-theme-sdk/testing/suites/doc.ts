// CT-doctree-01..07 and CT-docview-01..07 (Mesh Doc slots-task-nav-doc-auth §5, §6).
// doc.tree is found by the roles the contract names (tree / treeitem, aria-level / aria-expanded / aria-selected);
// doc.view by the core blocks it draws: each stand-in carries `data-mesh-block="<name>"` (see mounters/doc.tsx).
import { fireEvent, waitFor, within } from "@testing-library/react";
import { expect } from "vitest";
import type { DocTreeData } from "../../src/index";
import { fixtures } from "../fixtures";
import { button, calls, markup, tick, visibleText } from "../helpers";
import { defineTests } from "../suite";
import type { Mounted } from "../types";
import type { DocViewFrame } from "../mounters/doc";

const TREE = ["doc.tree"];
const VIEW = ["doc.view"];

// -- doc.tree helpers ------------------------------------------------------------------------------------

const items = (m: Mounted) => Array.from(m.container.querySelectorAll<HTMLElement>("[role=treeitem]"));

/** The treeitem whose accessible content names the document (`Doc <id>` in the fixture). */
function item(m: Mounted, id: string): HTMLElement {
  const el = items(m).find((i) => {
    const own = Array.from(i.querySelectorAll("a,[role=link]")).map((a) => a.textContent?.trim());
    return own.includes(`Doc ${id}`) || i.getAttribute("aria-label") === `Doc ${id}`;
  });
  if (!el) throw new Error(`no treeitem for document ${id}`);
  return el;
}

const focusIn = (el: Element) => el === document.activeElement || el.contains(document.activeElement);

/** What a control is called: its own aria-label / title and its visible content, each on its own. */
const names = (el: HTMLElement): string[] =>
  [el.getAttribute("aria-label"), el.getAttribute("title"), visibleText(el)].map((n) => (n ?? "").trim()).filter(Boolean);

/** Buttons a reader could use to change the tree (create, rename, move, delete, a node actions menu). */
const EDIT_CONTROL = /\b(new|create|add|rename|move|delete|remove|actions)\b/i;

// -- doc.view helpers ------------------------------------------------------------------------------------

const blocks = (m: Mounted, name: string) => m.container.ownerDocument.querySelectorAll(`[data-mesh-block="${name}"]`);
const editorBox = (m: Mounted) => m.container.querySelector<HTMLTextAreaElement>('[data-mesh-block="Editor"] textarea');

/** Words of an edit control a view could add on its own: Edit / Done, rename, move, delete. */
const EDIT_WORD = /^\s*(edit|done|rename|move|delete|remove)\b/i;

defineTests([
  // ---------------------------------------------------------------- doc.tree
  {
    id: "CT-doctree-01",
    title: "role tree / treeitem, aria-level by depth, aria-expanded only on nodes with children",
    slots: TREE,
    run({ mount }) {
      const m = mount();
      expect(m.container.querySelector("[role=tree]"), "the list needs role=tree").not.toBeNull();
      expect(items(m)).toHaveLength(3);
      const [root, kidA, kidB] = [item(m, "root"), item(m, "kid-a"), item(m, "kid-b")];
      expect([root, kidA, kidB].map((i) => i.getAttribute("aria-level"))).toEqual(["1", "2", "2"]);
      expect(root.getAttribute("aria-expanded")).toBe("true");
      expect(kidA.getAttribute("aria-expanded")).toBe("false");
      expect(kidB.hasAttribute("aria-expanded"), "a leaf has no aria-expanded").toBe(false);
    },
  },
  {
    id: "CT-doctree-02",
    title: "keyboard: Down / Up move focus, Right opens, Left closes or goes to the parent, Enter selects",
    slots: TREE,
    async run({ mount }) {
      const m = mount();
      const root = item(m, "root");
      root.focus();
      expect(focusIn(root), "a treeitem must be focusable").toBe(true);
      await m.user.keyboard("{ArrowDown}");
      expect(focusIn(item(m, "kid-a")), "ArrowDown goes to the next visible node").toBe(true);
      await m.user.keyboard("{ArrowDown}");
      expect(focusIn(item(m, "kid-b"))).toBe(true);
      await m.user.keyboard("{ArrowUp}");
      expect(focusIn(item(m, "kid-a")), "ArrowUp goes to the previous visible node").toBe(true);
      await m.user.keyboard("{ArrowRight}");
      expect(calls(m, "toggleExpanded").map((c) => c[0]), "ArrowRight opens a closed node").toEqual(["kid-a"]);
      item(m, "kid-b").focus();
      await m.user.keyboard("{ArrowLeft}");
      expect(focusIn(item(m, "root")), "ArrowLeft on a leaf goes to its parent").toBe(true);
      await m.user.keyboard("{ArrowLeft}");
      expect(calls(m, "toggleExpanded").map((c) => c[0]), "ArrowLeft closes an open node").toEqual(["kid-a", "root"]);
      item(m, "kid-a").focus();
      await m.user.keyboard("{Enter}");
      expect(calls(m, "select").map((c) => c[0]), "Enter selects the focused node").toEqual(["kid-a"]);
    },
  },
  {
    id: "CT-doctree-03",
    title: "click / Ctrl+click call select(id) / select(id, {newTab: true}); the node link has a real href",
    slots: TREE,
    run({ mount }) {
      const m = mount();
      const link = item(m, "kid-b").querySelector<HTMLAnchorElement>("a[href]");
      expect(link, "a node is a link").not.toBeNull();
      expect(link!.getAttribute("href")).toBe("/docs/kid-b");
      fireEvent.click(link!);
      const plain = calls(m, "select");
      expect(plain).toHaveLength(1);
      expect(plain[0]![0]).toBe("kid-b");
      expect((plain[0]![1] as { newTab?: boolean } | undefined)?.newTab ?? false).toBe(false);
      fireEvent.click(link!, { ctrlKey: true });
      const modified = calls(m, "select");
      expect(modified).toHaveLength(2);
      expect(modified[1]).toEqual(["kid-b", { newTab: true }]);
    },
  },
  {
    id: "CT-doctree-04",
    title: "a viewer gets no create / rename / move / delete controls",
    slots: TREE,
    async run({ mount }) {
      const m = mount({ role: "viewer" });
      const offending = Array.from(m.container.querySelectorAll<HTMLElement>("button,[role=button],[role=menuitem]"))
        .flatMap(names)
        .filter((t) => EDIT_CONTROL.test(t));
      expect(offending, "controls for actions the viewer does not have").toEqual([]);
      // What is still there must not break: opening and closing a branch is not an edit.
      for (const b of Array.from(m.container.querySelectorAll<HTMLElement>("button:not([disabled])"))) await m.user.click(b);
      expect(calls(m, "createChild").length + calls(m, "rename").length + calls(m, "move").length + calls(m, "remove").length).toBe(0);
    },
  },
  {
    id: "CT-doctree-05",
    title: "the selected node carries aria-selected and aria-current; the others do not",
    slots: TREE,
    run({ mount }) {
      const m = mount();
      const selected = item(m, "kid-b");
      expect(selected.getAttribute("aria-selected")).toBe("true");
      expect(selected.getAttribute("aria-current") ?? "false").not.toBe("false");
      for (const id of ["root", "kid-a"]) {
        const other = item(m, id);
        expect(other.getAttribute("aria-selected"), id).not.toBe("true");
        expect(other.getAttribute("aria-current") ?? "false", id).toBe("false");
      }
    },
  },
  {
    id: "CT-doctree-06",
    title: "a new document from realtime appears; expansion, selection and keyboard focus survive",
    slots: TREE,
    async run({ mount }) {
      const m = mount<DocTreeData>();
      const kidA = item(m, "kid-a");
      kidA.focus();
      await m.emitRealtime({ type: "document.created", data: { id: "fresh", title: "Doc fresh" }, at: "1" });
      expect(items(m)).toHaveLength(4);
      expect(item(m, "fresh")).toBeTruthy();
      expect(item(m, "root").getAttribute("aria-expanded")).toBe("true");
      expect(item(m, "kid-b").getAttribute("aria-selected")).toBe("true");
      expect(items(m).filter((i) => i.getAttribute("aria-selected") === "true")).toHaveLength(1);
      // The view must not rebuild the rows: the focused one stays focused.
      expect(focusIn(item(m, "kid-a")), "focus lost on a realtime update").toBe(true);
    },
  },
  {
    id: "CT-doctree-07",
    title: "nodes loading draws a placeholder, not a tree; error draws the message and retry calls retry()",
    slots: TREE,
    async run({ mount }) {
      const loading = mount({ options: { state: "loading" } });
      expect(loading.container.children.length).toBeGreaterThan(0);
      expect(loading.container.querySelector("[role=treeitem]")).toBeNull();
      loading.unmount();
      const error = mount({ options: { state: "error" } });
      expect(within(error.container).getByText(/Could not load the pages/)).toBeTruthy();
      const retry = button(error.container, /retry|повтор|try again/i);
      expect(retry, "the error state needs a retry button").not.toBeNull();
      await error.user.click(retry!);
      expect(calls(error, "retry")).toHaveLength(1);
    },
  },

  // ---------------------------------------------------------------- doc.view
  {
    id: "CT-docview-01",
    title: "ready: the Editor block exactly once, breadcrumbs and the title drawn",
    slots: VIEW,
    run({ mount }) {
      const m = mount();
      expect(blocks(m, "Editor"), "Editor exactly once").toHaveLength(1);
      expect(blocks(m, "Breadcrumbs").length, "the breadcrumbs block").toBeGreaterThan(0);
      const headings = Array.from(m.container.querySelectorAll("h1,h2,[role=heading]"));
      expect(headings.some((h) => h.textContent?.includes(fixtures.doc().title)), "the title is a heading").toBe(true);
    },
  },
  {
    id: "CT-docview-02",
    title: "save.state saving / saved / error is shown, each told apart; error shows its message",
    slots: VIEW,
    async run({ mount }) {
      const m = mount<DocViewFrame>();
      const shot = () => markup(m.container);
      const idle = shot();
      await m.update({ save: { state: "saving" } });
      const saving = shot();
      expect(visibleText(m.container), "saving").toMatch(/saving/i);
      await m.update({ save: { state: "saved" } });
      const saved = shot();
      expect(visibleText(m.container), "saved").toMatch(/saved/i);
      await m.update({ save: { state: "error", message: "disk full" } });
      const error = shot();
      expect(visibleText(m.container), "the error message").toContain("disk full");
      expect(new Set([idle, saving, saved, error]).size, "each save state draws differently").toBe(4);
    },
  },
  {
    id: "CT-docview-03",
    title: "conflict offers reload and keep-mine, each calling resolveConflict",
    slots: VIEW,
    async run({ mount }) {
      const m = mount<DocViewFrame>();
      const idle = markup(m.container);
      await m.update({ save: { state: "conflict" } });
      expect(markup(m.container), "the conflict is not silent").not.toBe(idle);
      const reload = button(m.container, /reload|перезагруз/i);
      expect(reload, "a reload choice").not.toBeNull();
      await m.user.click(reload!);
      expect(calls(m, "resolveConflict").slice(-1)[0]).toEqual(["reload"]);
      const mine = button(m.container, /keep\s*mine|оставить мо/i);
      expect(mine, "a keep-mine choice").not.toBeNull();
      await m.user.click(mine!);
      expect(calls(m, "resolveConflict").slice(-1)[0]).toEqual(["keep-mine"]);
    },
  },
  {
    id: "CT-docview-04",
    title: "viewer: the editor is still there (read mode is the block's); the view adds no edit controls",
    slots: VIEW,
    async run({ mount }) {
      const m = mount({ role: "viewer" });
      expect(blocks(m, "Editor")).toHaveLength(1);
      const doc = m.container.ownerDocument;
      const editControls = () =>
        Array.from(doc.querySelectorAll<HTMLElement>("button,[role=button],[role=menuitem]"))
          .filter((b) => !b.closest("[data-mesh-block]"))
          .flatMap(names)
          .filter((t) => EDIT_WORD.test(t));
      expect(editControls(), "edit controls on the page").toEqual([]);
      // Menus the view offers: open each and look inside.
      for (const trigger of Array.from(m.container.querySelectorAll<HTMLElement>("[aria-haspopup]"))) {
        if (trigger.closest("[data-mesh-block]")) continue;
        await m.user.click(trigger);
        await tick();
        expect(editControls(), "edit items in a menu").toEqual([]);
        await m.user.keyboard("{Escape}");
      }
    },
  },
  {
    id: "CT-docview-05",
    title: "the comments rail follows layout.commentsOpen; a comments control calls toggleComments",
    slots: VIEW,
    async run({ mount }) {
      const m = mount<DocViewFrame>();
      expect(blocks(m, "CommentsRail"), "closed: no rail").toHaveLength(0);
      const toggle =
        m.container.querySelector<HTMLElement>('[data-mesh-block="CommentsToggle"]') ?? button(m.container, /comment|коммент/i);
      expect(toggle, "a way to show the comments (the CommentsToggle block or a control of the view)").not.toBeNull();
      await m.user.click(toggle!);
      expect(calls(m, "toggleComments")).toHaveLength(1);
      await m.update({ layout: { commentsOpen: true } });
      expect(blocks(m, "CommentsRail"), "open: the rail").toHaveLength(1);
      await m.update({ layout: { commentsOpen: false } });
      expect(blocks(m, "CommentsRail"), "closed again").toHaveLength(0);
    },
  },
  {
    id: "CT-docview-06",
    title: "doc loading draws a placeholder without the editor; error draws the message and retry calls retry()",
    slots: VIEW,
    async run({ mount }) {
      const loading = mount({ options: { state: "loading" } });
      expect(loading.container.children.length).toBeGreaterThan(0);
      expect(blocks(loading, "Editor")).toHaveLength(0);
      expect(loading.container.querySelector("h1")).toBeNull();
      loading.unmount();
      const error = mount({ options: { state: "error" } });
      expect(within(error.container).getByText(/Could not open the document/)).toBeTruthy();
      expect(blocks(error, "Editor")).toHaveLength(0);
      const retry = button(error.container, /retry|повтор|try again/i);
      expect(retry, "the error state needs a retry button").not.toBeNull();
      await error.user.click(retry!);
      expect(calls(error, "retry")).toHaveLength(1);
    },
  },
  {
    id: "CT-docview-07",
    title: "an edit by someone else (realtime) updates the meta without remounting the editor or losing focus and text",
    slots: VIEW,
    async run({ mount }) {
      const m = mount<DocViewFrame>();
      const box = editorBox(m)!;
      const instance = box.parentElement!.getAttribute("data-instance");
      await m.user.click(box);
      await m.user.type(box, "half a sentence");
      const metaBefore = blocks(m, "Meta")[0]?.textContent;
      await m.emitRealtime({
        type: "document.updated",
        data: { id: "d1", version: 4, updatedAt: "2026-01-02T00:00:00Z", updatedBy: { id: "u2", type: "user", name: "Ann" } },
        at: "2",
      });
      await waitFor(() => expect(blocks(m, "Meta")[0]?.textContent).not.toBe(metaBefore));
      expect(blocks(m, "Meta")[0]?.textContent).toContain("Ann");
      const after = editorBox(m)!;
      expect(after.parentElement!.getAttribute("data-instance"), "the Editor was remounted").toBe(instance);
      expect(after.value).toBe("half a sentence");
      expect(document.activeElement, "focus left the editor").toBe(after);
    },
  },
]);
