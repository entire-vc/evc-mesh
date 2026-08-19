import type { Ctx } from "@milkdown/kit/ctx";
import {
  Editor,
  defaultValueCtx,
  editorViewOptionsCtx,
  remarkStringifyOptionsCtx,
  rootCtx,
} from "@milkdown/kit/core";
import { commonmark } from "@milkdown/kit/preset/commonmark";
import { gfm } from "@milkdown/kit/preset/gfm";
import { history } from "@milkdown/kit/plugin/history";
import { listener, listenerCtx } from "@milkdown/kit/plugin/listener";
import {
  attachmentAwareImageSchema,
  safeLinkSchema,
} from "@/lib/milkdown/schema-overrides";

/**
 * How a document body is written back out.
 *
 * `bullet: '-'` is the load-bearing one. remark-stringify defaults to `*`, so
 * without this every list in every document would be rewritten on its first
 * save — a diff against nothing, in files humans and Obsidian both write with
 * `-`. The rest are pinned for the same reason: whatever the default happens to
 * be today, the point is that it does not change under us.
 *
 * `rule` is deliberately absent. remark-stringify refuses `-` for both bullet
 * and thematic break (the output would be ambiguous), and the bullet is the one
 * that appears in nearly every document, so it wins. Thematic breaks serialise
 * as `***`.
 */
export const MESH_STRINGIFY_OPTIONS = {
  bullet: "-",
  emphasis: "*",
  strong: "*",
  fence: "`",
  fences: true,
  listItemIndent: "one",
  // A tight list must stay tight: remark's default already does this, but a
  // document full of blank-line-separated bullets is the most visible possible
  // churn, so it is pinned rather than inherited.
  tightDefinitions: true,
} as const;

export interface DocEditorConfig {
  /** Where the editor mounts. */
  root: HTMLElement;
  /** Markdown the editor opens with. */
  defaultValue: string;
  /** False turns the same engine into the viewer. */
  editable: boolean;
  /** Called with the serialised markdown after every document change. */
  onMarkdown?: (markdown: string) => void;
  /** Extra plugins — uploads, which only exist when a document owns the bytes. */
  plugins?: readonly unknown[];
  /**
   * Extra configuration, applied after the built-in config. Runs in Milkdown's
   * config phase, which is after every plugin has injected its slices, so it may
   * `ctx.update` a slice belonging to one of `plugins`.
   */
  configure?: (ctx: Ctx) => void;
  /** Class applied to the ProseMirror contenteditable. */
  editorClassName?: string;
}

/**
 * The one place a Docs editor is configured.
 *
 * The viewer and the editor are the same call with `editable` flipped, which is
 * the entire point of the unit: the two used to be separate hand-written
 * renderers, and they drifted. The round-trip test builds its editor from here
 * too, so what it proves is what ships.
 */
export function makeDocEditor(config: DocEditorConfig): Editor {
  const {
    root,
    defaultValue,
    editable,
    onMarkdown,
    plugins = [],
    configure,
    editorClassName,
  } = config;

  let editor = Editor.make()
    .config((ctx) => {
      ctx.set(rootCtx, root);
      ctx.set(defaultValueCtx, defaultValue);
      ctx.set(remarkStringifyOptionsCtx, { ...MESH_STRINGIFY_OPTIONS });
      ctx.update(editorViewOptionsCtx, (prev) => ({
        ...prev,
        editable: () => editable,
        attributes: {
          class: editorClassName ?? "",
          // The viewer is not a disabled input, and announcing it as one is
          // worse than announcing nothing.
          ...(editable ? {} : { "aria-readonly": "true" }),
        },
      }));
      if (onMarkdown) {
        ctx.get(listenerCtx).markdownUpdated((_ctx, markdown) => {
          onMarkdown(markdown);
        });
      }
      configure?.(ctx);
    })
    .use(commonmark)
    .use(gfm)
    // After the presets: extendSchema re-registers under the same id, so these
    // replace the preset's link and image rather than sitting alongside them.
    .use(safeLinkSchema)
    .use(attachmentAwareImageSchema)
    .use(listener)
    .use(history);

  for (const plugin of plugins) {
    editor = editor.use(plugin as Parameters<Editor["use"]>[0]);
  }

  return editor;
}
