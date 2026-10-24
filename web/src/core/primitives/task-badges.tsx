import { useMemo } from "react";
import type { TaskBadgesProps, TaskData } from "@entire-vc/mesh-theme-sdk";
import { SlotBadges } from "@/components/extensions/slots/slot-host";
import type { Task } from "@/types";

/** The REST-shaped task an extension's `task.badge` reads, rebuilt from the card's data (a view gets only `props.data`, never a store). */
export function badgeTaskFromData(d: TaskData): Task {
  return {
    id: d.id,
    project_id: d.projectId,
    title: d.title,
    status_id: d.statusId,
    priority: d.priority,
    labels: d.labels,
    vcs_link_count: d.counts.vcsLinks,
    vcs_providers: d.vcsProviders,
    vcs_pipeline_status: d.pipelineStatus,
  } as unknown as Task;
}

/** `task.badge` insertion point for a theme's own card. */
export function TaskBadges({ task }: TaskBadgesProps) {
  const rest = useMemo(() => badgeTaskFromData(task), [task]);
  return <SlotBadges task={rest} />;
}
