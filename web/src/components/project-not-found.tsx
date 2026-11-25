// What a project page shows for a slug the viewer has no project for (missing, or not a member): the board's
// own answer, shared so every project tab ends in the same place instead of a skeleton that never resolves.
export function ProjectNotFound({ slug }: { slug: string | undefined }) {
  return (
    <div className="flex items-center justify-center py-12" data-testid="project-not-found">
      <p className="text-muted-foreground">Project &quot;{slug}&quot; not found</p>
    </div>
  );
}
