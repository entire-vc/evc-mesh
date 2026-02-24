import { type FormEvent, useCallback, useState } from "react";
import { Link, useParams } from "react-router";
import { FolderKanban, Plus, X } from "lucide-react";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

export function DashboardPage() {
  const { wsSlug } = useParams();
  const { currentWorkspace } = useWorkspaceStore();
  const { projects, isLoading, createProject } = useProjectStore();
  const [showForm, setShowForm] = useState(false);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">
            {currentWorkspace?.name || "Dashboard"}
          </h1>
          <p className="text-muted-foreground">
            Workspace overview and project navigation
          </p>
        </div>
        <Button onClick={() => setShowForm(true)}>
          <Plus className="h-4 w-4" />
          New Project
        </Button>
      </div>

      {showForm && currentWorkspace && (
        <CreateProjectForm
          workspaceId={currentWorkspace.id}
          onCreate={createProject}
          onClose={() => setShowForm(false)}
        />
      )}

      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-5 w-32" />
                <Skeleton className="h-4 w-48" />
              </CardHeader>
              <CardContent>
                <Skeleton className="h-8 w-full" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : projects.length === 0 && !showForm ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12">
            <FolderKanban className="mb-4 h-12 w-12 text-muted-foreground" />
            <h3 className="mb-2 text-lg font-semibold">No projects yet</h3>
            <p className="mb-4 text-sm text-muted-foreground">
              Create your first project to start managing tasks.
            </p>
            <Button onClick={() => setShowForm(true)}>
              <Plus className="h-4 w-4" />
              Create Project
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {projects.map((project) => (
            <Link key={project.id} to={`/w/${wsSlug}/p/${project.slug}`}>
              <Card className="transition-shadow hover:shadow-md">
                <CardHeader>
                  <div className="flex items-center gap-2">
                    <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10 text-sm">
                      {project.icon || project.name.charAt(0).toUpperCase()}
                    </div>
                    <div>
                      <CardTitle className="text-base">
                        {project.name}
                      </CardTitle>
                      <CardDescription className="text-xs">
                        {project.slug}
                      </CardDescription>
                    </div>
                  </div>
                </CardHeader>
                <CardContent>
                  <p className="line-clamp-2 text-sm text-muted-foreground">
                    {project.description || "No description"}
                  </p>
                </CardContent>
              </Card>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}

function CreateProjectForm({
  workspaceId,
  onCreate,
  onClose,
}: {
  workspaceId: string;
  onCreate: (workspaceId: string, req: { name: string; slug: string; description?: string }) => Promise<unknown>;
  onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const handleNameChange = useCallback((value: string) => {
    setName(value);
    setSlug(
      value
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, ""),
    );
  }, []);

  const handleSubmit = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      if (!name.trim() || !slug.trim()) return;
      setError(null);
      setCreating(true);
      try {
        await onCreate(workspaceId, {
          name: name.trim(),
          slug: slug.trim(),
          description: description.trim() || undefined,
        });
        onClose();
      } catch (err) {
        setError(err instanceof Error ? err.message : "Failed to create project");
      } finally {
        setCreating(false);
      }
    },
    [name, slug, description, workspaceId, onCreate, onClose],
  );

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between pb-3">
        <CardTitle className="text-base">New Project</CardTitle>
        <Button variant="ghost" size="icon" className="h-7 w-7" onClick={onClose}>
          <X className="h-4 w-4" />
        </Button>
      </CardHeader>
      <form onSubmit={handleSubmit}>
        <CardContent className="space-y-3">
          {error && (
            <div className="rounded-lg bg-destructive/10 p-3 text-sm text-destructive">
              {error}
            </div>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1">
              <label htmlFor="proj-name" className="text-sm font-medium">Name</label>
              <Input
                id="proj-name"
                placeholder="My Project"
                value={name}
                onChange={(e) => handleNameChange(e.target.value)}
                required
                autoFocus
              />
            </div>
            <div className="space-y-1">
              <label htmlFor="proj-slug" className="text-sm font-medium">Slug</label>
              <Input
                id="proj-slug"
                placeholder="my-project"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                required
              />
            </div>
          </div>
          <div className="space-y-1">
            <label htmlFor="proj-desc" className="text-sm font-medium">Description</label>
            <Input
              id="proj-desc"
              placeholder="Optional description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
        </CardContent>
        <div className="flex justify-end gap-2 px-6 pb-4">
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={creating}>
            {creating ? "Creating..." : "Create"}
          </Button>
        </div>
      </form>
    </Card>
  );
}
