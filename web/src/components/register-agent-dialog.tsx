import { type FormEvent, useCallback, useState } from "react";
import { ArrowUpCircle, Check, Copy, AlertTriangle, Layers } from "lucide-react";
import { cn } from "@/lib/cn";
import { agentTypeConfig, splitList } from "@/lib/agent-utils";
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
import { apiErrorMessage } from "@/lib/api-error";

// Defaults deliberately non-empty (task #85714565): a freshly registered
// agent that goes untouched by its creator must not read as "takes no
// tasks" (max_concurrent_tasks=0) or "not configured" (accepts_from empty).
const DEFAULT_MAX_CONCURRENT_TASKS = 5;
const DEFAULT_ACCEPTS_FROM = "*";
const DEFAULT_WORKING_HOURS = "24/7";

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
  const [role, setRole] = useState("");
  const [responsibilityZone, setResponsibilityZone] = useState("");
  const [maxConcurrentTasks, setMaxConcurrentTasks] = useState(
    String(DEFAULT_MAX_CONCURRENT_TASKS),
  );
  const [escalationTo, setEscalationTo] = useState("");
  const [acceptsFrom, setAcceptsFrom] = useState(DEFAULT_ACCEPTS_FROM);
  const [workingHours, setWorkingHours] = useState(DEFAULT_WORKING_HOURS);
  const [capabilities, setCapabilities] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [apiKey, setApiKey] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const resetForm = useCallback(() => {
    setStep("form");
    setName("");
    setAgentType("claude_code");
    setRole("");
    setResponsibilityZone("");
    setMaxConcurrentTasks(String(DEFAULT_MAX_CONCURRENT_TASKS));
    setEscalationTo("");
    setAcceptsFrom(DEFAULT_ACCEPTS_FROM);
    setWorkingHours(DEFAULT_WORKING_HOURS);
    setCapabilities("");
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

      const parsedMax = parseInt(maxConcurrentTasks, 10);
      const caps = splitList(capabilities);
      const acceptsFromList = splitList(acceptsFrom);

      try {
        const response = await registerAgent(workspaceId, {
          name: name.trim(),
          agent_type: agentType,
          role: role.trim() || undefined,
          responsibility_zone: responsibilityZone.trim() || undefined,
          escalation_to: escalationTo.trim() || undefined,
          // Omit entirely rather than send [] when cleared — the empty
          // string reads as "not configured", and the backend already knows
          // "field omitted" means "accept from anyone" (["*"]).
          accepts_from: acceptsFromList.length > 0 ? acceptsFromList : undefined,
          max_concurrent_tasks: Number.isFinite(parsedMax) ? parsedMax : undefined,
          working_hours: workingHours.trim() || undefined,
          capabilities: caps.length > 0
            ? Object.fromEntries(caps.map((c) => [c, true]))
            : undefined,
        });
        setApiKey(response.api_key);
        setStep("key");
      } catch (err) {
        setError(
          apiErrorMessage(err, "Failed to register agent"),
        );
      } finally {
        setIsSubmitting(false);
      }
    },
    [
      name,
      agentType,
      role,
      responsibilityZone,
      maxConcurrentTasks,
      escalationTo,
      acceptsFrom,
      workingHours,
      capabilities,
      workspaceId,
      registerAgent,
    ],
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

              <div className="space-y-2">
                <label
                  htmlFor="agent-role"
                  className="text-sm font-medium leading-none"
                >
                  Role
                </label>
                <Input
                  id="agent-role"
                  placeholder="developer, lead, reviewer..."
                  value={role}
                  onChange={(e) => setRole(e.target.value)}
                />
              </div>

              <div className="space-y-2">
                <label
                  htmlFor="agent-zone"
                  className="text-sm font-medium leading-none"
                >
                  Responsibility zone
                </label>
                <Input
                  id="agent-zone"
                  value={responsibilityZone}
                  onChange={(e) => setResponsibilityZone(e.target.value)}
                />
              </div>

              <div className="flex gap-3">
                <div className="flex-1 space-y-2">
                  <label
                    htmlFor="agent-working-hours"
                    className="text-sm font-medium leading-none"
                  >
                    Working hours
                  </label>
                  <Input
                    id="agent-working-hours"
                    value={workingHours}
                    onChange={(e) => setWorkingHours(e.target.value)}
                  />
                </div>
                {/* No shipped copy for this field yet (§1r.A) — icon +
                    aria-label/title only, no rendered text. */}
                <div className="w-28 space-y-2">
                  <div className="flex h-5 items-center text-muted-foreground">
                    <Layers className="h-3.5 w-3.5" />
                  </div>
                  <Input
                    id="agent-max-concurrent"
                    type="number"
                    min={0}
                    aria-label="Max concurrent tasks"
                    title="Max concurrent tasks"
                    value={maxConcurrentTasks}
                    onChange={(e) => setMaxConcurrentTasks(e.target.value)}
                  />
                </div>
              </div>

              <div className="flex gap-3">
                <div className="flex-1 space-y-2">
                  <label
                    htmlFor="agent-accepts-from"
                    className="text-sm font-medium leading-none"
                  >
                    Accepts from
                  </label>
                  <Input
                    id="agent-accepts-from"
                    placeholder="*"
                    value={acceptsFrom}
                    onChange={(e) => setAcceptsFrom(e.target.value)}
                  />
                </div>
                <div className="flex-1 space-y-2">
                  <div className="flex h-5 items-center gap-1.5 text-muted-foreground">
                    <ArrowUpCircle className="h-3.5 w-3.5" />
                  </div>
                  <Input
                    id="agent-escalation-to"
                    aria-label="Escalation contact"
                    title="Escalation contact"
                    value={escalationTo}
                    onChange={(e) => setEscalationTo(e.target.value)}
                  />
                </div>
              </div>

              <div className="space-y-2">
                <label
                  htmlFor="agent-capabilities"
                  className="text-sm font-medium leading-none"
                >
                  Capabilities
                </label>
                <Input
                  id="agent-capabilities"
                  placeholder="code-review, docs, deploy"
                  value={capabilities}
                  onChange={(e) => setCapabilities(e.target.value)}
                />
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
