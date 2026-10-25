import { forwardRef, memo, useMemo, type HTMLAttributes } from "react";
import { CardBody, humanGateTitle as gateTitle } from "@/themes/mesh-default/board/card";
import i18n from "@/i18n";
import { useAgentShortTagMap } from "@/hooks/use-agent-short-tag";
import { useSlotOwnedVcsProviders } from "@/components/extensions/slots/slot-host";
import { DEFAULT_FIELD_DESCRIPTORS, toTaskData } from "@/core/slots/board-data";
import type { Task, StatusCategory } from "@/types";

interface TaskCardProps extends HTMLAttributes<HTMLDivElement> {
  task: Task;
  isDragging?: boolean;
  /** Optional status category — used to suppress overdue highlighting for done/cancelled tasks. */
  statusCategory?: StatusCategory;
  /** Display name of the checkout holder, when the parent can resolve it. */
  checkedOutByName?: string;
  /** Set when the board shows this gated card in the Triage column: the card's real status name. */
  gateStatusName?: string;
}

export function humanGateTitle(task: Pick<Task, "human_gate_class" | "gate_reason">): string {
  return gateTitle({ class: task.human_gate_class === "soft" ? "soft" : "hard", reason: task.gate_reason ?? "" }, (key, vars) => i18n.t(`board:${key}`, vars));
}

/**
 * The kanban card drawn outside a board, from a REST task. The board itself draws cards
 * through the `board.card` slot (themes/mesh-default/board/card.tsx); this keeps the same
 * markup available to callers that hold a Task.
 */
export const TaskCard = memo(
  forwardRef<HTMLDivElement, TaskCardProps>(
    ({ task, isDragging, statusCategory, checkedOutByName, gateStatusName, ...props }, ref) => {
      const shortTags = useAgentShortTagMap();
      const ownedVcs = useSlotOwnedVcsProviders("task.badge");
      const data = useMemo(
        () => ({
          task: toTaskData(task, statusCategory, checkedOutByName, shortTags, ownedVcs),
          column: { id: task.status_id, kind: "status" as const, statusCategory },
          gateStatusName,
          fields: DEFAULT_FIELD_DESCRIPTORS,
          selected: false,
          isOverlay: false,
        }),
        [task, statusCategory, checkedOutByName, gateStatusName, shortTags, ownedVcs],
      );
      return <CardBody ref={ref} data={data} isDragging={isDragging} plainTitle badgeTask={task} {...props} />;
    },
  ),
);
TaskCard.displayName = "TaskCard";
