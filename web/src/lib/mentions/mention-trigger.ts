/**
 * Where an `@` menu is open, and what typing it produces.
 *
 * Kept apart from the hook so both are testable without React, and because the
 * same rules have to hold in two places: the picker decides what to offer, and
 * the renderer decides what to highlight. When those disagree, you get a slug
 * that was offered by the menu and then rendered as plain text.
 */

/** An in-progress `@…` at the caret. */
export interface MentionTrigger {
  /** Index of the `@` itself. */
  start: number;
  /** What has been typed after it, possibly empty. */
  query: string;
}

/**
 * Finds the `@…` the caret is currently inside, if any.
 *
 * `[^\s@]*` rather than the slug character class on purpose: the menu has to
 * stay open while somebody is halfway through typing a name that is not yet a
 * legal slug, and it has to close on whitespace or a second `@`.
 *
 * The character before the `@` must be a boundary, which is what keeps an email
 * address from opening a menu — `bob@example.com` has `b` in front of the `@`,
 * and the server's extraction refuses it for exactly the same reason. Matching
 * here and refusing there would offer a menu whose result is then rejected.
 */
export function findMentionTrigger(
  value: string,
  caret: number,
): MentionTrigger | null {
  const before = value.slice(0, caret);
  const match = before.match(/(^|[\s([{])@([^\s@]*)$/);
  if (!match) return null;
  const query = match[2] ?? "";
  return { start: caret - query.length - 1, query };
}

/**
 * Replaces the trigger with `@slug ` and reports where the caret lands.
 *
 * Returns the caret rather than moving it: a hook can restore selection after
 * React has re-rendered, and a function that reached into the DOM could not be
 * tested without one.
 */
export function applyMentionInsertion(
  value: string,
  trigger: MentionTrigger,
  caret: number,
  slug: string,
): { value: string; caret: number } {
  const before = value.slice(0, trigger.start);
  const after = value.slice(caret);
  const inserted = `@${slug} `;
  return {
    value: `${before}${inserted}${after}`,
    caret: trigger.start + inserted.length,
  };
}
