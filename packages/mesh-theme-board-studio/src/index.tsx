import { Card } from "./card";
import { Column } from "./column";
import { DashLayout, FOCUS_WIDGET, FocusWidget } from "./dashboard";
import { Layout } from "./layout";
import "./theme.css";

// One entry per slot this theme replaces; `contract` is the slot contract major the view is written for.
export default {
  views: {
    "board.layout": { contract: "^1", view: Layout },
    "board.column": { contract: "^1", view: Column },
    "board.card": { contract: "^1", view: Card },
    "dashboard.layout": { contract: "^1", view: DashLayout },
    "dashboard.widget.focus": { contract: "^1", view: FocusWidget },
  },
  // The widgets this theme adds to the dashboard; the core lists them next to its own and draws them with the view above.
  widgets: [FOCUS_WIDGET],
};
