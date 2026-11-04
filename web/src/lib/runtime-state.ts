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

/**
 * Text for the reason: the translation of `reason_code` when the web knows it,
 * otherwise the free-text `reason` (old records, codes newer than this build).
 */
export function runtimeReasonText(rs: Pick<RuntimeState, "reason" | "reason_code" | "reason_params">): string | null {
  if (rs.reason_code) {
    const key = `board:runtimeState.reasons.${rs.reason_code}`;
    if (i18n.exists(key)) return i18n.t(key, { ...(rs.reason_params ?? {}) });
  }
  return rs.reason ?? null;
}

/** Short text for the board badge and the title attribute. Refs are rendered separately (they are links). */
export function runtimeStateText(rs: RuntimeState): string {
  const t = (key: string, opts?: Record<string, unknown>) => i18n.t(`board:runtimeState.${key}`, opts);
  const reason = runtimeReasonText(rs);
  switch (rs.kind) {
    case "working": {
      const at = clock(rs.since);
      return `${rs.lane ? t("workingLane", { lane: rs.lane }) : t("working")}${at ? ` ${t("since", { time: at })}` : ""}`;
    }
    case "waiting": {
      const until = clock(rs.until);
      return `${t("waiting")}${reason ? `: ${reason}` : rs.ref ? ":" : ""}${until ? ` ${t("until", { time: until })}` : ""}`;
    }
    case "queued":
      return `${rs.lane ? t("queuedLane", { lane: rs.lane }) : t("queued")}${rs.position ? `, ${t("position", { position: rs.position })}` : ""}`;
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

/** Server-supplied text is data, never markup or an operation: cap its length and drop control characters. */
function safeText(v: unknown): string | null {
  if (typeof v !== "string") return null;
  // eslint-disable-next-line no-control-regex
  const cleaned = v.replace(/[\u0000-\u001f\u007f]/g, " ").trim();
  if (!cleaned) return null;
  return cleaned.length > MAX_TEXT ? `${cleaned.slice(0, MAX_TEXT)}…` : cleaned;
}

/** A ref is a link only when parseRuntimeRef recognises it; anything else is plain text, never an href. */
function safeRef(ref: string | null | undefined): RuntimeRef | null {
  const text = safeText(ref);
  const parsed = parseRuntimeRef(text);
  if (!parsed || !text) return null;
  // Link only when the WHOLE ref has a known shape; a recognised prefix with trailing junk stays plain text.
  const wellFormed = /^card:#?[0-9a-f]{8}(?:-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})?$/i.test(text) || /^mr:[^!\s]+!\d+$/.test(text) || /^pipeline:[^#\s]+#\d+$/.test(text);
  if (!wellFormed) return { label: text, href: null };
  if (parsed.href && !(parsed.href.startsWith("/t/") || parsed.href.startsWith(`${GITLAB}/`))) return { ...parsed, href: null };
  return parsed;
}

export interface ConstraintView {
  key: string;
  classKey: string;
  classLabel: string;
  reason: string | null;
  ref: RuntimeRef | null;
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
  const reason = code && i18n.exists(codeKey)
    ? i18n.t(codeKey, { ...(c.reason_params ?? {}) })
    : runtimeReasonText({ reason: safeText(c.reason), reason_code: code, reason_params: c.reason_params ?? null });
  const wakeRaw = safeText(c.wake?.predicate);
  const wake = wakeRaw && wakeRaw !== "unknown" ? wakeRaw : null;
  const actorName = c.next_actor && c.next_actor.kind !== "unknown" ? safeText(c.next_actor.name) ?? safeText(c.next_actor.id) : null;
  const sourceRef = safeText(c.source?.ref);
  const source = safeText(c.source?.kind) ? `${safeText(c.source.kind)}${sourceRef ? ` ${sourceRef}` : ""}` : sourceRef;
  // Scope/position are provider evidence only for the two recognised non-unavailable states.
  const ciEvidence = c.ci?.state === "capacity" || c.ci?.state === "manual_required";
  const ci = c.ci
    ? {
        label: i18n.exists(`board:runtimeState.constraints.ci.${c.ci.state}`) ? t(`ci.${c.ci.state}`) : t("ci.unavailable"),
        details: [
          // Only provider-evidenced facts; never filled in for `unavailable`.
          ...(ciEvidence && safeText(c.ci.scope) ? [t("ci.scope", { scope: safeText(c.ci.scope) })] : []),
          ...(ciEvidence && typeof c.ci.position === "number" ? [t("ci.position", { position: c.ci.position })] : []),
          ...(safeText(c.ci.reason) ? [safeText(c.ci.reason) as string] : []),
        ],
      }
    : null;
  // A redacted constraint must not leak anything the server marked restricted, even if the payload carries it.
  if (code === "access_denied") {
    return {
      key: safeText(c.id) ?? `constraint-${index}`,
      classKey,
      classLabel: t(`classes.${classKey}`),
      reason,
      ref: null,
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
    ref: safeRef(c.wake?.ref),
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
