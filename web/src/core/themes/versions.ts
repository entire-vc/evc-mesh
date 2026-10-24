// Contract versions of the slots this core implements (spec «Каталог слотов
// тем v1» §1) and the range check a theme's declaration goes through (ADR D6).
// Source of truth is slot-contracts.json in the SDK (T6); until it lands this
// map is the core's copy.

export const CORE_SLOT_VERSIONS: Readonly<Record<string, string>> = {
  "board.layout": "1.0.0",
  "board.column": "1.0.0",
  "board.card": "1.0.0",
  "task.row": "1.0.0",
  "task.view": "1.0.0",
  "dashboard.layout": "1.0.0",
  "nav.sidebar": "1.0.0",
  "nav.topbar": "1.0.0",
  "doc.tree": "1.0.0",
  "doc.view": "1.0.0",
  "auth.screen": "1.0.0",
};

/** `dashboard.widget.<id>` slots share one contract version. */
export function coreSlotVersion(slot: string): string | null {
  if (slot.startsWith("dashboard.widget.")) return "1.0.0";
  return CORE_SLOT_VERSIONS[slot] ?? null;
}

const major = (v: string): number | null => {
  const m = /^(\d+)(?:\.\d+){0,2}$/.exec(v.trim());
  return m ? Number(m[1]) : null;
};

/**
 * Whether the core's `version` satisfies a theme's `range`. Themes declare a
 * major only (`^1`, the schema accepts nothing else); `^N.M.P` is tolerated
 * and compared by major too. Anything unparseable does not satisfy.
 * The core supports its current major and the previous one (ADR D6): a core
 * 2.x still satisfies `^1` only for slots listed in LEGACY_MAJORS.
 */
export function satisfies(range: string, version: string, legacy: readonly number[] = []): boolean {
  const m = /^\^(\d+(?:\.\d+){0,2})$/.exec(range.trim());
  if (!m) return false;
  const want = major(m[1] ?? "");
  const have = major(version);
  if (want === null || have === null) return false;
  return want === have || legacy.includes(want);
}
