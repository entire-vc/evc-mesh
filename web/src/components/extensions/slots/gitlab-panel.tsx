import { VCSLinks } from "@/components/vcs-links";
import { useSlotReady } from "./slot-host";
import type { PanelProps } from "./builtin-slots";

/** `task.panel` of the GitLab extension: the task's GitLab merge requests and branches. */
export default function GitLabPanel({ task }: PanelProps) {
  const ready = useSlotReady();
  return (
    <div className="flex-1 overflow-y-auto p-3" data-testid="slot-panel-gitlab">
      <VCSLinks taskId={task.id} only="gitlab" onLoaded={ready} />
    </div>
  );
}
