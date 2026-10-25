// Written out in full so Tailwind sees every class.
const AVATAR_CLASSES = [
  "bg-avatar-1",
  "bg-avatar-2",
  "bg-avatar-3",
  "bg-avatar-4",
  "bg-avatar-5",
  "bg-avatar-6",
  "bg-avatar-7",
  "bg-avatar-8",
] as const;

const AVATAR_TOKEN = /^avatar-([1-8])$/;

/** Agent badge background from the theme's avatar tokens, never a raw palette class. */
export function agentColorClass(token: string | undefined, name: string): string {
  const m = token ? AVATAR_TOKEN.exec(token) : null;
  if (m) return AVATAR_CLASSES[Number(m[1]) - 1]!;
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.codePointAt(0)!) >>> 0;
  return AVATAR_CLASSES[h % AVATAR_CLASSES.length]!;
}

export function agentInitials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const first = (s: string) => Array.from(s)[0] ?? "";
  if (parts.length === 0) return "?";
  const out = parts.length > 1 ? first(parts[0]!) + first(parts[1]!) : Array.from(parts[0]!).slice(0, 2).join("");
  return out.toUpperCase();
}
