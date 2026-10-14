import { type FormEvent, useEffect, useState } from "react";
import { UserPlus } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { Avatar } from "@/components/ui/avatar";
import { useMemberStore } from "@/stores/member";
import type { ProjectRole, WorkspaceMemberWithUser } from "@/types";
import { inlineLabel } from "@/lib/user-display";
import { apiErrorMessage } from "@/lib/api-error";

interface AddProjectMemberDialogProps {
  open: boolean;
  onClose: () => void;
  projectId: string;
  workspaceMembers: WorkspaceMemberWithUser[];
}

const roleOptions: { value: ProjectRole; labelKey: string }[] = [
  { value: "member", labelKey: "addMember.roles.member" },
  { value: "admin", labelKey: "addMember.roles.admin" },
  { value: "viewer", labelKey: "addMember.roles.viewer" },
];

export function AddProjectMemberDialog({
  open,
  onClose,
  projectId,
  workspaceMembers,
}: AddProjectMemberDialogProps) {
  const { t } = useTranslation("settings");
  const { addProjectMember, projectMembers } = useMemberStore();

  const [selectedUserId, setSelectedUserId] = useState("");
  const [role, setRole] = useState<ProjectRole>("member");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Filter out workspace members who are already project members
  const availableMembers = workspaceMembers.filter(
    (wm) => !projectMembers.some((pm) => pm.user_id === wm.user_id),
  );

  // Reset form on open
  useEffect(() => {
    if (open) {
      setSelectedUserId(availableMembers[0]?.user_id ?? "");
      setRole("member");
      setError(null);
    }
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();

    if (!selectedUserId) {
      setError(t("addMember.selectMember"));
      return;
    }

    setIsSubmitting(true);
    setError(null);

    try {
      await addProjectMember(projectId, selectedUserId, role);
      onClose();
    } catch (err) {
      setError(apiErrorMessage(err, t("addMember.failed")));
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      onClose();
    }
  };

  const selectedMember = workspaceMembers.find(
    (m) => m.user_id === selectedUserId,
  );

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent onClose={onClose}>
        <DialogHeader>
          <DialogTitle>{t("addMember.title")}</DialogTitle>
          <DialogDescription>
            {t("addMember.description")}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          {availableMembers.length === 0 ? (
            <p className="py-4 text-center text-sm text-muted-foreground">
              {t("addMember.allAdded")}
            </p>
          ) : (
            <>
              {/* Member selector */}
              <div className="space-y-1.5">
                <label htmlFor="apmd-member" className="text-sm font-medium">
                  {t("addMember.member")} <span className="text-destructive">*</span>
                </label>
                <Select
                  id="apmd-member"
                  value={selectedUserId}
                  onChange={(e) => setSelectedUserId(e.target.value)}
                >
                  {availableMembers.map((member) => (
                    <option key={member.user_id} value={member.user_id}>
                      {inlineLabel(member.user)}
                    </option>
                  ))}
                </Select>
              </div>

              {/* Selected member preview */}
              {selectedMember && (
                <div className="flex items-center gap-3 rounded-lg border border-border px-3 py-2.5">
                  <Avatar
                    src={selectedMember.user.avatar_url || undefined}
                    name={selectedMember.user.name || selectedMember.user.email}
                    size="sm"
                  />
                  <div className="flex-1 min-w-0">
                    <p className="truncate text-sm font-medium">
                      {selectedMember.user.name}
                    </p>
                    <p className="truncate text-xs text-muted-foreground">
                      {selectedMember.user.email}
                    </p>
                  </div>
                </div>
              )}

              {/* Role selector */}
              <div className="space-y-1.5">
                <label htmlFor="apmd-role" className="text-sm font-medium">
                  {t("addMember.role")}
                </label>
                <Select
                  id="apmd-role"
                  value={role}
                  onChange={(e) => setRole(e.target.value as ProjectRole)}
                >
                  {roleOptions.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {t(opt.labelKey)}
                    </option>
                  ))}
                </Select>
              </div>
            </>
          )}

          {error && <p className="text-sm text-destructive">{error}</p>}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={onClose}
              disabled={isSubmitting}
            >
              {t("addMember.cancel")}
            </Button>
            {availableMembers.length > 0 && (
              <Button type="submit" disabled={isSubmitting}>
                {isSubmitting ? (
                  t("addMember.adding")
                ) : (
                  <>
                    <UserPlus className="h-4 w-4" />
                    {t("addMember.add")}
                  </>
                )}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
