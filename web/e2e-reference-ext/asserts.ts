/** Pure assert helpers, so the red controls can feed them broken input without a browser. */

export const MESH_ORIGIN = "https://mesh.entire.host";

export function assertCsp(csp: string | undefined, xfo: string | undefined): void {
  if (!csp) throw new Error("Content-Security-Policy header is missing");
  if (csp.trim() !== `frame-ancestors ${MESH_ORIGIN}`) {
    throw new Error(`CSP must be exactly "frame-ancestors ${MESH_ORIGIN}", got a different value (len ${csp.length})`);
  }
  if (xfo) throw new Error("X-Frame-Options must not be set");
}

export function assertNoHScroll(m: { scrollWidth: number; clientWidth: number }, where: string): void {
  if (m.scrollWidth !== m.clientWidth) throw new Error(`${where}: scrollWidth ${m.scrollWidth} must equal clientWidth ${m.clientWidth}`);
}

export function assertTabs(names: string[], expected: string[], absent: string[]): void {
  for (const n of expected) if (!names.includes(n)) throw new Error(`tab "${n}" missing, have ${JSON.stringify(names)}`);
  for (const n of absent) if (names.includes(n)) throw new Error(`tab "${n}" must not be present`);
}

export function assertFrameText(text: string, needles: string[]): void {
  for (const n of needles) if (!text.includes(n)) throw new Error(`iframe text lacks "${n}"`);
  if (text.includes("unavailable right now")) throw new Error("iframe shows the unavailable line");
}

export function assertExactTabs(names: string[], expected: string[]): void {
  const got = JSON.stringify(names);
  if (got !== JSON.stringify(expected)) throw new Error(`visible tab set must be exactly ${JSON.stringify(expected)}, got ${got}`);
}

/** A push adds exactly one history entry, a replace adds none. */
export function assertHistoryDelta(before: number, after: number, expected: number, what: string): void {
  if (after - before !== expected) throw new Error(`${what}: history.length changed by ${after - before}, expected ${expected}`);
}

/** The frame is the same document (not remounted, not reloaded) only while its marker is unchanged. */
export function assertMarkerEqual(before: string, after: string, what: string): void {
  if (!before || before !== after) throw new Error(`${what}: frame marker must be unchanged (not empty)`);
}

/** Address (path#hash) must be exactly the expected one. */
export function assertAddress(got: string, expected: string, what: string): void {
  if (got !== expected) throw new Error(`${what}: address must be ${expected}, got ${got}`);
}

/** A real reload is a navigation of type "reload" in a document that lost the pre-reload sentinel. */
export function assertRealReload(navType: string | undefined, sentinelSurvived: boolean): void {
  if (navType !== "reload") throw new Error(`navigation type must be "reload", got ${navType}`);
  if (sentinelSurvived) throw new Error("document sentinel survived: the page was not reloaded");
}

/** Exact origin match on the parsed URL; a string prefix would also accept "<fixture>.other-host". */
export function isFixtureOrigin(u: URL, fixture: string): boolean {
  return u.origin === new URL(fixture).origin;
}
