import { type DragEvent, useCallback, useEffect, useRef, useState } from "react";
import {
  Database,
  Download,
  ExternalLink,
  Eye,
  File,
  FileCode,
  FileText,
  Image,
  Link,
  Package,
  Trash2,
} from "lucide-react";
import { api } from "@/lib/api";
import { toast } from "@/components/ui/toast";
import { formatBytes, formatRelative } from "@/lib/utils";
import { useProjectTrIntegration } from "@/hooks/useProjectTrIntegration";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { AttachmentSourceMenu } from "@/components/AttachmentSourceMenu";
import { downloadArtifact, uploadArtifact } from "@/lib/task-artifacts";
import { documentMarkdownLink } from "@/lib/docs/doc-link";
import type { DocumentSearchHit } from "@/lib/docs/document-search";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { previewKindFor } from "@/lib/artifact-preview";
import type { Artifact, ArtifactType } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";
import { artifactHref, fetchLinkedEntities } from "@/lib/entity-deep-links";
import { EntityAnchor } from "@/components/entity-anchor";

interface ArtifactListProps {
  taskId: string;
  focusArtifactId?: string | null;
  /** Increment this counter from parent to trigger a re-fetch */
  refreshKey?: number;
  projId?: string;
  projectSettings?: Record<string, unknown>;
  /**
   * A markdown snippet — `[title](/w/.../docs/id)` for one of our own Docs,
   * or a bare `relay://...` URL for a Team Relay document (MarkdownWithRelay
   * recognises both the bare and the `[label](relay://...)` forms) — to
   * insert wherever the caller keeps the task description draft. Named for
   * what it carries, not for which of the two sources produced it: the
   * caller appends either kind identically.
   */
  onDocInsert?: (markdown: string) => void;
}

const artifactTypeIcons: Record<ArtifactType, typeof File> = {
  file: File,
  code: FileCode,
  log: FileText,
  report: FileText,
  link: Link,
  image: Image,
  data: Database,
};

const artifactTypeBadgeVariant: Record<ArtifactType, "default" | "secondary" | "outline"> = {
  file: "secondary",
  code: "outline",
  log: "secondary",
  report: "secondary",
  link: "outline",
  image: "secondary",
  data: "outline",
};

// Deciding how an artifact opens now lives in lib/artifact-preview — here it
// only picks the affordance (an eye for text we render, a tab icon for
// everything else). Both open the artifact's own page, /a/<id>, which does the
// real deciding about how the bytes are shown.

export function ArtifactList({ taskId, refreshKey, projId, onDocInsert, focusArtifactId }: ArtifactListProps) {
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const requestId = useRef(0);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [downloadingId, setDownloadingId] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const { enabled: hasTrIntegration } = useProjectTrIntegration(projId);
  const currentWorkspace = useWorkspaceStore((s) => s.currentWorkspace);
  const projects = useProjectStore((s) => s.projects);
  const currentProject = useProjectStore((s) => s.currentProject);

  const handlePickDoc = useCallback(
    (hit: DocumentSearchHit) => {
      if (!onDocInsert) return;
      const wsSlug = currentWorkspace?.slug;
      const project =
        projects.find((p) => p.id === projId) ??
        (currentProject?.id === projId ? currentProject : undefined);
      if (!wsSlug || !project) return;
      onDocInsert(documentMarkdownLink(hit.title, wsSlug, project.slug, hit.id));
    },
    [onDocInsert, currentWorkspace, projects, currentProject, projId],
  );

  const handlePickRelay = useCallback(
    (hit: DocumentSearchHit) => {
      if (!onDocInsert || !hit.relayUrl) return;
      onDocInsert(hit.relayUrl);
    },
    [onDocInsert],
  );

  const fetchArtifacts = useCallback(async () => {
    const request = ++requestId.current;
    setLoading(true);
    setLoadError(false);
    try {
      const items = await fetchLinkedEntities<Artifact>(
        `/api/v1/tasks/${taskId}/artifacts`,
        focusArtifactId,
      );
      if (request === requestId.current) setArtifacts(items);
    } catch {
      if (request === requestId.current) setLoadError(true);
    } finally {
      if (request === requestId.current) setLoading(false);
    }
  }, [taskId, focusArtifactId]);

  useEffect(() => {
    void fetchArtifacts();
    return () => { requestId.current++; };
  }, [fetchArtifacts, refreshKey]);

  // Upload files via drag-and-drop or file picker
  const handleUploadFiles = useCallback(
    async (files: File[]) => {
      if (!files.length) return;
      setUploading(true);
      try {
        for (const file of files) {
          // Caught per file rather than around the loop: one refused file
          // should not silently abandon the rest of a multi-file drop.
          try {
            const artifact = await uploadArtifact(taskId, file);
            setArtifacts((prev) => [...prev, artifact]);
          } catch (err) {
            toast.error(`Could not attach ${file.name}`, {
              description: apiErrorMessage(err, "upload failed"),
            });
          }
        }
      } finally {
        setUploading(false);
      }
    },
    [taskId],
  );

  const handleDragOver = useCallback((e: DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(true);
  }, []);

  const handleDragLeave = useCallback((e: DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
  }, []);

  const handleDrop = useCallback(
    async (e: DragEvent) => {
      e.preventDefault();
      e.stopPropagation();
      setDragOver(false);
      const files = Array.from(e.dataTransfer.files);
      await handleUploadFiles(files);
    },
    [handleUploadFiles],
  );

  const handleFileInputChange = useCallback(
    async (e: React.ChangeEvent<HTMLInputElement>) => {
      // Copy FileList into array BEFORE resetting input — resetting clears the FileList
      const fileList = Array.from(e.target.files ?? []);
      if (!fileList.length) return;
      e.target.value = "";
      await handleUploadFiles(fileList);
    },
    [handleUploadFiles],
  );

  // Open = the artifact's own page in Mesh, `/a/<id>`, in a new tab. Every
  // artifact, one destination — the reader's session is the only credential
  // involved.
  //
  // It used to branch three ways, and the first one was the bug (#eb6fde4e):
  // `metadata.tr_public_url` won over everything and sent the reader to
  // docs.entire.vc, where a private share answers 401 and even a public one
  // makes Team Relay's page — not ours — decide who reads the file. The bytes
  // are ours; the page renders them. The old "hand the browser the presigned
  // URL" branch is gone for the same reason: a tab of raw JSON or a forced
  // download is a worse answer than a page we control. When a confirmed
  // "published to Team Relay" flag arrives from the backend, a separate
  // button will offer that page — deliberately not this one.
  const handleOpen = (artifactId: string) => {
    window.open(artifactHref(artifactId), "_blank");
  };

  // Download = force a real file download (fetch blob + anchor download)
  const handleDownload = async (artifactId: string, name: string) => {
    setDownloadingId(artifactId);
    try {
      await downloadArtifact(artifactId, name);
    } catch {
      // The bare API endpoint 401s without auth.
      toast("Could not download file");
    } finally {
      setDownloadingId(null);
    }
  };

  const handleDelete = async (artifactId: string) => {
    if (!window.confirm("Are you sure you want to delete this artifact?")) {
      return;
    }
    setDeletingId(artifactId);
    try {
      await api(`/api/v1/artifacts/${artifactId}`, { method: "DELETE" });
      setArtifacts((prev) => prev.filter((a) => a.id !== artifactId));
    } catch {
      // error handled by api layer
    } finally {
      setDeletingId(null);
    }
  };

  if (loading) {
    return (
      <div className="space-y-2">
        <Skeleton className="h-14 w-full" />
        <Skeleton className="h-14 w-full" />
        <Skeleton className="h-14 w-full" />
      </div>
    );
  }

  // Upload zone (shared between empty and populated states). "browse files",
  // "from Docs" and "attach Obsidian doc" used to be three separately-styled
  // controls a writer had to learn one at a time (R6) — one AttachmentSourceMenu
  // now owns all three, including the two AttachDocDialog instances it opens.
  const uploadZone = (
    <div
      className={`flex flex-col items-center rounded-lg border-2 border-dashed px-4 py-6 transition-colors ${
        dragOver
          ? "border-primary bg-primary/5"
          : "border-border hover:border-muted-foreground/50"
      }`}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={(e) => void handleDrop(e)}
    >
      <p className="mb-1 text-sm text-muted-foreground">
        {uploading ? "Uploading..." : "Drop files here, or"}
      </p>
      {!uploading && (
        <AttachmentSourceMenu
          projId={projId}
          hasTrIntegration={hasTrIntegration}
          onPickFiles={() => fileInputRef.current?.click()}
          onPickDoc={handlePickDoc}
          onPickRelay={handlePickRelay}
        />
      )}
      <input
        ref={fileInputRef}
        type="file"
        multiple
        className="hidden"
        onChange={(e) => void handleFileInputChange(e)}
      />
    </div>
  );

  if (loadError) {
    return <div role="alert" className="space-y-2 text-sm">
      <p>Could not load artifacts.</p>
      <Button variant="outline" onClick={() => void fetchArtifacts()}>Retry</Button>
    </div>;
  }

  const notFound = focusArtifactId && !artifacts.some(artifact => artifact.id === focusArtifactId)
    ? <p role="alert" className="py-3 text-sm text-muted-foreground">Artifact not found or you don&apos;t have access.</p> : null;

  if (artifacts.length === 0) {
    return (
      <div className="space-y-3">
        {notFound}
        <div className="flex flex-col items-center py-4 text-muted-foreground">
          <Package className="mb-2 h-8 w-8" />
          <p className="text-sm">No artifacts uploaded yet.</p>
        </div>
        {uploadZone}
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {notFound}
      {artifacts.map((artifact) => {
        const Icon = artifactTypeIcons[artifact.artifact_type] ?? File;
        const badgeVariant = artifactTypeBadgeVariant[artifact.artifact_type] ?? "secondary";
        const previewKind = previewKindFor(artifact);

        return (
          <EntityAnchor
            key={artifact.id}
            id={artifact.id}
            kind="artifact"
            focused={artifact.id === focusArtifactId}
            className="flex items-center justify-between rounded-lg border border-border p-3 transition-colors hover:bg-muted/50"
          >
            <div className="flex min-w-0 flex-1 items-center gap-3">
              <Icon className="h-5 w-5 shrink-0 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium">
                  {artifact.name}
                </p>
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <span>{formatBytes(artifact.size_bytes)}</span>
                  <span>&middot;</span>
                  <span>{formatRelative(artifact.created_at)}</span>
                </div>
              </div>
              <Badge variant={badgeVariant} className="shrink-0 text-[10px]">
                {artifact.artifact_type}
              </Badge>
            </div>

            <div className="ml-3 flex shrink-0 items-center gap-1">
              <a href={artifactHref(artifact.id)} title="Link to artifact" aria-label="Link to artifact"
                className="inline-flex h-8 w-8 items-center justify-center rounded-md hover:bg-muted">
                <Link className="h-4 w-4" />
              </a>
              {/* Text we render ourselves gets the Preview affordance (an eye);
                  everything else gets the tab affordance. Both now lead to the
                  same place — /a/<id> in a new tab — where the page decides
                  how to render. `tr_public_url` is deliberately not consulted
                  here anymore (see handleOpen). */}
              {previewKind === "markdown" || previewKind === "text" ? (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8"
                  onClick={() => handleOpen(artifact.id)}
                  title="Preview"
                >
                  <Eye className="h-4 w-4" />
                </Button>
              ) : (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8"
                  onClick={() => handleOpen(artifact.id)}
                  title="Open in new tab"
                >
                  <ExternalLink className="h-4 w-4" />
                </Button>
              )}
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                onClick={() => void handleDownload(artifact.id, artifact.name)}
                disabled={downloadingId === artifact.id}
                title="Download"
              >
                <Download className="h-4 w-4" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-destructive"
                onClick={() => void handleDelete(artifact.id)}
                disabled={deletingId === artifact.id}
                title="Delete"
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          </EntityAnchor>
        );
      })}
      {uploadZone}
    </div>
  );
}
