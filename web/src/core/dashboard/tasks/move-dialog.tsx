// "Move" of the triage queue: target project, then a status outside triage, then POST /tasks/:id/move. The dialog of
// the Triage page, moved here with the queue (the page goes away, PRD §5.9). The core opens it (slots.tsx, the moveTask
// action), so it lives outside the lazy group folder widgets/tasks/.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import { useProjectStore } from "@/stores/project";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select } from "@/components/ui/select";
import { toast } from "@/components/ui/toast";
import type { Task, TaskStatus } from "@/types";

export function MoveDialog({ task, onClose, onMoved }: { task: Task | null; onClose: () => void; onMoved: (taskId: string) => void }) {
  const { t } = useTranslation("notifications");
  const projects = useProjectStore((s) => s.projects);
  const [projectId, setProjectId] = useState("");
  const [statuses, setStatuses] = useState<TaskStatus[]>([]);
  const [statusId, setStatusId] = useState("");
  const [loading, setLoading] = useState(false);
  const [moving, setMoving] = useState(false);

  useEffect(() => {
    if (task) setProjectId(task.project_id);
  }, [task]);

  useEffect(() => {
    setStatuses([]);
    setStatusId("");
    if (!projectId) return;
    let live = true;
    setLoading(true);
    api<TaskStatus[]>(`/api/v1/projects/${projectId}/statuses`)
      .then((list) => {
        if (!live) return;
        // moving out of triage: a triage status is no target
        const targets = (list ?? []).filter((s) => s.category !== "triage");
        setStatuses(targets);
        setStatusId((targets.find((s) => s.category === "todo") ?? targets[0])?.id ?? "");
      })
      .catch(() => live && setStatuses([]))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [projectId]);

  const move = async () => {
    if (!task || !statusId) return;
    setMoving(true);
    try {
      // a status belongs to its project (the server refuses another project's status): change the project first
      if (projectId !== task.project_id) {
        await api(`/api/v1/tasks/${task.id}/move-to-project`, { method: "POST", body: { project_id: projectId } });
      }
      await api(`/api/v1/tasks/${task.id}/move`, { method: "POST", body: { status_id: statusId } });
      toast.success(t("dashboard.tasks.move.movedTo", { status: statuses.find((s) => s.id === statusId)?.name ?? "" }));
      onMoved(task.id);
      onClose();
    } catch (err) {
      toast.error(apiErrorMessage(err, t("dashboard.tasks.move.failed")));
    } finally {
      setMoving(false);
    }
  };

  if (!task) return null;
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent onClose={onClose} closeLabel={t("dashboard.tasks.move.cancel")} role="dialog" aria-modal="true" aria-labelledby="move-task-title">
        <DialogHeader>
          <DialogTitle id="move-task-title">{t("dashboard.tasks.move.title")}</DialogTitle>
          <p className="mt-1 line-clamp-2 text-sm text-muted-foreground">{task.title}</p>
        </DialogHeader>
        <div className="mt-4 space-y-4">
          <label className="block space-y-1.5">
            <span className="text-sm font-medium">{t("dashboard.tasks.move.project")}</span>
            <Select value={projectId} onChange={(e) => setProjectId(e.target.value)}>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          </label>
          <label className="block space-y-1.5">
            <span className="text-sm font-medium">{t("dashboard.tasks.move.status")}</span>
            {loading ? (
              <div className="h-9 animate-pulse rounded-md border border-border bg-muted" />
            ) : (
              <Select value={statusId} onChange={(e) => setStatusId(e.target.value)} disabled={statuses.length === 0}>
                {statuses.length === 0 ? (
                  <option value="">{t("dashboard.tasks.move.noStatuses")}</option>
                ) : (
                  statuses.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name}
                    </option>
                  ))
                )}
              </Select>
            )}
          </label>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={moving}>
            {t("dashboard.tasks.move.cancel")}
          </Button>
          <Button onClick={() => void move()} disabled={moving || loading || !statusId}>
            {moving ? t("dashboard.tasks.move.moving") : t("dashboard.tasks.move.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
