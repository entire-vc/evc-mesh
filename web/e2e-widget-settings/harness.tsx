// Backend-free harness for the dashboard "Widget settings" dialog: the real SettingsDialog with the
// Task list form the editor opens (catalog entry -> client descriptor). Save writes the payload to
// <output data-testid="saved"> so the spec can see that the click reached the form.
import { createRoot } from "react-dom/client";
import { useState } from "react";
import { fromWireCatalog } from "../src/core/dashboard/edit/api";
import { SettingsDialog } from "../src/core/dashboard/edit/settings-dialog";
import type { LayoutItem } from "../src/core/dashboard/edit/types";
import "@/i18n";
import "../src/index.css";

const type = new URLSearchParams(location.search).get("type") ?? "task-list";
const size = { default: { w: 6, h: 4 }, min: { w: 3, h: 2 }, max: { w: 12, h: 10 } };
const catalog = fromWireCatalog({ id: type, origin: "core", category: "tasks", size, settings: { type: "object", properties: {} } });
const widget: LayoutItem = { id: "w1", type, x: 0, y: 0, w: 6, h: 4, settings: {} };

function Page() {
  const [open, setOpen] = useState(true);
  const [saved, setSaved] = useState("");
  return (
    <div className="p-4">
      <button type="button" onClick={() => setOpen(true)}>Open settings</button>
      <output data-testid="saved" className="block text-xs">{saved}</output>
      <SettingsDialog item={open ? widget : null} type={catalog} onClose={() => setOpen(false)} onSave={(p) => setSaved(JSON.stringify(p))} />
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<Page />);
