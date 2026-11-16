// A descriptor file that pulls runtime code into the registry chunk: directly (a hook module) and through a data
// file that imports a hook (./schema). ./size is a data file with type-only imports, which is allowed.
// registry.test.ts must name the first two and not the third. Read as source only, never loaded.
import type { WidgetDescriptor } from "../../../../types";
import { useWidgetData } from "../../../../hooks";
import { SCHEMA } from "./schema";
import { SIZE } from "./size";

void useWidgetData;
void SCHEMA;
void SIZE;
export const descriptors: WidgetDescriptor[] = [];
