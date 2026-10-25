// The width the reader dragged the chat tree to, and whether it is folded.
// Not keyed per project, for the reason docs-layout-storage.ts gives.
const WIDTH_KEY = "mesh_chat_tree_width";
const COLLAPSED_KEY = "mesh_chat_tree_collapsed";

export const CHAT_TREE_DEFAULT_WIDTH = 288;
export const CHAT_TREE_MIN_WIDTH = 200;
export const CHAT_TREE_MAX_WIDTH = 480;

export function clampChatTreeWidth(width: number): number {
  if (!Number.isFinite(width)) return CHAT_TREE_DEFAULT_WIDTH;
  return Math.min(CHAT_TREE_MAX_WIDTH, Math.max(CHAT_TREE_MIN_WIDTH, Math.round(width)));
}

export function loadChatTreeWidth(): number {
  try {
    const raw = Number.parseInt(localStorage.getItem(WIDTH_KEY) ?? "", 10);
    return Number.isFinite(raw) ? clampChatTreeWidth(raw) : CHAT_TREE_DEFAULT_WIDTH;
  } catch {
    return CHAT_TREE_DEFAULT_WIDTH;
  }
}

export function saveChatTreeWidth(width: number): void {
  try {
    localStorage.setItem(WIDTH_KEY, String(clampChatTreeWidth(width)));
  } catch {
    // a column width is never worth breaking a page over
  }
}

export function loadChatTreeCollapsed(): boolean {
  try {
    return localStorage.getItem(COLLAPSED_KEY) === "1";
  } catch {
    return false;
  }
}

export function saveChatTreeCollapsed(collapsed: boolean): void {
  try {
    localStorage.setItem(COLLAPSED_KEY, collapsed ? "1" : "0");
  } catch {
    // see above
  }
}
