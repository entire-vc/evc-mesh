// Class recipes shared by the rows of the mesh-default sidebar (items, dashboards).
export const RAIL_LINK = "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent";
export const RAIL_LINK_ACTIVE = "bg-sidebar-accent text-sidebar-primary";
export const ROW_LINK = "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent";
export const ROW_LINK_ACTIVE = "bg-sidebar-accent font-medium";
// Dashboard rows draw the same active look from `aria-current` itself: leaving a dashboard changes one attribute,
// not two (perf `board.open.dom_mutations` counts every record).
export const RAIL_LINK_CURRENT = "aria-[current=page]:bg-sidebar-accent aria-[current=page]:text-sidebar-primary";
export const ROW_LINK_CURRENT = "aria-[current=page]:bg-sidebar-accent aria-[current=page]:font-medium";

// Whole class strings per tone, so the attention badge stays byte-for-byte what it was.
export const ROW_BADGE: Record<"info" | "attention", string> = {
  attention: "ml-auto flex h-5 min-w-5 items-center justify-center rounded-full bg-tone-danger-500 px-1 text-[10px] font-bold text-white",
  info: "ml-auto flex h-5 min-w-5 items-center justify-center rounded-full bg-sidebar-primary px-1 text-[10px] font-bold text-primary-foreground",
};
export const RAIL_BADGE: Record<"info" | "attention", string> = {
  attention: "absolute -right-0.5 -top-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-tone-danger-500 text-[10px] font-bold text-white",
  info: "absolute -right-0.5 -top-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-sidebar-primary text-[10px] font-bold text-primary-foreground",
};

