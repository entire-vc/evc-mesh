import { type FormEvent, useCallback, useEffect, useState } from "react";
import { useParams } from "react-router";
import {
  CheckCircle,
  ChevronDown,
  ChevronUp,
  Plus,
  TrendingDown,
  TrendingUp,
  Minus,
} from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useProjectUpdateStore } from "@/stores/project-update";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Select } from "@/components/ui/select";
import { toast } from "@/components/ui/toast";
import type { CreateProjectUpdateRequest, ProjectUpdate, UpdateStatus } from "@/types";
import { Columns3, List, GitBranch, FileText } from "lucide-react";
import { Link } from "react-router";
import { cn } from "@/lib/cn";

const STATUS_CONFIG: Record<UpdateStatus, { label: string; color: string; icon: React.ReactNode }> = {
  on_track: {
    label: "On Track",
    color: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200",
    icon: <TrendingUp className="h-3 w-3" />,
  },
  at_risk: {
    label: "At Risk",
    color: "bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200",
    icon: <Minus className="h-3 w-3" />,
  },
  off_track: {
    label: "Off Track",
    color: "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200",
    icon: <TrendingDown className="h-3 w-3" />,
  },
  completed: {
    label: "Completed",
    color: "bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200",
    icon: <CheckCircle className="h-3 w-3" />,
  },
};

function StatusBadge({ status }: { status: UpdateStatus }) {
  const cfg = STATUS_CONFIG[status] ?? STATUS_CONFIG.on_track;
  return (
    <span className={cn("inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium", cfg.color)}>
      {cfg.icon}
      {cfg.label}
    </span>
  );
}

function BulletList({ items }: { items: { text: string }[] }) {
  if (!items || items.length === 0) return <p className="text-sm text-muted-foreground italic">None</p>;
  return (
    <ul className="list-disc pl-4 space-y-1">
      {items.map((item, i) => (
        <li key={i} className="text-sm">{item.text}</li>
      ))}
    </ul>
  );
}

function UpdateCard({ update }: { update: ProjectUpdate }) {
  const [expanded, setExpanded] = useState(false);
  const date = new Date(update.created_at).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });

  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex items-start justify-between gap-3">
          <div className="flex-1">
            <div className="flex items-center gap-2 mb-1">
              <StatusBadge status={update.status} />
              <span className="text-xs text-muted-foreground">{date}</span>
            </div>
            <CardTitle className="text-base">{update.title}</CardTitle>
          </div>
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 shrink-0"
            onClick={() => setExpanded((v) => !v)}
          >
            {expanded ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
          </Button>
        </div>
        <p className="text-sm text-muted-foreground">{update.summary}</p>
      </CardHeader>
      {expanded && (
        <CardContent className="space-y-4 pt-0">
          {/* Metrics */}
          {update.metrics && (
            <div className="flex gap-4 rounded-lg bg-muted/50 p-3">
              <div className="text-center">
                <p className="text-xl font-bold">{update.metrics.tasks_completed}</p>
                <p className="text-xs text-muted-foreground">Done</p>
              </div>
              <div className="text-center">
                <p className="text-xl font-bold">{update.metrics.tasks_in_progress}</p>
                <p className="text-xs text-muted-foreground">In Progress</p>
              </div>
              <div className="text-center">
                <p className="text-xl font-bold">{update.metrics.tasks_total}</p>
                <p className="text-xs text-muted-foreground">Total</p>
              </div>
              {update.metrics.tasks_total > 0 && (
                <div className="text-center">
                  <p className="text-xl font-bold">
                    {Math.round(((update.metrics.tasks_completed ?? 0) / (update.metrics.tasks_total || 1)) * 100)}%
                  </p>
                  <p className="text-xs text-muted-foreground">Complete</p>
                </div>
              )}
            </div>
          )}
          <div className="grid gap-4 sm:grid-cols-3">
            <div>
              <h4 className="text-sm font-semibold text-emerald-700 dark:text-emerald-400 mb-2">Highlights</h4>
              <BulletList items={update.highlights ?? []} />
            </div>
            <div>
              <h4 className="text-sm font-semibold text-red-700 dark:text-red-400 mb-2">Blockers</h4>
              <BulletList items={update.blockers ?? []} />
            </div>
            <div>
              <h4 className="text-sm font-semibold text-blue-700 dark:text-blue-400 mb-2">Next Steps</h4>
              <BulletList items={update.next_steps ?? []} />
            </div>
          </div>
        </CardContent>
      )}
    </Card>
  );
}

function NewUpdateForm({
  projectId,
  onClose,
}: {
  projectId: string;
  onClose: () => void;
}) {
  const { createUpdate } = useProjectUpdateStore();
  const [title, setTitle] = useState("");
  const [status, setStatus] = useState<UpdateStatus>("on_track");
  const [summary, setSummary] = useState("");
  const [highlightsRaw, setHighlightsRaw] = useState("");
  const [blockersRaw, setBlockersRaw] = useState("");
  const [nextStepsRaw, setNextStepsRaw] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const parseLines = (raw: string) =>
    raw
      .split("\n")
      .map((t) => t.trim())
      .filter(Boolean)
      .map((text) => ({ text }));

  const handleSubmit = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      if (!title.trim() || !summary.trim()) return;
      setSubmitting(true);
      try {
        const req: CreateProjectUpdateRequest = {
          title: title.trim(),
          status,
          summary: summary.trim(),
          highlights: parseLines(highlightsRaw),
          blockers: parseLines(blockersRaw),
          next_steps: parseLines(nextStepsRaw),
        };
        await createUpdate(projectId, req);
        toast.success("Update posted");
        onClose();
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "Failed to create update");
      } finally {
        setSubmitting(false);
      }
    },
    [title, status, summary, highlightsRaw, blockersRaw, nextStepsRaw, projectId, createUpdate, onClose],
  );

  return (
    <Card className="border-primary/30">
      <CardHeader className="pb-3">
        <CardTitle className="text-base">New Status Update</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1">
              <label className="text-sm font-medium">Title</label>
              <Input
                placeholder="Sprint 5 Update"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                required
              />
            </div>
            <div className="space-y-1">
              <label className="text-sm font-medium">Status</label>
              <Select
                value={status}
                onChange={(e) => setStatus(e.target.value as UpdateStatus)}
              >
                <option value="on_track">On Track</option>
                <option value="at_risk">At Risk</option>
                <option value="off_track">Off Track</option>
                <option value="completed">Completed</option>
              </Select>
            </div>
          </div>
          <div className="space-y-1">
            <label className="text-sm font-medium">Summary</label>
            <Textarea
              placeholder="Brief overview of project status..."
              value={summary}
              onChange={(e) => setSummary(e.target.value)}
              rows={2}
              required
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-3">
            <div className="space-y-1">
              <label className="text-sm font-medium text-emerald-700 dark:text-emerald-400">
                Highlights (one per line)
              </label>
              <Textarea
                placeholder="Feature X shipped"
                value={highlightsRaw}
                onChange={(e) => setHighlightsRaw(e.target.value)}
                rows={3}
              />
            </div>
            <div className="space-y-1">
              <label className="text-sm font-medium text-red-700 dark:text-red-400">
                Blockers (one per line)
              </label>
              <Textarea
                placeholder="Waiting on API design"
                value={blockersRaw}
                onChange={(e) => setBlockersRaw(e.target.value)}
                rows={3}
              />
            </div>
            <div className="space-y-1">
              <label className="text-sm font-medium text-blue-700 dark:text-blue-400">
                Next Steps (one per line)
              </label>
              <Textarea
                placeholder="Complete auth module"
                value={nextStepsRaw}
                onChange={(e) => setNextStepsRaw(e.target.value)}
                rows={3}
              />
            </div>
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting ? "Posting..." : "Post Update"}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

export function ProjectUpdatesPage() {
  const { wsSlug, projectSlug } = useParams();
  const { currentProject } = useProjectStore();
  const { updates, isLoading, fetchUpdates } = useProjectUpdateStore();
  const [showForm, setShowForm] = useState(false);

  useEffect(() => {
    if (currentProject) {
      fetchUpdates(currentProject.id);
    }
  }, [currentProject, fetchUpdates]);

  return (
    <div className="space-y-6">
      {/* Header with view toggle */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">
            {currentProject?.name ?? "Project"}
          </h1>
          <p className="text-muted-foreground">Status updates and progress reports</p>
        </div>
        <div className="flex items-center gap-2">
          {/* View toggle links */}
          <div className="flex items-center rounded-lg border bg-card p-1">
            <Link
              to={`/w/${wsSlug}/p/${projectSlug}`}
              className="flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-muted-foreground hover:bg-accent"
            >
              <Columns3 className="h-4 w-4" /> Board
            </Link>
            <Link
              to={`/w/${wsSlug}/p/${projectSlug}/list`}
              className="flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-muted-foreground hover:bg-accent"
            >
              <List className="h-4 w-4" /> List
            </Link>
            <Link
              to={`/w/${wsSlug}/p/${projectSlug}/timeline`}
              className="flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-muted-foreground hover:bg-accent"
            >
              <GitBranch className="h-4 w-4" /> Timeline
            </Link>
            <span className="flex items-center gap-1.5 rounded-md bg-accent px-2.5 py-1.5 text-sm font-medium">
              <FileText className="h-4 w-4" /> Updates
            </span>
          </div>
          <Button onClick={() => setShowForm(true)}>
            <Plus className="h-4 w-4" />
            New Update
          </Button>
        </div>
      </div>

      {showForm && currentProject && (
        <NewUpdateForm
          projectId={currentProject.id}
          onClose={() => setShowForm(false)}
        />
      )}

      {isLoading ? (
        <div className="space-y-4">
          {Array.from({ length: 3 }).map((_, i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-5 w-32" />
                <Skeleton className="h-4 w-64 mt-1" />
              </CardHeader>
            </Card>
          ))}
        </div>
      ) : updates.length === 0 && !showForm ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12">
            <FileText className="mb-4 h-12 w-12 text-muted-foreground" />
            <h3 className="mb-2 text-lg font-semibold">No updates yet</h3>
            <p className="mb-4 text-sm text-muted-foreground">
              Post the first status update to keep your team informed.
            </p>
            <Button onClick={() => setShowForm(true)}>
              <Plus className="h-4 w-4" />
              Post Update
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-4">
          {updates.map((update) => (
            <UpdateCard key={update.id} update={update} />
          ))}
        </div>
      )}
    </div>
  );
}
