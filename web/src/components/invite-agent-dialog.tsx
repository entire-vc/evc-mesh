import { type FormEvent, useEffect, useState } from "react";
import { UserPlus } from "lucide-react";
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
import { ApiKeyRevealPanel } from "@/components/api-key-reveal";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAgentWorkspaceGrantStore } from "@/stores/agent-workspace-grant";
import { api } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import type { Agent, PaginatedResponse, WorkspaceRole } from "@/types";

interface InviteAgentDialogProps {
  open: boolean;
  onClose: () => void;
  workspaceId: string;
  /** Agent ids that already hold an active connection to this workspace —
   *  disabled in the candidate list rather than hidden, so a repeat pick
   *  reads as "already connected" instead of "this agent doesn't exist". */
  connectedAgentIds: Set<string>;
}

const roleOptions: { value: WorkspaceRole; label: string }[] = [
  { value: "owner", label: "Owner" },
  { value: "admin", label: "Admin" },
  { value: "member", label: "Member" },
  { value: "viewer", label: "Viewer" },
];

type DialogStep = "form" | "key";

interface CandidateGroup {
  workspaceId: string;
  workspaceName: string;
  agents: Agent[];
}

/**
 * Invite an agent — from any workspace the current user belongs to — into
 * workspaceId. There is no cross-workspace agent list in the API, and there
 * must not be one: that is exactly the isolation boundary the whole grant
 * scheme protects. So candidates are built client-side, one request per
 * workspace the user is already a member of (GET /workspaces/:id/agents,
 * access already implied by membership) — the rule is simply "you can
 * invite an agent you can already see" (task U4).
 */
export function InviteAgentDialog({
  open,
  onClose,
  workspaceId,
  connectedAgentIds,
}: InviteAgentDialogProps) {
  const { workspaces } = useWorkspaceStore();
  const { inviteAgentToWorkspace } = useAgentWorkspaceGrantStore();

  const [step, setStep] = useState<DialogStep>("form");
  const [groups, setGroups] = useState<CandidateGroup[]>([]);
  const [isLoadingCandidates, setIsLoadingCandidates] = useState(false);
  const [selectedAgentId, setSelectedAgentId] = useState("");
  const [role, setRole] = useState<WorkspaceRole>("member");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [apiKey, setApiKey] = useState<string | null>(null);

  // Deliberately NOT routed through useAgentStore — that store holds ONE
  // flat agents[] list for "whatever workspace was last fetched", and every
  // other screen (the Members tab, the agent detail card) relies on it
  // staying the CURRENT workspace's own agents. Fetching a second
  // workspace's agents through it here would silently clobber that list out
  // from under them. Local state instead, scoped to just this dialog.
  useEffect(() => {
    if (!open) return;
    setStep("form");
    setSelectedAgentId("");
    setRole("member");
    setError(null);
    setApiKey(null);

    let cancelled = false;
    setIsLoadingCandidates(true);
    void (async () => {
      const results = await Promise.all(
        workspaces.map(async (ws): Promise<CandidateGroup> => {
          try {
            const resp = await api<PaginatedResponse<Agent>>(
              `/api/v1/workspaces/${ws.id}/agents`,
            );
            return {
              workspaceId: ws.id,
              workspaceName: ws.name,
              agents: resp.items ?? [],
            };
          } catch {
            // A workspace this user can no longer reach (membership revoked
            // between page load and opening this dialog) simply contributes
            // no candidates rather than failing the whole dialog.
            return { workspaceId: ws.id, workspaceName: ws.name, agents: [] };
          }
        }),
      );
      if (!cancelled) {
        setGroups(results.filter((g) => g.agents.length > 0));
        setIsLoadingCandidates(false);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [open, workspaces]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!selectedAgentId) return;
    setIsSubmitting(true);
    setError(null);
    try {
      const resp = await inviteAgentToWorkspace(
        workspaceId,
        selectedAgentId,
        role,
      );
      setApiKey(resp.api_key);
      setStep("key");
    } catch (err) {
      setError(apiErrorMessage(err, "Failed to invite agent"));
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <DialogContent onClose={onClose}>
        {step === "form" ? (
          <form onSubmit={(e) => void handleSubmit(e)}>
            <DialogHeader>
              <DialogTitle>Invite Agent</DialogTitle>
              <DialogDescription>
                Connect an agent from a workspace you belong to. It gets a new
                API key for this workspace — its access elsewhere is
                unaffected.
              </DialogDescription>
            </DialogHeader>

            <div className="mt-4 space-y-4">
              <div className="space-y-1.5">
                <label htmlFor="iad-agent" className="text-sm font-medium">
                  Agent
                </label>
                {isLoadingCandidates ? (
                  <p className="text-sm text-muted-foreground">
                    Loading agents…
                  </p>
                ) : groups.length === 0 ? (
                  <p className="text-sm text-muted-foreground">
                    No agents found in any workspace you belong to.
                  </p>
                ) : (
                  <Select
                    id="iad-agent"
                    value={selectedAgentId}
                    onChange={(e) => setSelectedAgentId(e.target.value)}
                    required
                    autoFocus
                  >
                    <option value="" disabled>
                      Select an agent…
                    </option>
                    {groups.map((group) => (
                      <optgroup
                        key={group.workspaceId}
                        label={group.workspaceName}
                      >
                        {group.agents.map((a) => (
                          <option
                            key={a.id}
                            value={a.id}
                            disabled={connectedAgentIds.has(a.id)}
                          >
                            {a.name}
                            {connectedAgentIds.has(a.id)
                              ? " (already connected)"
                              : ""}
                          </option>
                        ))}
                      </optgroup>
                    ))}
                  </Select>
                )}
                <p className="text-xs text-muted-foreground">
                  Only agents in workspaces you belong to can be invited.
                </p>
              </div>

              <div className="space-y-1.5">
                <label htmlFor="iad-role" className="text-sm font-medium">
                  Role
                </label>
                <Select
                  id="iad-role"
                  value={role}
                  onChange={(e) => setRole(e.target.value as WorkspaceRole)}
                >
                  {roleOptions.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {opt.label}
                    </option>
                  ))}
                </Select>
              </div>

              {error && <p className="text-sm text-destructive">{error}</p>}
            </div>

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={onClose}
                disabled={isSubmitting}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={isSubmitting || !selectedAgentId}
              >
                {isSubmitting ? (
                  "Inviting..."
                ) : (
                  <>
                    <UserPlus className="h-4 w-4" />
                    Invite Agent
                  </>
                )}
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <div>
            <DialogHeader>
              <DialogTitle>Agent Connected</DialogTitle>
              <DialogDescription>
                Copy the API key below — it will only be shown once.
              </DialogDescription>
            </DialogHeader>

            {apiKey && (
              <ApiKeyRevealPanel apiKey={apiKey} onClose={onClose} />
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
