import { type FormEvent, useCallback, useState } from "react";
import { Check, Copy, AlertTriangle } from "lucide-react";
import { cn } from "@/lib/cn";
import { agentTypeConfig } from "@/lib/agent-utils";
import { useAgentStore } from "@/stores/agent";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { AgentType } from "@/types";

interface RegisterAgentDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  workspaceId: string;
}

type DialogStep = "form" | "key";

export function RegisterAgentDialog({
  open,
  onOpenChange,
  workspaceId,
}: RegisterAgentDialogProps) {
  const { registerAgent } = useAgentStore();

  const [step, setStep] = useState<DialogStep>("form");
  const [name, setName] = useState("");
  const [agentType, setAgentType] = useState<AgentType>("claude_code");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [apiKey, setApiKey] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const resetForm = useCallback(() => {
    setStep("form");
    setName("");
    setAgentType("claude_code");
    setIsSubmitting(false);
    setError(null);
    setApiKey(null);
    setCopied(false);
  }, []);

  const handleClose = useCallback(() => {
    onOpenChange(false);
    // Reset form after dialog animation
    setTimeout(resetForm, 200);
  }, [onOpenChange, resetForm]);

  const handleSubmit = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      if (!name.trim()) return;

      setIsSubmitting(true);
      setError(null);

      try {
        const response = await registerAgent(workspaceId, {
          name: name.trim(),
          agent_type: agentType,
        });
        setApiKey(response.api_key);
        setStep("key");
      } catch (err) {
        setError(
          err instanceof Error ? err.message : "Failed to register agent",
        );
      } finally {
        setIsSubmitting(false);
      }
    },
    [name, agentType, workspaceId, registerAgent],
  );

  const handleCopy = useCallback(async () => {
    if (!apiKey) return;
    try {
      await navigator.clipboard.writeText(apiKey);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Fallback for older browsers
      const textArea = document.createElement("textarea");
      textArea.value = apiKey;
      document.body.appendChild(textArea);
      textArea.select();
      document.execCommand("copy");
      document.body.removeChild(textArea);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }, [apiKey]);

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent onClose={handleClose}>
        {step === "form" ? (
          <form onSubmit={handleSubmit}>
            <DialogHeader>
              <DialogTitle>Register Agent</DialogTitle>
              <DialogDescription>
                Register a new AI agent to work in this workspace. An API key
                will be generated for authentication.
              </DialogDescription>
            </DialogHeader>

            <div className="mt-4 space-y-4">
              <div className="space-y-2">
                <label
                  htmlFor="agent-name"
                  className="text-sm font-medium leading-none"
                >
                  Name
                </label>
                <Input
                  id="agent-name"
                  placeholder="My Agent"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  required
                  autoFocus
                />
              </div>

              <div className="space-y-2">
                <label
                  htmlFor="agent-type"
                  className="text-sm font-medium leading-none"
                >
                  Agent Type
                </label>
                <Select
                  id="agent-type"
                  value={agentType}
                  onChange={(e) => setAgentType(e.target.value as AgentType)}
                >
                  {(
                    Object.entries(agentTypeConfig) as [
                      AgentType,
                      { label: string },
                    ][]
                  ).map(([value, config]) => (
                    <option key={value} value={value}>
                      {config.label}
                    </option>
                  ))}
                </Select>
              </div>

              {error && (
                <p className="text-sm text-destructive">{error}</p>
              )}
            </div>

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={handleClose}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={isSubmitting || !name.trim()}>
                {isSubmitting ? "Registering..." : "Register Agent"}
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <div>
            <DialogHeader>
              <DialogTitle>Agent Registered Successfully</DialogTitle>
              <DialogDescription>
                Your agent has been registered. Copy the API key below — it will
                only be shown once.
              </DialogDescription>
            </DialogHeader>

            <div className="mt-4 space-y-4">
              <div className="rounded-lg border border-border bg-muted p-4">
                <p className="mb-2 text-xs font-medium text-muted-foreground">
                  API Key
                </p>
                <div className="flex items-center gap-2">
                  <code className="flex-1 break-all font-mono text-sm">
                    {apiKey}
                  </code>
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    onClick={handleCopy}
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
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
