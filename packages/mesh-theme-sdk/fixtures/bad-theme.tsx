// Red control: a theme that imports a slot type and a primitive the SDK does not have. Must NOT typecheck.
import { Draggable, NoSuchPrimitive } from "../src";
import type { NoSuchSlotView } from "../src";

export const View: NoSuchSlotView = () => <Draggable item={{ kind: "task", id: "x" }}>{NoSuchPrimitive}</Draggable>;
