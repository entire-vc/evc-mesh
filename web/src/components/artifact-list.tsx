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
import { uploadArtifact } from "@/lib/task-artifacts";
import { documentMarkdownLink } from "@/lib/docs/doc-link";
import type { DocumentSearchHit } from "@/lib/docs/document-search";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { ArtifactPreviewDialog } from "@/components/artifact-preview-dialog";
import { previewKindFor } from "@/lib/artifact-preview";
import type { Artifact, ArtifactType, PaginatedResponse } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";

interface ArtifactListProps {
  taskId: string;
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

// Deciding how an artifact opens now lives in lib/artifact-preview, because the
// decision has three outcomes rather than two: render it here, hand it to the
// browser, or offer only Download. See that module for why text is no longer in
// the "hand it to the browser" bucket.

export function ArtifactList({ taskId, refreshKey, projId, onDocInsert }: ArtifactListProps) {
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [downloadingId, setDownloadingId] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const [previewing, setPreviewing] = useState<Artifact | null>(null);
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
    try {
      const data = await api<PaginatedResponse<Artifact>>(
        `/api/v1/tasks/${taskId}/artifacts`,
      );
      setArtifacts(data.items ?? []);
    } catch {
      // silently fail - will show empty list
    } finally {
      setLoading(false);
    }
  }, [taskId]);

  useEffect(() => {
    void fetchArtifacts();
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

  // Open = a Team Relay artifact opens in Team Relay; anything else opens through
  // its S3 presigned URL.
  //
  // It used to ask the server to mint a short-lived embed token first and open
  // THAT. The token existed to authenticate an <iframe> we embedded, and the
  // iframe is gone (D10) — a Team Relay document is now read and rendered by our
  // own editor. Minting an embed token to open a new browser tab was the last
  // caller of that machinery, and keeping a credential-minting endpoint alive for
  // it would have left exactly the orphan this unit set out to remove.
  //
  // Named change, not a silent one: on a PRIVATE share the reader previously got
  // an authenticated view via that token and now gets Team Relay's own page,
  // where they sign in as themselves. That is what every other "Open in Team
  // Relay" control in this product already does, and it is the correct party to
  // be deciding whether this person may read that share.
  const handleOpen = async (artifactId: string, trPublicUrl?: string) => {
    if (trPublicUrl) {
      window.open(trPublicUrl, "_blank");
      return;
    }
    try {
      const data = await api<{ url: string }>(
        `/api/v1/artifacts/${artifactId}/download?disposition=inline`,
      );
      window.open(data.url, "_blank");
    } catch {
      // The bare API endpoint requires auth the browser won't send on a plain
      // window.open — it can only ever 401, so don't fall back to it.
      toast("Could not open file — try downloading it instead");
    }
  };

  // Download = force a real file download (fetch blob + anchor download)
  const handleDownload = async (artifactId: string, name: string) => {
    setDownloadingId(artifactId);
    try {
      const data = await api<{ url: string }>(
        `/api/v1/artifacts/${artifactId}/download`,
      );
      const resp = await fetch(data.url);
      const blob = await resp.blob();
      const objUrl = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = objUrl;
      a.download = name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(objUrl);
    } catch {
      // Same reasoning as handleOpen: the bare API endpoint 401s without auth.
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

  if (artifacts.length === 0) {
    return (
      <div className="space-y-3">
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
      {artifacts.map((artifact) => {
        const Icon = artifactTypeIcons[artifact.artifact_type] ?? File;
        const badgeVariant = artifactTypeBadgeVariant[artifact.artifact_type] ?? "secondary";
        const previewKind = previewKindFor(artifact);
        const trPublicUrl =
          typeof artifact.metadata?.tr_public_url === "string"
            ? artifact.metadata.tr_public_url
            : undefined;

        return (
          <div
            key={artifact.id}
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
              {/* A Team Relay artifact keeps opening in Team Relay: the bytes
                  are not ours to render and the share decides who may read
                  them. Checked before the kind, so it wins. */}
              {trPublicUrl ? (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8"
                  onClick={() => void handleOpen(artifact.id, trPublicUrl)}
                  title="Open in new tab"
                >
                  <ExternalLink className="h-4 w-4" />
                </Button>
              ) : previewKind === "markdown" || previewKind === "text" ? (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8"
                  onClick={() => setPreviewing(artifact)}
                  title="Preview"
                >
                  <Eye className="h-4 w-4" />
                </Button>
              ) : previewKind === "external" ? (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8"
                  onClick={() => void handleOpen(artifact.id)}
                  title="Open in new tab"
                >
                  <ExternalLink className="h-4 w-4" />
                </Button>
              ) : null}
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
          </div>
        );
      })}
      {uploadZone}
      <ArtifactPreviewDialog
        artifact={previewing}
        onClose={() => setPreviewing(null)}
        onDownload={(id, name) => void handleDownload(id, name)}
      />
    </div>
  );
}
