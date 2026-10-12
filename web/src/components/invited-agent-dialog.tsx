import { useEffect, useState } from "react";
import { KeyRound, Trash2 } from "lucide-react";
import { agentLabel } from "@/lib/agent-label";
import { apiErrorMessage } from "@/lib/api-error";
import { agentStatusConfig } from "@/lib/agent-utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ApiKeyRevealPanel } from "@/components/api-key-reveal";
import { useAgentWorkspaceGrantStore } from "@/stores/agent-workspace-grant";
import type { OrgChartAgentNode, WorkspaceRole } from "@/types";

interface InvitedAgentDialogProps {
  open: boolean;
  onClose: () => void;
  /** Called after a revoke or reissue changed the connection, so the caller
   *  can refetch whatever lists it shows. */
  onChanged: () => void;
  workspaceId: string;
  agent: OrgChartAgentNode | null;
  /** owner/admin of THIS workspace — the same bar the API enforces
   *  (manage_members) on POST/DELETE agent-grants. */
  canManage: boolean;
}

type Step = "view" | "confirm-revoke" | "confirm-reissue" | "key";

/**
 * Detail card for an agent invited into this workspace through
 * agent_workspace_grants (its home workspace is elsewhere). Shows only what
 * the Team directory already exposes — name, role, status — plus the grant's
 * workspace role and key prefix. Owner/admin can revoke the connection or
 * reissue its key (= revoke + re-invite via the existing grant API). The new
 * key lives in component state for the one render that shows it: it is never
 * logged, stored or put in a URL.
 */
export function InvitedAgentDialog({
  open,
  onClose,
  onChanged,
  workspaceId,
  agent,
  canManage,
}: InvitedAgentDialogProps) {
  const {
    workspaceAgentGrants,
    fetchWorkspaceAgentGrants,
    revokeAgentGrant,
    inviteAgentToWorkspace,
  } = useAgentWorkspaceGrantStore();

  const [step, setStep] = useState<Step>("view");
  const [isBusy, setIsBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [apiKey, setApiKey] = useState<string | null>(null);
  // Remembered across the revoke half of a reissue: once the grant is gone
  // from the list, its role is the only thing the re-invite still needs.
  const [pendingRole, setPendingRole] = useState<WorkspaceRole | null>(null);

  const grant = agent
    ? workspaceAgentGrants.find((g) => g.agent_id === agent.id)
    : undefined;

  useEffect(() => {
    if (!open) return;
    setStep("view");
    setError(null);
    setApiKey(null);
    setPendingRole(null);
    void fetchWorkspaceAgentGrants(workspaceId);
  }, [open, workspaceId, agent?.id, fetchWorkspaceAgentGrants]);

  if (!agent) return null;

  const statusCfg = agentStatusConfig[agent.status as keyof typeof agentStatusConfig] ?? {
    label: agent.status,
    dotColor: "bg-gray-400",
  };

  const finish = () => {
    onClose();
    if (step === "key" || pendingRole !== null) onChanged();
  };

  const reinvite = async (role: WorkspaceRole) => {
    setIsBusy(true);
    setError(null);
    try {
      const resp = await inviteAgentToWorkspace(workspaceId, agent.id, role);
      setApiKey(resp.api_key);
      setStep("key");
    } catch (err) {
      // The old key is already revoked at this point; say so, and keep the
      // role so "Retry" can finish the job.
      setPendingRole(role);
      setError(
        apiErrorMessage(err, "Failed to issue a new key") +
          " The previous key is already revoked — retry to issue a new one.",
      );
      setStep("view");
    } finally {
      setIsBusy(false);
    }
  };

  const handleReissue = async () => {
    if (!grant) return;
    setIsBusy(true);
    setError(null);
    const role = grant.role;
    try {
      await revokeAgentGrant(workspaceId, grant.id);
    } catch (err) {
      setError(apiErrorMessage(err, "Failed to revoke the previous key"));
      setStep("view");
      setIsBusy(false);
      return;
    }
    setIsBusy(false);
    await reinvite(role);
  };

  const handleRevoke = async () => {
    if (!grant) return;
    setIsBusy(true);
    setError(null);
    try {
      await revokeAgentGrant(workspaceId, grant.id);
      onClose();
      onChanged();
    } catch (err) {
      setError(apiErrorMessage(err, "Failed to revoke the connection"));
      setStep("view");
    } finally {
      setIsBusy(false);
    }
  };

  const name = agentLabel(agent.name, agent.short_tag);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) finish();
      }}
    >
      <DialogContent onClose={finish}>
        {step === "key" && apiKey ? (
          <div>
            <DialogHeader>
              <DialogTitle>New key issued</DialogTitle>
              <DialogDescription>
                Copy the API key below — it will only be shown once. The
                previous key no longer works.
              </DialogDescription>
            </DialogHeader>
            <ApiKeyRevealPanel apiKey={apiKey} onClose={finish} />
          </div>
        ) : step === "confirm-revoke" || step === "confirm-reissue" ? (
          <div>
            <DialogHeader>
              <DialogTitle>
                {step === "confirm-revoke" ? "Revoke connection" : "Reissue key"}
              </DialogTitle>
              <DialogDescription>
                {step === "confirm-revoke"
                  ? `${name}'s API key for this workspace will stop working immediately. This does not affect its access to any other workspace.`
                  : `${name}'s current key for this workspace will stop working immediately, and a new one will be shown once.`}
              </DialogDescription>
            </DialogHeader>
            {error && <p className="mt-3 text-sm text-destructive">{error}</p>}
            <DialogFooter>
              <Button
                variant="outline"
                onClick={() => setStep("view")}
                disabled={isBusy}
              >
                Cancel
              </Button>
              <Button
                variant="destructive"
                disabled={isBusy}
                onClick={() =>
                  void (step === "confirm-revoke" ? handleRevoke() : handleReissue())
                }
              >
                {isBusy
                  ? "Working..."
                  : step === "confirm-revoke"
                    ? "Revoke Connection"
                    : "Reissue Key"}
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <div>
            <DialogHeader>
              <DialogTitle className="flex items-center gap-2">
                <span className="truncate">{name}</span>
                <Badge variant="outline" className="text-xs">
                  Invited
                </Badge>
              </DialogTitle>
              <DialogDescription>
                This agent belongs to another workspace and was invited into
                this one.
              </DialogDescription>
            </DialogHeader>

            <dl className="mt-4 grid grid-cols-[8rem_1fr] gap-x-3 gap-y-2 text-sm">
              {agent.role && (
                <>
                  <dt className="text-muted-foreground">Role</dt>
                  <dd>{agent.role}</dd>
                </>
              )}
              <dt className="text-muted-foreground">Status</dt>
              <dd className="flex items-center gap-1.5">
                <span className={`h-2 w-2 rounded-full ${statusCfg.dotColor}`} />
                {statusCfg.label}
              </dd>
              {grant && (
                <>
                  <dt className="text-muted-foreground">Workspace role</dt>
                  <dd className="capitalize">{grant.role}</dd>
                  <dt className="text-muted-foreground">Key</dt>
                  <dd className="font-mono text-xs">{grant.api_key_prefix}…</dd>
                </>
              )}
            </dl>

            {error && <p className="mt-3 text-sm text-destructive">{error}</p>}

            <DialogFooter>
              {pendingRole !== null && canManage ? (
                <Button disabled={isBusy} onClick={() => void reinvite(pendingRole)}>
                  <KeyRound className="h-4 w-4" />
                  {isBusy ? "Working..." : "Retry"}
                </Button>
              ) : (
                canManage &&
                grant && (
                  <>
                    <Button
                      variant="outline"
                      onClick={() => setStep("confirm-reissue")}
                    >
                      <KeyRound className="h-4 w-4" />
                      Reissue key
                    </Button>
                    <Button
                      variant="destructive"
                      onClick={() => setStep("confirm-revoke")}
                    >
                      <Trash2 className="h-4 w-4" />
                      Revoke
                    </Button>
                  </>
                )
              )}
              <Button variant="outline" onClick={finish}>
                Close
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
