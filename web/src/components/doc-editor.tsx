import { MarkdownEditor } from "@/components/markdown-editor";
import { MarkdownRenderer } from "@/components/markdown-renderer";

export interface DocEditorProps {
  /** Markdown source of the document. */
  value: string;
  /** Called on every keystroke while editing. Never called when readOnly. */
  onChange: (value: string) => void;
  /** Render the document instead of editing it. Default false. */
  readOnly?: boolean;
}

/**
 * DocEditor — the single seam between the Docs page and whatever renders or
 * edits a document body.
 *
 * The interface is deliberately three props wide. The real WYSIWYG editor
 * (Milkdown) is a separate unit behind a spike that has not run: when it lands
 * it replaces the innards of this file, and the tree, the routing and the save
 * path do not change, because none of them ever sees a textarea or a renderer.
 * Nothing outside this file may import markdown-editor or markdown-renderer for
 * document bodies.
 */
export function DocEditor({ value, onChange, readOnly = false }: DocEditorProps) {
  if (readOnly) {
    if (!value.trim()) {
      return (
        <p className="text-sm text-muted-foreground">This page is empty.</p>
      );
    }
    return <MarkdownRenderer content={value} />;
  }

  return (
    <MarkdownEditor
      value={value}
      onChange={onChange}
      rows={24}
      placeholder="Write your page... (Markdown supported)"
      hint="Markdown supported"
      // Uploads go through the task artifact API, which a document has no task
      // for. Offering the buttons anyway would insert placeholders that never
      // resolve into anything.
      attachments={false}
    />
  );
}
