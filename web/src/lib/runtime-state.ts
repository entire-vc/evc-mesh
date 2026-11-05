import { parseISO } from "date-fns";
import { localClock } from "@/lib/utils";
import i18n from "@/i18n";
import type { OperationalConstraint, RuntimeState, RuntimeStateKind, Task } from "@/types";

export interface RuntimeRef {
  label: string;
  /** Absolute or app-relative link; null when the ref has no page to open. */
  href: string | null;
}

const GITLAB = "https://gitlab.com";

/** Turns a WAIT ref (`card:#00000000`, `mr:g/p!3`, `pipeline:g/p#9`, `decision:@x`) into a label and a link. */
export function parseRuntimeRef(ref: string | null | undefined): RuntimeRef | null {
  if (!ref) return null;
  // A producer may name the card by its 8-hex short id or by the full UUID (the server's own dependency constraints do).
  const card = /^card:#?(([0-9a-f]{8})(?:-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})?)(?![0-9a-f-])/i.exec(ref);
  if (card) return { label: `#${card[2]}`, href: `/t/${card[1]}` };
  const mr = /^mr:([^!]+)!(\d+)$/.exec(ref);
  if (mr) return { label: `${mr[1]}!${mr[2]}`, href: `${GITLAB}/${mr[1]}/-/merge_requests/${mr[2]}` };
  const pipe = /^pipeline:([^#]+)#(\d+)$/.exec(ref);
  if (pipe) return { label: `${pipe[1]}#${pipe[2]}`, href: `${GITLAB}/${pipe[1]}/-/pipelines/${pipe[2]}` };
  const decision = /^decision:(.+)$/.exec(ref);
  if (decision) return { label: decision[1] ?? ref, href: null };
  return { label: ref, href: null };
}

function clock(iso: string | null | undefined): string | null {
  if (!iso) return null;
  const d = parseISO(iso);
  return Number.isNaN(d.getTime()) ? null : localClock(d);
}

/** Server-supplied interpolation values are data: control characters are removed before they reach any translated text. */
function cleanParams(p: Record<string, string> | null | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(p ?? {})) out[k] = safeText(v, Number.POSITIVE_INFINITY) ?? "";
  return out;
}

/**
 * Text for the reason: the translation of `reason_code` when the web knows it,
 * otherwise the free-text `reason` (old records, codes newer than this build).
 */
export function runtimeReasonText(rs: Pick<RuntimeState, "reason" | "reason_code" | "reason_params">): string | null {
  if (rs.reason_code) {
    const key = `board:runtimeState.reasons.${rs.reason_code}`;
    if (i18n.exists(key)) return i18n.t(key, cleanParams(rs.reason_params));
  }
  return safeText(rs.reason, Number.POSITIVE_INFINITY);
}

const DIAGNOSTIC_TEXT = /\bUNKNOWN\b|\bHTTP\s*0\b|addressee\s*=\s*unknown/i;

/** True when a displayed value is an unreadable-diagnostic string (non-strings never are). */
const isDiag = (v: unknown): boolean => typeof v === "string" && DIAGNOSTIC_TEXT.test(v);
/** Reason parameters are interpolated into the displayed reason, so they are checked like any other text. */
const paramsDiag = (p: unknown): boolean => Object.values((p as object) ?? {}).some(isDiag);

/**
 * Every text field a constraint can put on screen is checked here, so no field can show a diagnostic as a
 * confirmed value. Only the exact wake predicate "unknown" (the contract's "event not nameable") is exempt.
 */
function constraintHasDiagnostic(c: OperationalConstraint): boolean {
  const w = c?.wake?.predicate;
  return (
    c?.ci?.state === "unavailable" ||
    paramsDiag(c?.reason_params) ||
    [c?.reason, c?.ci?.reason, c?.ci?.scope, c?.source?.kind, c?.source?.ref, c?.next_actor?.name, c?.next_actor?.id, c?.wake?.ref, w === "unknown" ? null : w].some(isDiag)
  );
}

/**
 * A diagnostic that could not be read ("UNKNOWN", "HTTP 0", addressee unknown) is a fetch failure,
 * not a confirmed wait. Display-only: nothing is inferred and nothing is scheduled from it.
 */
export function isUnverifiedDiagnostic(rs: Pick<RuntimeState, "kind" | "reason" | "reason_params" | "constraints">): boolean {
  if (rs.kind !== "waiting") return false;
  return isDiag(rs.reason) || paramsDiag(rs.reason_params) || (rs.constraints ?? []).some(constraintHasDiagnostic);
}

/** Kind word only (Working / Waiting / Queued / Stopped): the compact board card never shows reasons, refs or diagnostics. */
export function runtimeKindLabel(rs: Pick<RuntimeState, "kind">): string {
  return i18n.t(`board:runtimeState.${rs.kind}`);
}

/** Short text for the board badge and the title attribute. Refs are rendered separately (they are links). */
export function runtimeStateText(rs: RuntimeState): string {
  const t = (key: string, opts?: Record<string, unknown>) => i18n.t(`board:runtimeState.${key}`, opts);
  const unverified = isUnverifiedDiagnostic(rs);
  const reason = unverified ? t("unverified") : runtimeReasonText(rs);
  switch (rs.kind) {
    case "working": {
      const at = clock(rs.since);
      return `${safeText(rs.lane) ? t("workingLane", { lane: safeText(rs.lane) }) : t("working")}${at ? ` ${t("since", { time: at })}` : ""}`;
    }
    case "waiting": {
      const until = clock(rs.until);
      return `${t("waiting")}${reason ? `: ${reason}` : rs.ref ? ":" : ""}${until ? ` ${t("until", { time: until })}` : ""}`;
    }
    case "queued":
      return `${safeText(rs.lane) ? t("queuedLane", { lane: safeText(rs.lane) }) : t("queued")}${rs.position ? `, ${t("position", { position: rs.position })}` : ""}`;
    case "stopped":
      return `${t("stopped")}${reason ? `: ${reason}` : ""}`;
  }
}

/** Note appended to a stale state ("data is stale"). */
export function runtimeStaleNote(): string {
  return i18n.t("board:runtimeState.stale");
}

export function isWorking(task: Pick<Task, "runtime_state">): boolean {
  return task.runtime_state?.kind === "working";
}

export const runtimeKindTone: Record<RuntimeStateKind, string> = {
  working: "text-tone-positive-700 dark:text-tone-positive-400",
  waiting: "text-tone-sky-700 dark:text-tone-sky-400",
  queued: "text-muted-foreground",
  stopped: "text-tone-warning-700 dark:text-tone-warning-400",
};

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * The server's checkout fallback names the lane by agent id. A raw uuid is not
 * readable: show the assignee's name when it is that agent, otherwise no lane.
 */
export function readableRuntimeState(task: Pick<Task, "runtime_state" | "assignee_id" | "assignee_name">): RuntimeState | null {
  const rs = task.runtime_state;
  if (!rs || !rs.lane || !UUID.test(rs.lane)) return rs ?? null;
  return { ...rs, lane: task.assignee_id === rs.lane ? (task.assignee_name ?? null) : null };
}

const KNOWN_CLASSES = ["mesh_dependency", "local_wait", "ci_capacity", "review_rejection", "expired_authority", "write_acl"];
const MAX_TEXT = 240;
/** The collapsed technical details show a reason in full: control characters are removed, length is not cut. */
const FULL = Number.POSITIVE_INFINITY;

/** Server-supplied text is data, never markup or an operation: cap its length and drop control characters. */
function safeText(v: unknown, max = MAX_TEXT): string | null {
  if (typeof v !== "string") return null;
  // eslint-disable-next-line no-control-regex
  const cleaned = v.replace(/[\u0000-\u001f\u007f]/g, " ").trim();
  if (!cleaned) return null;
  return cleaned.length > max ? `${cleaned.slice(0, max)}…` : cleaned;
}

/** A ref is a link only when parseRuntimeRef recognises it; anything else is plain text, never an href. */
export function safeRef(ref: string | null | undefined): RuntimeRef | null {
  const text = safeText(ref);
  const parsed = parseRuntimeRef(text);
  if (!parsed || !text) return null;
  // Link only when the WHOLE ref has a known shape; a recognised prefix with trailing junk stays plain text.
  const wellFormed = /^card:#?[0-9a-f]{8}(?:-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})?$/i.test(text) || /^mr:[^!\s]+!\d+$/.test(text) || /^pipeline:[^#\s]+#\d+$/.test(text);
  if (!wellFormed) return { label: text, href: null };
  if (parsed.href && !(parsed.href.startsWith("/t/") || parsed.href.startsWith(`${GITLAB}/`))) return { ...parsed, href: null };
  return parsed;
}

/** Constraints whose target the caller may not see: they never supply a target or a link, even if the payload carries a ref. */
const HIDDEN_TARGET_CODES = new Set(["access_denied", "dependency_not_visible"]);

/** One identity rule for "the same awaited thing": ids are hex, so the comparison ignores case. */
export const sameTarget = (a: string | null | undefined, b: string | null | undefined): boolean => !!a && !!b && a.toLowerCase() === b.toLowerCase();

export type WaitTargetKind = "card" | "mr" | "pipeline" | "decision";

export interface WaitBrief {
  kind: WaitTargetKind;
  /** Link-or-text of the thing awaited; href is null when there is no safe page. */
  ref: RuntimeRef;
  /** Short visible label: `#00000000`, `!12`, `#9954`, `@name`. The full label is `ref.label`. */
  short: string;
  /** The sanitized ref exactly as the server sent it, for the collapsed technical details. */
  raw: string;
}

/**
 * The awaited thing as ONE structured target, or null when the data names none. Only well-formed refs count:
 * free text is never promoted to a target (the neutral "Waiting" is the honest answer then). The state's own
 * ref wins; a constraint's wake ref is used when the state carries none.
 */
export function waitBrief(rs: Pick<RuntimeState, "kind" | "ref" | "constraints">): WaitBrief | null {
  if (rs.kind !== "waiting") return null;
  // A constraint the caller may not see never names a target, and neither does a state-level ref that points at the same thing.
  const cs = Array.isArray(rs.constraints) ? rs.constraints : [];
  const hidden = new Set(cs.filter((c) => HIDDEN_TARGET_CODES.has(String(c?.reason_code))).map((c) => safeRef(c?.wake?.ref)?.label).filter((l): l is string => !!l));
  const candidates = [rs.ref, ...cs.filter((c) => !HIDDEN_TARGET_CODES.has(String(c?.reason_code))).map((c) => c?.wake?.ref)];
  for (const raw of candidates) {
    const text = safeText(raw, FULL);
    if (!text || isDiag(text)) continue;
    const ref = safeRef(text);
    if (!ref || [...hidden].some((h) => sameTarget(h, ref.label))) continue; // identity is case-insensitive: ids are hex
    if (/^card:/.test(text) && ref.href) return { kind: "card", ref, short: ref.label, raw: text };
    const mr = /^mr:[^!\s]+!(\d+)$/.exec(text);
    if (mr && ref.href) return { kind: "mr", ref, short: `!${mr[1]}`, raw: text };
    const pipe = /^pipeline:[^#\s]+#(\d+)$/.exec(text);
    if (pipe && ref.href) return { kind: "pipeline", ref, short: `#${pipe[1]}`, raw: text };
    const decision = /^decision:(@?[\w.-]{1,40})$/.exec(text);
    if (decision?.[1]) return { kind: "decision", ref: { label: decision[1], href: null }, short: decision[1], raw: text };
  }
  return null;
}

/** "Waiting for MR" / "Waiting for task" / … — the words before the target; the target itself is rendered as a link. */
export function waitBriefPrefix(kind: WaitTargetKind): string {
  return i18n.t(`board:runtimeState.waitFor.${kind}`);
}

/** Reason shown beside the brief: a translated known code, or a SHORT free text. Long prose stays in technical details. */
export function briefReason(rs: Pick<RuntimeState, "reason" | "reason_code" | "reason_params">): string | null {
  const r = safeText(runtimeReasonText(rs), FULL);
  if (!r || isDiag(r) || paramsDiag(rs.reason_params)) return null;
  return r.length <= 60 ? r : null;
}

/** Full state reason for the collapsed technical details; an unreadable diagnostic is never shown. */
export function stateReasonDetail(rs: Pick<RuntimeState, "reason" | "reason_code" | "reason_params">): string | null {
  if (paramsDiag(rs.reason_params)) return null;
  const r = safeText(runtimeReasonText(rs), FULL);
  return r && !isDiag(r) ? r : null;
}

/** Kanban text for a waiting card: brief prefix (+ until). The target link follows separately. */
export function waitingBoardText(rs: RuntimeState, brief: WaitBrief | null): string {
  const until = clock(rs.until);
  const base = brief ? waitBriefPrefix(brief.kind) : i18n.t("board:runtimeState.waiting");
  return until ? `${base} ${i18n.t("board:runtimeState.until", { time: until })}` : base;
}

export interface ConstraintView {
  key: string;
  classKey: string;
  classLabel: string;
  reason: string | null;
  /** The same reason without the display length cap (control characters still removed): for the collapsed technical details. */
  reasonFull: string | null;
  ref: RuntimeRef | null;
  /** Sanitized wake ref as sent; null when the ref is hidden or diagnostic. */
  rawRef: string | null;
  /** Name of who unblocks it, or null when unknown. */
  nextActor: string | null;
  /** Exact wake predicate, or null when unknown. */
  wake: string | null;
  source: string | null;
  observedAt: string | null;
  stale: boolean;
  /** CI facts, only what the provider reported. */
  ci: { label: string; details: string[] } | null;
}

/** One place that turns a constraint into display values: board and task detail both read this. */
export function constraintView(c: OperationalConstraint, index = 0): ConstraintView {
  const t = (key: string, opts?: Record<string, unknown>) => i18n.t(`board:runtimeState.constraints.${key}`, opts);
  const classKey = KNOWN_CLASSES.includes(c.class) ? c.class : "unknown";
  const code = safeText(c.reason_code);
  const codeKey = `board:runtimeState.constraints.reasonCodes.${code}`;
  const baseReason = code && i18n.exists(codeKey)
    ? i18n.t(codeKey, cleanParams(c.reason_params))
    : runtimeReasonText({ reason: safeText(c.reason), reason_code: code, reason_params: c.reason_params ?? null });
  const unverifiedReason = constraintHasDiagnostic(c);
  const clean = (v: string | null | undefined): string | null => (isDiag(v) ? null : safeText(v));
  // The one reason every return path below uses: an unreadable diagnostic is never shown as a confirmed reason.
  const reason = unverifiedReason ? i18n.t("board:runtimeState.unverified") : baseReason;
  const fullBase = code && i18n.exists(codeKey)
    ? baseReason
    : runtimeReasonText({ reason: safeText(c.reason, FULL), reason_code: code, reason_params: c.reason_params ?? null });
  const reasonFull = unverifiedReason ? reason : safeText(fullBase, FULL);
  const wakeRaw = clean(c.wake?.predicate);
  const wake = wakeRaw && wakeRaw !== "unknown" ? wakeRaw : null;
  const actorName = c.next_actor && c.next_actor.kind !== "unknown" ? clean(c.next_actor.name) ?? clean(c.next_actor.id) : null;
  const sourceRef = clean(c.source?.ref);
  const sourceKind = clean(c.source?.kind);
  const source = sourceKind ? `${sourceKind}${sourceRef ? ` ${sourceRef}` : ""}` : sourceRef;
  // Scope/position are provider evidence only for the two recognised non-unavailable states.
  const ciEvidence = c.ci?.state === "capacity" || c.ci?.state === "manual_required";
  const ci = c.ci
    ? {
        label: i18n.exists(`board:runtimeState.constraints.ci.${c.ci.state}`) ? t(`ci.${c.ci.state}`) : t("ci.unavailable"),
        details: [
          // Only provider-evidenced facts; never filled in for `unavailable`.
          ...(ciEvidence && clean(c.ci.scope) ? [t("ci.scope", { scope: clean(c.ci.scope) })] : []),
          ...(ciEvidence && typeof c.ci.position === "number" ? [t("ci.position", { position: c.ci.position })] : []),
          ...(clean(c.ci.reason) ? [clean(c.ci.reason) as string] : []),
        ],
      }
    : null;
  // A redacted constraint must not leak anything the server marked restricted, even if the payload carries it.
  if (HIDDEN_TARGET_CODES.has(String(code))) {
    return {
      key: safeText(c.id) ?? `constraint-${index}`,
      classKey,
      classLabel: t(`classes.${classKey}`),
      reason,
      reasonFull: reason,
      ref: null,
      rawRef: null,
      nextActor: null,
      wake: null,
      source: null,
      observedAt: null,
      stale: c.stale === true,
      ci: null,
    };
  }
  return {
    key: safeText(c.id) ?? `constraint-${index}`,
    classKey,
    classLabel: t(`classes.${classKey}`),
    reason,
    reasonFull,
    ref: isDiag(c.wake?.ref) || HIDDEN_TARGET_CODES.has(String(code)) ? null : safeRef(c.wake?.ref),
    rawRef: isDiag(c.wake?.ref) || HIDDEN_TARGET_CODES.has(String(code)) ? null : safeText(c.wake?.ref, FULL),
    nextActor: actorName,
    wake,
    source,
    observedAt: clock(c.observed_at),
    stale: c.stale === true,
    ci,
  };
}

export function constraintViews(rs: Pick<RuntimeState, "constraints">): ConstraintView[] {
  return Array.isArray(rs.constraints) ? rs.constraints.filter((c) => c && typeof c === "object").map(constraintView) : [];
}
