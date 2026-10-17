import { CheckCircle2, Circle, Clock, GitPullRequest, Hand, Loader2, XCircle } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { BadgeProps } from "./builtin-slots";

/** Pipeline states the backend stores (domain.NormalizePipelineStatus). */
const PIPELINE: Record<string, { icon: LucideIcon; color: string; spin?: boolean }> = {
  pending: { icon: Clock, color: "text-muted-foreground" },
  running: { icon: Loader2, color: "text-blue-600 dark:text-blue-400", spin: true },
  success: { icon: CheckCircle2, color: "text-emerald-600 dark:text-emerald-400" },
  failed: { icon: XCircle, color: "text-red-600 dark:text-red-400" },
  canceled: { icon: Circle, color: "text-muted-foreground" },
  skipped: { icon: Circle, color: "text-muted-foreground" },
  manual: { icon: Hand, color: "text-amber-600 dark:text-amber-400" },
};

/**
 * `task.badge` of the GitLab extension: shown on a card that has GitLab links.
 * Carries the latest pipeline state when one was reported; an unknown state
 * falls back to the plain MR icon rather than drawing something wrong.
 */
export function GitLabBadge({ task }: BadgeProps) {
  const { t } = useTranslation("task");
  if (!task.vcs_providers?.includes("gitlab")) return null;
  const status = task.vcs_pipeline_status ?? "";
  const pipeline = Object.prototype.hasOwnProperty.call(PIPELINE, status) ? PIPELINE[status] : undefined;
  const Icon = pipeline?.icon ?? GitPullRequest;
  const label = pipeline ? t(`slots.gitlab.pipeline.${status}`) : t("slots.gitlab.badge");
  return (
    <span
      className={`inline-flex items-center ${pipeline?.color ?? "text-muted-foreground"}`}
      title={label}
      data-testid="slot-badge-gitlab"
      data-pipeline-status={pipeline ? status : undefined}
    >
      <Icon
        className={`h-3.5 w-3.5${pipeline?.spin ? " animate-spin motion-reduce:animate-none" : ""}`}
        aria-label={label}
      />
    </span>
  );
}
