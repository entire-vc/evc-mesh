// Stand-alone page for the table affordances, served by `vite` only (not part of the build):
// the Docs editor, the same document read-only, and the task description editor, each with a 20×10 table.
import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router";
import i18n from "@/i18n";
import "@/index.css";
import { DocEditor } from "@/components/doc-editor";
import { RichTextEditor } from "@/components/rich-text-editor";

const params = new URLSearchParams(location.search);
if (params.get("lang")) void i18n.changeLanguage(params.get("lang")!);
const COLS = 10;
const ROWS = Number(params.get("rows") ?? 20);
const header = `| ${Array.from({ length: COLS }, (_, c) => `Col ${c + 1}`).join(" | ")} |`;
const delim = `| ${Array.from({ length: COLS }, () => "-").join(" | ")} |`;
const body = Array.from({ length: ROWS - 1 }, (_, r) => `| ${Array.from({ length: COLS }, (_, c) => `r${r + 1}c${c + 1}`).join(" | ")} |`);
const TABLE = [header, delim, ...body].join("\n") + "\n";
const DOC = `# Таблица\n\nТекст перед таблицей.\n\n${TABLE}\nТекст после таблицы.`;

function Page() {
  const [doc, setDoc] = useState(DOC);
  const [desc, setDesc] = useState(`Описание задачи.\n\n| A | B | C |\n| - | - | - |\n| a1 | b1 | c1 |\n| a2 | b2 | c2 |\n`);
  (window as unknown as { __md: Record<string, string> }).__md = { doc, desc };
  return (
    <MemoryRouter>
      <main className="mx-auto flex max-w-5xl flex-col gap-6 p-4">
        <section data-testid="doc-edit"><h2 className="mb-2 text-sm font-semibold">Документ — редактирование</h2><DocEditor value={doc} onChange={setDoc} documentId="fixture" /></section>
        <section data-testid="task-edit"><h2 className="mb-2 text-sm font-semibold">Описание задачи — редактирование</h2><RichTextEditor value={desc} onChange={setDesc} /></section>
        <section data-testid="doc-view"><h2 className="mb-2 text-sm font-semibold">Документ — просмотр</h2><DocEditor value={doc} onChange={() => {}} readOnly /></section>
      </main>
    </MemoryRouter>
  );
}
createRoot(document.getElementById("root")!).render(<StrictMode><Page /></StrictMode>);
