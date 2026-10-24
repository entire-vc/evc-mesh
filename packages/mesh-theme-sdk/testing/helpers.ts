import { act, fireEvent } from "@testing-library/react";
import type { Mounted } from "./types";

export const tick = () => act(async () => { await new Promise((r) => setTimeout(r, 30)); });

/** How many times an action was called; an action the role has no right to does not exist (0 calls, not an error). */
export const calls = (m: Pick<Mounted, "spies">, name: string): unknown[][] => m.spies[name]?.mock.calls ?? [];

/** The text a user can read or hear from an element: text, accessible names and titles. */
export function visibleText(root: Element): string {
  const parts = [root.textContent ?? ""];
  root.querySelectorAll("[aria-label],[title],img[alt]").forEach((e) => {
    parts.push(e.getAttribute("aria-label") ?? "", e.getAttribute("title") ?? "", e.getAttribute("alt") ?? "");
  });
  return parts.join(" ");
}

/** Normalised markup: what two renderings must differ in to be told apart by a user. */
export const markup = (el: Element) => el.outerHTML.replace(/\s(id|for|aria-describedby|aria-labelledby)="[^"]*"/g, "");

/** First enabled button whose accessible name matches. */
export function button(root: ParentNode, name: RegExp): HTMLElement | null {
  return (
    (Array.from(root.querySelectorAll<HTMLElement>("button,[role=button]")).find((b) => name.test(visibleText(b))) as HTMLElement | undefined) ?? null
  );
}

// -- drag and drop (jsdom): the board mounter puts columns on a grid, 300px apart ------------------------

export async function pointerDrag(el: Element, dx: number, dy = 0) {
  const x = 10, y = 30;
  fireEvent.pointerDown(el, { button: 0, isPrimary: true, pointerId: 1, clientX: x, clientY: y });
  for (let i = 1; i <= 8; i++) fireEvent.pointerMove(document, { isPrimary: true, pointerId: 1, clientX: x + (dx / 8) * i, clientY: y + (dy / 8) * i });
  await tick();
  fireEvent.pointerUp(document, { isPrimary: true, pointerId: 1, clientX: x + dx, clientY: y + dy });
  await tick();
  // dnd-kit keeps a click-swallowing listener on the document for 50 ms after a pointer drag (a browser's click
  // after pointerup must not open the card). Wait it out, or it eats the first click of whatever runs next.
  await act(async () => { await new Promise((r) => setTimeout(r, 70)); });
}

/** Pick up with Space, move one zone with an arrow, drop with Space (or cancel with Escape). */
export async function keyboardMove(el: HTMLElement, arrow: "ArrowRight" | "ArrowDown", end: "Space" | "Escape" = "Space") {
  el.focus();
  fireEvent.keyDown(el, { code: "Space", key: " " });
  await tick();
  fireEvent.keyDown(document, { code: arrow, key: arrow });
  await tick();
  if (end === "Space") fireEvent.keyDown(document, { code: "Space", key: " " });
  else fireEvent.keyDown(document, { code: "Escape", key: "Escape" });
  await tick();
}
