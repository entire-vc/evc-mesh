import { MarkdownEditor } from "@/components/markdown-editor";
import { MarkdownRenderer } from "@/components/markdown-renderer";

export interface DocEditorProps {
  /** Markdown source of the document. */
  value: string;
  /** Called on every keystroke while editing. Never called when readOnly. */
  onChange: (value: string) => void;
  /** Render the document instead of editing it. Default false. */
  readOnly?: boolean;
  /**
   * The document being edited. Uploads become attachments owned by it; without
   * it the upload affordances are dropped rather than left inserting
   * placeholders nothing will replace.
   */
  documentId?: string;
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
export function DocEditor({ value, onChange, readOnly = false, documentId }: DocEditorProps) {
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
      hint="Markdown supported — paste or drop an image to attach it"
      documentId={documentId}
      // Uploads used to be dropped here because they rode on the task artifact
      // API and a document has no task. Documents now own their attachments, so
      // the affordances are real — but only once the document exists, which is
      // why this still turns off when there is no id to hang the bytes on.
      attachments={!!documentId}
    />
  );
}
