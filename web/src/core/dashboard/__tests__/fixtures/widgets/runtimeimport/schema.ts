// Not a pure data file: it imports a hook module. Read as source only, never loaded.
import { useWidgetData } from "../../../../hooks";

export const SCHEMA = { type: "object", hook: useWidgetData } as const;
