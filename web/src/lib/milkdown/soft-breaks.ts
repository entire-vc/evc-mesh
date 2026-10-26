/**
 * remark plugin: every newline inside a paragraph's text becomes a hard break.
 *
 * In markdown a single newline is a soft break and renders as a space, so
 * "line one\nline two" came out on one row. A chat composer is not a markdown
 * editor: Shift+Enter means "new line", and the bubble has to show it.
 *
 * It works on the parsed tree, not on the source string, so code is safe by
 * construction: fenced, indented and inline code are `code`/`inlineCode` nodes,
 * never `text`, and keep their newlines exactly as markdown treats them.
 */

interface Node {
  type: string;
  value?: string;
  children?: Node[];
}

function split(parent: Node): void {
  const children = parent.children;
  if (!children) return;
  parent.children = children.flatMap((node) => {
    if (node.type !== "text" || !node.value?.includes("\n")) {
      split(node);
      return [node];
    }
    return node.value.split("\n").flatMap((value, i) => {
      const text = value ? [{ type: "text", value }] : [];
      return i === 0 ? text : [{ type: "break" }, ...text];
    });
  });
}

export function remarkSoftBreaks() {
  return (tree: Node) => split(tree);
}
