// The icon value an entity stores: "" (default), one emoji grapheme, or `icon:<name>`.
// Anything else is a legacy text icon and is drawn as it is. The server validates new values with
// internal/domain/icon.go (same rules).
export const ICON_NAME = /^[a-z0-9-]{1,40}$/;

export type IconValue =
  | { kind: "none" }
  | { kind: "emoji"; emoji: string }
  | { kind: "icon"; name: string }
  | { kind: "text"; text: string };

/** True when value starts like an emoji (pictograph, flag or keycap). */
export function isEmojiValue(value: string): boolean {
  return /^(?:\p{Extended_Pictographic}|\p{Regional_Indicator}|[0-9#*]️?⃣)/u.test(value);
}

export function parseIconValue(value: string | null | undefined): IconValue {
  const v = value ?? "";
  if (!v.trim()) return { kind: "none" };
  if (v.startsWith("icon:")) {
    const name = v.slice(5);
    if (ICON_NAME.test(name)) return { kind: "icon", name };
    return { kind: "text", text: v };
  }
  return isEmojiValue(v) ? { kind: "emoji", emoji: v } : { kind: "text", text: v };
}

export function iconValue(name: string): string {
  return `icon:${name}`;
}
