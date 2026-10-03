/** Display only: names, slugs and assignment values remain independent. */
export function agentLabel(name: string, shortTag?: string | null): string {
  return shortTag ? `${name} · ${shortTag}` : name;
}

export function agentOptionLabel(name: string, shortTag: string | null | undefined, description: string): string {
  // Preserve the existing untagged option exactly; tagged options follow the
  // approved plain-text name/tag contract without repeating the long role.
  return shortTag ? agentLabel(name, shortTag) : `${name} (agent)${description ? ` — ${description}` : ""}`;
}
