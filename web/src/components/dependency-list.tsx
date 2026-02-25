import { useState, useEffect, useCallback } from "react";
import { ArrowRight, Link2, GitMerge, Plus, X, Loader2 } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/cn";
import type { DependencyType, TaskDependency } from "@/types";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const DEP_TYPE_CONFIG: Record<
  DependencyType,
  {
    label: string;
    icon: typeof ArrowRight;
    badgeClass: string;
    description: string;
  }
> = {
  blocks: {
    label: "Blocks",
    icon: ArrowRight,
    badgeClass:
      "bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300",
    description: "This task blocks another task",
  },
  relates_to: {
    label: "Relates to",
    icon: Link2,
    badgeClass:
      "bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300",
    description: "Related task",
  },
  is_child_of: {
    label: "Child of",
    icon: GitMerge,
    badgeClass:
      "bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300",
    description: "This task is a child of another task",
  },
};

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface DependencyListProps {
  taskId: string;
  className?: string;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function DependencyList({ taskId, className }: DependencyListProps) {
  const [deps, setDeps] = useState<TaskDependency[]>([]);
  const [loading, setLoading] = useState(true);
  const [showForm, setShowForm] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [form, setForm] = useState<{
    depends_on_task_id: string;
    dependency_type: DependencyType;
  }>({
    depends_on_task_id: "",
    dependency_type: "blocks",
  });

  // ---- Fetch ---------------------------------------------------------------

  const fetchDeps = useCallback(async () => {
    try {
      const data = await api<TaskDependency[]>(
        `/api/v1/tasks/${taskId}/dependencies`,
      );
      setDeps(Array.isArray(data) ? data : []);
    } catch {
      // Non-fatal — show empty state
      setDeps([]);
    } finally {
      setLoading(false);
    }
  }, [taskId]);

  useEffect(() => {
    setLoading(true);
    setDeps([]);
    void fetchDeps();
  }, [fetchDeps]);

  // ---- Actions -------------------------------------------------------------

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = form.depends_on_task_id.trim();
    if (!trimmed) {
      setError("Task ID is required.");
      return;
    }
    // Basic UUID validation
    const uuidRegex =
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
    if (!uuidRegex.test(trimmed)) {
      setError("Please enter a valid task UUID (e.g. 550e8400-e29b-41d4-a716-446655440000).");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const created = await api<TaskDependency>(
        `/api/v1/tasks/${taskId}/dependencies`,
        {
          method: "POST",
          body: {
            depends_on_task_id: trimmed,
            dependency_type: form.dependency_type,
          },
        },
      );
      setDeps((prev) => [...prev, created]);
      setShowForm(false);
      setForm({ depends_on_task_id: "", dependency_type: "blocks" });
    } catch (err: unknown) {
      setError(
        err instanceof Error ? err.message : "Failed to add dependency.",
      );
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (depId: string) => {
    setDeletingId(depId);
    try {
      await api(`/api/v1/tasks/${taskId}/dependencies/${depId}`, {
        method: "DELETE",
      });
      setDeps((prev) => prev.filter((d) => d.id !== depId));
    } catch {
      // Silently ignore — dep row stays in list
    } finally {
      setDeletingId(null);
    }
  };

  // ---- Render --------------------------------------------------------------

  if (loading) {
    return (
      <div className={cn("space-y-2", className)}>
        <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          Loading dependencies...
        </div>
      </div>
    );
  }

  // Group by dependency type
  const grouped = deps.reduce<Record<DependencyType, TaskDependency[]>>(
    (acc, dep) => {
      const key = dep.dependency_type as DependencyType;
      if (!acc[key]) acc[key] = [];
      acc[key]!.push(dep);
      return acc;
    },
    {} as Record<DependencyType, TaskDependency[]>,
  );

  const depTypeOrder: DependencyType[] = ["blocks", "relates_to", "is_child_of"];

  return (
    <div className={cn("space-y-3", className)}>
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Link2 className="h-4 w-4" />
          Dependencies
          {deps.length > 0 && (
            <span className="text-xs text-muted-foreground">
              ({deps.length})
            </span>
          )}
        </div>
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1 text-xs"
          onClick={() => {
            setShowForm((v) => !v);
            setError(null);
          }}
        >
          <Plus className="h-3 w-3" />
          Add
        </Button>
      </div>

      {/* Grouped dependency rows */}
      {deps.length > 0 && (
        <div className="space-y-3">
          {depTypeOrder.map((type) => {
            const group = grouped[type];
            if (!group || group.length === 0) return null;
            const cfg = DEP_TYPE_CONFIG[type];
            const Icon = cfg.icon;
            return (
              <div key={type}>
                <p className="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  {cfg.label}
                </p>
                <div className="space-y-1">
                  {group.map((dep) => (
                    <div
                      key={dep.id}
                      className="group flex items-center gap-2 rounded-md border border-border bg-card px-2.5 py-1.5"
                    >
                      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      <span className="flex-1 truncate font-mono text-xs text-muted-foreground">
                        {dep.depends_on_task_id.slice(0, 8).toUpperCase()}
                      </span>
                      <Badge
                        className={cn(
                          "shrink-0 px-1.5 py-0 text-[10px] font-medium",
                          cfg.badgeClass,
                        )}
                      >
                        {cfg.label}
                      </Badge>
                      <button
                        type="button"
                        aria-label="Remove dependency"
                        onClick={() => void handleDelete(dep.id)}
                        disabled={deletingId === dep.id}
                        className="ml-1 shrink-0 rounded p-0.5 opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100 disabled:cursor-wait"
                      >
                        {deletingId === dep.id ? (
                          <Loader2 className="h-3 w-3 animate-spin" />
                        ) : (
                          <X className="h-3 w-3" />
                        )}
                      </button>
                    </div>
                  ))}
                </div>
              </div>
            );
          })}
        </div>
      )}

      {deps.length === 0 && !showForm && (
        <p className="text-xs text-muted-foreground">No dependencies yet.</p>
      )}

      {/* Add form */}
      {showForm && (
        <form
          onSubmit={(e) => void handleAdd(e)}
          className="space-y-2 rounded-lg border border-border bg-muted/20 p-3"
        >
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              Task ID (UUID)
            </label>
            <Input
              value={form.depends_on_task_id}
              onChange={(e) =>
                setForm((f) => ({ ...f, depends_on_task_id: e.target.value }))
              }
              placeholder="550e8400-e29b-41d4-a716-446655440000"
              className="h-7 font-mono text-xs"
              autoFocus
            />
          </div>
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              Relationship type
            </label>
            <Select
              value={form.dependency_type}
              onChange={(e) =>
                setForm((f) => ({
                  ...f,
                  dependency_type: e.target.value as DependencyType,
                }))
              }
              className="h-7 text-xs"
            >
              {depTypeOrder.map((type) => (
                <option key={type} value={type}>
                  {DEP_TYPE_CONFIG[type].label} — {DEP_TYPE_CONFIG[type].description}
                </option>
              ))}
            </Select>
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
          <div className="flex gap-2">
            <Button
              type="submit"
              size="sm"
              className="flex-1"
              disabled={submitting || !form.depends_on_task_id.trim()}
            >
              {submitting ? (
                <>
                  <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                  Adding...
                </>
              ) : (
                "Add Dependency"
              )}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                setShowForm(false);
                setError(null);
                setForm({ depends_on_task_id: "", dependency_type: "blocks" });
              }}
            >
              Cancel
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
