import { useCallback, useState } from "react";
import { AlertTriangle, Check, Copy, Pencil, RefreshCw, Trash2, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { agentStatusConfig, agentTypeConfig } from "@/lib/agent-utils";
import { formatDate, formatRelative } from "@/lib/utils";
import { useAgentStore } from "@/stores/agent";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { Agent } from "@/types";

interface AgentDetailDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  agent: Agent | null;
}

type DialogMode = "detail" | "regenerate-confirm" | "regenerate-key" | "delete-confirm";

export function AgentDetailDialog({
  open,
  onOpenChange,
  agent,
}: AgentDetailDialogProps) {
  const { updateAgent, deleteAgent, regenerateKey } = useAgentStore();

  const [mode, setMode] = useState<DialogMode>("detail");
  const [editingName, setEditingName] = useState(false);
  const [nameDraft, setNameDraft] = useState("");
  const [editingDescription, setEditingDescription] = useState(false);
  const [descriptionDraft, setDescriptionDraft] = useState("");
  const [newApiKey, setNewApiKey] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const resetState = useCallback(() => {
    setMode("detail");
    setEditingName(false);
    setNameDraft("");
    setEditingDescription(false);
    setDescriptionDraft("");
    setNewApiKey(null);
    setCopied(false);
    setIsLoading(false);
    setError(null);
  }, []);

  const handleClose = useCallback(() => {
    onOpenChange(false);
    setTimeout(resetState, 200);
  }, [onOpenChange, resetState]);

  const handleStartEditName = useCallback(() => {
    if (!agent) return;
    setNameDraft(agent.name);
    setEditingName(true);
  }, [agent]);

  const handleSaveName = useCallback(async () => {
    if (!agent || !nameDraft.trim()) return;
    setIsLoading(true);
    setError(null);
    try {
      await updateAgent(agent.id, { name: nameDraft.trim() });
      setEditingName(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to update agent");
    } finally {
      setIsLoading(false);
    }
  }, [agent, nameDraft, updateAgent]);

  const handleStartEditDescription = useCallback(() => {
    if (!agent) return;
    setDescriptionDraft(agent.profile_description ?? "");
    setEditingDescription(true);
  }, [agent]);

  const handleSaveDescription = useCallback(async () => {
    if (!agent) return;
    setIsLoading(true);
    setError(null);
    try {
      await updateAgent(agent.id, { profile_description: descriptionDraft.trim() });
      setEditingDescription(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to update description");
    } finally {
      setIsLoading(false);
    }
  }, [agent, descriptionDraft, updateAgent]);

  const handleRegenerateKey = useCallback(async () => {
    if (!agent) return;
    setIsLoading(true);
    setError(null);
    try {
      const result = await regenerateKey(agent.id);
      setNewApiKey(result.api_key);
      setMode("regenerate-key");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to regenerate key");
      setMode("detail");
    } finally {
      setIsLoading(false);
    }
  }, [agent, regenerateKey]);

  const handleDelete = useCallback(async () => {
    if (!agent) return;
    setIsLoading(true);
    setError(null);
    try {
      await deleteAgent(agent.id);
      handleClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete agent");
      setMode("detail");
    } finally {
      setIsLoading(false);
    }
  }, [agent, deleteAgent, handleClose]);

  const handleCopy = useCallback(async () => {
    if (!newApiKey) return;
    try {
      await navigator.clipboard.writeText(newApiKey);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      const textArea = document.createElement("textarea");
      textArea.value = newApiKey;
      document.body.appendChild(textArea);
      textArea.select();
      document.execCommand("copy");
      document.body.removeChild(textArea);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }, [newApiKey]);

  if (!agent) return null;

  const typeConfig = agentTypeConfig[agent.agent_type];
  const statusConfig = agentStatusConfig[agent.status];

  const metadata = agent.metadata || {};
  const tasksCompleted =
    typeof metadata.tasks_completed === "number"
      ? metadata.tasks_completed
      : null;
  const totalErrors =
    typeof metadata.total_errors === "number" ? metadata.total_errors : null;
  const currentTask =
    metadata.current_task && typeof metadata.current_task === "object"
      ? (metadata.current_task as { id?: string; title?: string })
      : null;

  // Delete confirmation mode
  if (mode === "delete-confirm") {
    return (
      <Dialog open={open} onOpenChange={handleClose}>
        <DialogContent onClose={handleClose}>
          <DialogHeader>
            <DialogTitle>Delete Agent</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete <strong>{agent.name}</strong>? This action cannot be undone.
            </DialogDescription>
          </DialogHeader>
          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setMode("detail")} disabled={isLoading}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void handleDelete()} disabled={isLoading}>
              {isLoading ? "Deleting..." : "Delete Agent"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }

  // Regenerate key confirmation mode
  if (mode === "regenerate-confirm") {
    return (
      <Dialog open={open} onOpenChange={handleClose}>
        <DialogContent onClose={handleClose}>
          <DialogHeader>
            <DialogTitle>Regenerate API Key</DialogTitle>
            <DialogDescription>
              This will invalidate the current API key for <strong>{agent.name}</strong>. Any integrations using the old key will stop working immediately.
            </DialogDescription>
          </DialogHeader>
          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setMode("detail")} disabled={isLoading}>
              Cancel
            </Button>
            <Button onClick={() => void handleRegenerateKey()} disabled={isLoading}>
              {isLoading ? "Regenerating..." : "Regenerate Key"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }

  // New key display mode
  if (mode === "regenerate-key") {
    return (
      <Dialog open={open} onOpenChange={handleClose}>
        <DialogContent onClose={handleClose}>
          <DialogHeader>
            <DialogTitle>New API Key Generated</DialogTitle>
            <DialogDescription>
              Copy this key now — it will only be shown once.
            </DialogDescription>
          </DialogHeader>

          <div className="mt-2 space-y-4">
            <div className="rounded-lg border border-border bg-muted p-4">
              <p className="mb-2 text-xs font-medium text-muted-foreground">
                API Key
              </p>
              <div className="flex items-center gap-2">
                <code className="flex-1 break-all font-mono text-sm">
                  {newApiKey}
                </code>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  onClick={() => void handleCopy()}
                  className="shrink-0"
                >
                  {copied ? (
                    <Check className="h-4 w-4 text-green-500" />
                  ) : (
                    <Copy className="h-4 w-4" />
                  )}
                </Button>
              </div>
            </div>

            <div
              className={cn(
                "flex items-start gap-2 rounded-lg border border-yellow-200 bg-yellow-50 p-3",
                "dark:border-yellow-900 dark:bg-yellow-950",
              )}
            >
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-yellow-600" />
              <p className="text-sm text-yellow-800 dark:text-yellow-200">
                This key will only be shown once. Store it securely. You will
                not be able to retrieve it later.
              </p>
            </div>
          </div>

          <DialogFooter>
            <Button onClick={handleClose}>
              {copied ? "Done" : "Close"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }

  // Default detail mode
  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent
        onClose={handleClose}
        className="max-h-[85vh] overflow-y-auto"
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-3">
            {editingName ? (
              <div className="flex flex-1 items-center gap-2">
                <Input
                  value={nameDraft}
                  onChange={(e) => setNameDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void handleSaveName();
                    if (e.key === "Escape") setEditingName(false);
                  }}
                  className="h-8 flex-1 text-base font-semibold"
                  autoFocus
                />
                <Button
                  size="icon"
                  variant="ghost"
                  className="h-7 w-7 shrink-0"
                  onClick={() => void handleSaveName()}
                  disabled={isLoading}
                >
                  <Check className="h-4 w-4" />
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="h-7 w-7 shrink-0"
                  onClick={() => setEditingName(false)}
                >
                  <X className="h-4 w-4" />
                </Button>
              </div>
            ) : (
              <>
                <span className="flex-1">{agent.name}</span>
                <Button
                  size="icon"
                  variant="ghost"
                  className="h-7 w-7 shrink-0"
                  onClick={handleStartEditName}
                  title="Edit name"
                >
                  <Pencil className="h-3.5 w-3.5" />
                </Button>
              </>
            )}
            <Badge className={cn("text-xs", typeConfig.color)}>
              {typeConfig.label}
            </Badge>
          </DialogTitle>
        </DialogHeader>

        {error && (
          <p className="text-sm text-destructive">{error}</p>
        )}

        <div className="mt-4 space-y-5">
          {/* Status */}
          <DetailRow label="Status">
            <div className="flex items-center gap-2">
              <span
                className={cn("h-2.5 w-2.5 rounded-full", statusConfig.dotColor)}
              />
              <span className="text-sm">{statusConfig.label}</span>
            </div>
          </DetailRow>

          {/* Last Heartbeat */}
          <DetailRow label="Last Heartbeat">
            <span className="text-sm">
              {agent.last_heartbeat
                ? formatRelative(agent.last_heartbeat)
                : "Never"}
            </span>
          </DetailRow>

          {/* Registered */}
          <DetailRow label="Registered">
            <span className="text-sm">{formatDate(agent.created_at)}</span>
          </DetailRow>

          {/* API Key (masked) */}
          <DetailRow label="API Key">
            <code className="rounded bg-muted px-2 py-0.5 font-mono text-xs">
              {agent.api_key_hash
                ? `${agent.api_key_hash.substring(0, 12)}...`
                : "N/A"}
            </code>
          </DetailRow>

          {/* Description */}
          <div>
            <div className="mb-1.5 flex items-center justify-between">
              <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                Description
              </span>
              {!editingDescription && (
                <Button
                  size="icon"
                  variant="ghost"
                  className="h-6 w-6"
                  onClick={handleStartEditDescription}
                  title="Edit description"
                >
                  <Pencil className="h-3 w-3" />
                </Button>
              )}
            </div>
            {editingDescription ? (
              <div className="space-y-2">
                <Textarea
                  value={descriptionDraft}
                  onChange={(e) => setDescriptionDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Escape") setEditingDescription(false);
                    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                      void handleSaveDescription();
                    }
                  }}
                  placeholder="Describe this agent's purpose, skills, or context..."
                  className="min-h-[80px] resize-none text-sm"
                  autoFocus
                />
                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    onClick={() => void handleSaveDescription()}
                    disabled={isLoading}
                    className="h-7 gap-1.5"
                  >
                    <Check className="h-3 w-3" />
                    Save
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setEditingDescription(false)}
                    disabled={isLoading}
                    className="h-7 gap-1.5"
                  >
                    <X className="h-3 w-3" />
                    Cancel
                  </Button>
                </div>
              </div>
            ) : (
              <p
                className={cn(
                  "text-sm",
                  agent.profile_description
                    ? "text-foreground"
                    : "italic text-muted-foreground",
                )}
              >
                {agent.profile_description || "No description"}
              </p>
            )}
          </div>

          {/* Current Task */}
          {currentTask && (
            <DetailRow label="Current Task">
              <span className="text-sm">
                {currentTask.title || currentTask.id || "In progress"}
              </span>
            </DetailRow>
          )}

          {/* Capabilities */}
          {(() => {
            const caps = Array.isArray(agent.capabilities) ? agent.capabilities : Object.keys(agent.capabilities ?? {});
            return caps.length > 0 ? (
            <div>
              <p className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
                Capabilities
              </p>
              <div className="flex flex-wrap gap-1.5">
                {caps.map((cap) => (
                  <Badge key={cap} variant="outline" className="text-xs">
                    {cap}
                  </Badge>
                ))}
              </div>
            </div>
            ) : null;
          })()}

          {/* Statistics */}
          {(tasksCompleted !== null || totalErrors !== null) && (
            <div>
              <p className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
                Statistics
              </p>
              <div className="grid grid-cols-2 gap-3">
                {tasksCompleted !== null && (
                  <div className="rounded-lg border border-border p-3">
                    <p className="text-xs text-muted-foreground">
                      Tasks Completed
                    </p>
                    <p className="text-lg font-semibold">{tasksCompleted}</p>
                  </div>
                )}
                {totalErrors !== null && (
                  <div className="rounded-lg border border-border p-3">
                    <p className="text-xs text-muted-foreground">
                      Total Errors
                    </p>
                    <p className="text-lg font-semibold">{totalErrors}</p>
                  </div>
                )}
              </div>
            </div>
          )}
        </div>

        {/* Management actions */}
        <div className="mt-6 flex items-center justify-between border-t border-border pt-4">
          <Button
            variant="outline"
            size="sm"
            className="gap-2"
            onClick={() => setMode("regenerate-confirm")}
          >
            <RefreshCw className="h-3.5 w-3.5" />
            Regenerate Key
          </Button>
          <Button
            variant="destructive"
            size="sm"
            className="gap-2"
            onClick={() => setMode("delete-confirm")}
          >
            <Trash2 className="h-3.5 w-3.5" />
            Delete Agent
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function DetailRow({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      {children}
    </div>
  );
}
