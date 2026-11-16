import type { Ctx } from "@milkdown/kit/ctx";
import { headingKeymap } from "@milkdown/kit/preset/commonmark";
import type { Command } from "@milkdown/kit/prose/state";
import { TextSelection } from "@milkdown/kit/prose/state";
import type { Node as ProseNode, ResolvedPos } from "@milkdown/kit/prose/model";
import { $useKeymap } from "@milkdown/kit/utils";

/**
 * Backspace and Delete at the edge of a block.
 *
 * Out of the box, two things made these keys wreck formatting:
 *
 * 1. The commonmark preset binds BOTH keys at the start of a heading to
 *    "DowngradeHeading", which lowers the level number: Backspace at the start
 *    of an H2 produced an H1 — a bigger heading, the opposite of what was
 *    asked — and Delete there did the same instead of deleting a character.
 * 2. Milkdown's Backspace joins into the previous TEXTBLOCK, and its Delete
 *    joins the next one in. An image on its own line is an image inside a
 *    paragraph, so Backspace at the start of the paragraph under it pulled the
 *    text up next to the image, and Delete at the end of a paragraph glued a
 *    heading's text onto it as plain text.
 *
 * The rules here, one key press each:
 * - Backspace at the start of a heading: an empty block above is removed;
 *   otherwise the heading becomes a paragraph. The text stays where it is, and
 *   a second Backspace joins it with the block above as usual.
 * - Delete at the end of a block before a heading: an empty block is removed;
 *   otherwise the heading becomes a paragraph, again keeping its text and its
 *   line. A second Delete joins.
 * - Backspace at the start of a block under an image line, or Delete at the
 *   end of a block above one, removes that image line whole. Its neighbours
 *   keep their own lines.
 *
 * Anything else falls through to the default commands.
 */

/** A paragraph that holds nothing but images: an image on its own line. */
export function isImageLine(node: ProseNode | null | undefined): boolean {
  if (!node || node.type.name !== "paragraph" || node.childCount === 0) {
    return false;
  }
  let onlyImages = true;
  node.forEach((child) => {
    if (child.type.name !== "image") onlyImages = false;
  });
  return onlyImages;
}

function isHeading(node: ProseNode | null | undefined): boolean {
  return node?.type.name === "heading";
}

/** The cursor, when the selection is one and sits in a textblock. */
function cursor(state: Parameters<Command>[0]): ResolvedPos | null {
  const { selection } = state;
  if (!selection.empty || !(selection instanceof TextSelection)) return null;
  const $pos = selection.$from;
  return $pos.parent.isTextblock ? $pos : null;
}

/** The sibling block before/after the cursor's textblock, with its position. */
function sibling($pos: ResolvedPos, dir: -1 | 1): { node: ProseNode; pos: number } | null {
  const depth = $pos.depth - 1;
  const container = $pos.node(depth);
  const index = $pos.index(depth) + dir;
  if (index < 0 || index >= container.childCount) return null;
  const node = container.child(index);
  const pos = dir < 0 ? $pos.before() - node.nodeSize : $pos.after();
  return { node, pos };
}

export const backspaceAtBlockStart: Command = (state, dispatch) => {
  const $pos = cursor(state);
  if (!$pos || $pos.parentOffset !== 0) return false;
  const before = sibling($pos, -1);

  if (isHeading($pos.parent)) {
    if (before && before.node.isTextblock && before.node.content.size === 0) {
      dispatch?.(
        state.tr.delete(before.pos, before.pos + before.node.nodeSize).scrollIntoView(),
      );
      return true;
    }
    const paragraph = state.schema.nodes.paragraph;
    if (!paragraph) return false;
    dispatch?.(
      state.tr.setBlockType($pos.pos, $pos.pos, paragraph).scrollIntoView(),
    );
    return true;
  }

  if (before && isImageLine(before.node)) {
    dispatch?.(
      state.tr.delete(before.pos, before.pos + before.node.nodeSize).scrollIntoView(),
    );
    return true;
  }

  return false;
};

export const deleteAtBlockEnd: Command = (state, dispatch) => {
  const $pos = cursor(state);
  if (!$pos || $pos.parentOffset !== $pos.parent.content.size) return false;
  const after = sibling($pos, 1);
  if (!after) return false;

  if (isImageLine(after.node)) {
    dispatch?.(
      state.tr.delete(after.pos, after.pos + after.node.nodeSize).scrollIntoView(),
    );
    return true;
  }

  if (isHeading(after.node)) {
    if ($pos.parent.content.size === 0) {
      const from = $pos.before();
      const tr = state.tr.delete(from, $pos.after());
      // The heading now starts where the empty block did: put the cursor at
      // the start of its text, where the user was looking.
      tr.setSelection(TextSelection.create(tr.doc, from + 1));
      dispatch?.(tr.scrollIntoView());
      return true;
    }
    const paragraph = state.schema.nodes.paragraph;
    if (!paragraph) return false;
    const inside = after.pos + 1;
    dispatch?.(state.tr.setBlockType(inside, inside, paragraph).scrollIntoView());
    return true;
  }

  return false;
};

/**
 * The keymap. Priority above the preset's (50), so these run first and the
 * preset's own handling still applies wherever they decline.
 */
export const blockBoundaryKeymap = $useKeymap("meshBlockBoundary", {
  BackspaceAtBlockStart: {
    shortcuts: "Backspace",
    priority: 100,
    command: () => backspaceAtBlockStart,
  },
  DeleteAtBlockEnd: {
    shortcuts: "Delete",
    priority: 100,
    command: () => deleteAtBlockEnd,
  },
});

/**
 * Unbinds the preset's DowngradeHeading (Backspace/Delete at the start of a
 * heading → level - 1). Backspace there is handled above; Delete at the start
 * of a heading should delete the next character, which is what it does once
 * this binding is gone. Must run in the config phase, after the preset has
 * injected its keymap slice.
 */
export function unbindDowngradeHeading(ctx: Ctx): void {
  ctx.update(headingKeymap.key, (prev) => ({
    ...prev,
    DowngradeHeading: { ...prev.DowngradeHeading, shortcuts: [] },
  }));
}
