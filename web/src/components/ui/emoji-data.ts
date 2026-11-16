// Emoji catalogue for EmojiPicker. Lives in the lazy picker chunk only.
// Search names live in emoji-names/{en,ru}.json, keyed by emoji (data, not UI copy:
// both languages are always searched, whatever the interface language).
import enJson from "./emoji-names/en.json";
import ruJson from "./emoji-names/ru.json";

const en: Record<string, string> = enJson;
const ru: Record<string, string> = ruJson;

export type EmojiCategoryId =
  | "smileys"
  | "people"
  | "nature"
  | "food"
  | "activity"
  | "travel"
  | "objects"
  | "symbols";

export interface EmojiEntry {
  e: string;
  /** lowercase searchable text, both languages */
  k: string;
}

export const EMOJI_CATEGORY_ORDER: EmojiCategoryId[] = [
  "smileys",
  "people",
  "nature",
  "food",
  "activity",
  "travel",
  "objects",
  "symbols",
];

const LISTS: Record<EmojiCategoryId, string[]> = {
  smileys: ["😀", "😃", "😄", "😁", "😆", "😅", "😂", "🤣", "🙂", "😉", "😊", "😇", "🥰", "😍", "🤩", "😘", "😋", "😛", "😜", "🤪", "🤗", "🤔", "🤨", "😐", "😶", "🙄", "😏", "😴", "🤤", "😎", "🤓", "🧐", "😕", "😟", "😢", "😭", "😱", "😡", "🤬", "🤯", "😳", "🥳", "🥺", "🤖", "👻", "💀", "👽", "😈", "💩", "🙈"],
  people: ["👋", "👍", "👎", "👏", "🙌", "🙏", "🤝", "💪", "✌️", "🤞", "👌", "👈", "👉", "👆", "👇", "✍️", "🧠", "👀", "👁️", "🗣️", "👤", "👥", "🧑‍💻", "🧑‍🎨", "🧑‍🔬", "🧑‍🚀", "🦸", "🧙", "🧑‍🏫", "🧑‍⚕️", "👨‍👩‍👧", "🏃"],
  nature: ["🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼", "🐨", "🐯", "🦁", "🐮", "🐷", "🐸", "🐵", "🐔", "🐧", "🐦", "🦅", "🦉", "🦄", "🐝", "🦋", "🐛", "🐞", "🐢", "🐍", "🐙", "🐬", "🐳", "🦈", "🐘", "🦒", "🐉", "🌵", "🌲", "🌳", "🌴", "🌱", "🌿", "🍀", "🍁", "🌸", "🌹", "🌻", "🌍", "🌙", "☀️", "⭐", "🌟", "✨", "⚡", "🔥", "💧", "🌊", "❄️", "☁️", "🌈", "⛰️"],
  food: ["🍎", "🍊", "🍋", "🍌", "🍉", "🍇", "🍓", "🍒", "🍑", "🥑", "🍅", "🥕", "🌽", "🥦", "🍞", "🧀", "🍳", "🥞", "🍔", "🍟", "🍕", "🌭", "🌮", "🍣", "🍜", "🍝", "🍰", "🎂", "🍩", "🍪", "🍫", "🍿", "☕", "🍵", "🍺", "🍷", "🍹", "🥤"],
  activity: ["⚽", "🏀", "🏈", "⚾", "🎾", "🏐", "🏓", "🥊", "🎯", "🎳", "⛳", "🏆", "🥇", "🏅", "🎮", "🕹️", "🎲", "♟️", "🧩", "🎨", "🎭", "🎬", "🎤", "🎧", "🎵", "🎹", "🎸", "🥁", "🎉", "🎊", "🎁", "🎈", "🎄", "🎃"],
  travel: ["🚀", "🛸", "✈️", "🚁", "🚂", "🚇", "🚌", "🚗", "🚕", "🏎️", "🚚", "🚲", "🛵", "🛴", "⛵", "🚢", "⚓", "🚦", "🚧", "🏠", "🏢", "🏭", "🏗️", "🏰", "🏛️", "⛺", "🗺️", "🧭", "🏝️", "🌋", "🗼", "🗽", "🌃", "🌅"],
  objects: ["💡", "🔦", "💻", "🖥️", "⌨️", "🖱️", "📱", "☎️", "📷", "📹", "📺", "📻", "💾", "💿", "🔋", "🔌", "📡", "🔭", "🔬", "⚙️", "🔧", "🔨", "🛠️", "⛏️", "🧰", "🧪", "🧬", "💊", "💉", "🔒", "🔓", "🔑", "🛡️", "🗝️", "📦", "📫", "✉️", "📧", "📝", "📄", "📃", "📑", "📊", "📈", "📉", "📋", "📌", "📍", "📎", "✂️", "🗂️", "📁", "📂", "🗃️", "🗄️", "🗑️", "📚", "📖", "📓", "🔖", "🏷️", "💰", "💵", "💳", "💎", "🧾", "⚖️", "⏰", "⏳", "⌛", "📅", "🕐", "🔔", "📢", "💬", "🧲", "🔗", "🔍", "🧹", "🧯", "🛒", "🪄"],
  symbols: ["❤️", "🧡", "💛", "💚", "💙", "💜", "🖤", "🤍", "💔", "💯", "✅", "☑️", "❌", "❗", "❓", "⚠️", "🚫", "🔴", "🟠", "🟡", "🟢", "🔵", "🟣", "⚫", "⚪", "🟥", "🟩", "🟦", "♻️", "🔄", "🔁", "➕", "➖", "➡️", "⬆️", "🔝", "🆕", "🆗", "🔰", "🏁", "🚩", "🏳️", "🔱", "🌐", "#️⃣", "🔣", "🔤", "🔠", "🔢", "🧿"],
};

const norm = (x: string) => x.toLowerCase().replace(/\u0451/g, "\u0435");

function build(c: EmojiCategoryId): EmojiEntry[] {
  return LISTS[c].map((e) => ({ e, k: norm(`${en[e] ?? ""} ${ru[e] ?? ""}`) }));
}

export const EMOJI_BY_CATEGORY: Record<EmojiCategoryId, EmojiEntry[]> = Object.fromEntries(
  EMOJI_CATEGORY_ORDER.map((c) => [c, build(c)]),
) as Record<EmojiCategoryId, EmojiEntry[]>;

export function searchEmoji(query: string): EmojiEntry[] {
  const q = norm(query.trim());
  if (!q) return [];
  const out: EmojiEntry[] = [];
  for (const c of EMOJI_CATEGORY_ORDER) {
    for (const entry of EMOJI_BY_CATEGORY[c]) {
      if (entry.k.includes(q)) out.push(entry);
    }
  }
  return out;
}
