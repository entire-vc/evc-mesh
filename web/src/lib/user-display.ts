/**
 * How a person is labelled in the UI.
 *
 * One account can belong to several workspaces, so there is exactly one name
 * per person and it is the thing to show. The address is a disambiguator for
 * two people with the same name, not an identity — printing it where a name
 * belongs is both worse to read and a wider disclosure of personal data than
 * the screen needs.
 *
 * The awkward case is an account whose display name was never chosen. Every
 * path that provisioned a user without asking stored the address in
 * display_name, so `name` and `email` come back identical and a name-over-email
 * layout renders the same address twice. `isNamePlaceholder` detects that so
 * callers can show it once and offer to fix it.
 */

interface NamedUser {
  name?: string | null;
  email?: string | null;
  username?: string | null;
}

/** True when this account has no display name beyond its own address. */
export function isNamePlaceholder(user: NamedUser | null | undefined): boolean {
  if (!user) return true;
  const name = (user.name ?? "").trim();
  const email = (user.email ?? "").trim();
  if (!name) return true;
  return name.toLowerCase() === email.toLowerCase();
}

/**
 * The primary label for a person: their name, or their address when there is
 * no name to show. Never empty as long as one of the two is set.
 */
export function displayName(user: NamedUser | null | undefined): string {
  if (!user) return "Unknown";
  const name = (user.name ?? "").trim();
  if (name && !isNamePlaceholder(user)) return name;
  const email = (user.email ?? "").trim();
  return email || name || "Unknown";
}

/**
 * The secondary label shown under the name — the thing that tells two people
 * with the same name apart. Returns null when there is nothing to add, which
 * is the case for a placeholder name (the address is already the primary
 * label, and repeating it says nothing).
 */
export function secondaryLabel(user: NamedUser | null | undefined): string | null {
  if (!user || isNamePlaceholder(user)) return null;
  return (user.email ?? "").trim() || null;
}

/**
 * A one-line label for dense contexts (select options, badges) where there is
 * no room for two lines. Adds @username only when it exists, so the option text
 * stays a name rather than becoming an address.
 */
export function inlineLabel(user: NamedUser | null | undefined): string {
  const primary = displayName(user);
  const username = (user?.username ?? "").trim();
  if (username && !isNamePlaceholder(user)) return `${primary} (@${username})`;
  return primary;
}
