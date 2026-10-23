import { parseISO } from "date-fns";
import { localClock } from "@/lib/utils";
import i18n from "@/i18n";
import type { RuntimeState, RuntimeStateKind, Task } from "@/types";

export interface RuntimeRef {
  label: string;
  /** Absolute or app-relative link; null when the ref has no page to open. */
  href: string | null;
}

const GITLAB = "https://gitlab.com";

/** Turns a WAIT ref (`card:#00000000`, `mr:g/p!3`, `pipeline:g/p#9`, `decision:@x`) into a label and a link. */
export function parseRuntimeRef(ref: string | null | undefined): RuntimeRef | null {
  if (!ref) return null;
  const card = /^card:#?([0-9a-f]{8})/i.exec(ref);
  if (card) return { label: `#${card[1]}`, href: `/t/${card[1]}` };
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
      return `${t("waiting")}${rs.ref && !rs.reason_code ? ":" : ""}${(!rs.ref || rs.reason_code) && reason ? `: ${reason}` : ""}${until ? ` ${t("until", { time: until })}` : ""}`;
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
