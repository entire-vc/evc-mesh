import { type DragEvent, useCallback, useEffect, useRef, useState } from "react";
import {
  Database,
  Download,
  File,
  FileCode,
  FileText,
  Image,
  Link,
  Package,
  Trash2,
  Upload,
} from "lucide-react";
import { api } from "@/lib/api";
import { formatBytes, formatRelative } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { uploadArtifact } from "@/components/markdown-editor";
import type { Artifact, ArtifactType, PaginatedResponse } from "@/types";

interface ArtifactListProps {
  taskId: string;
  /** Increment this counter from parent to trigger a re-fetch */
  refreshKey?: number;
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

export function ArtifactList({ taskId, refreshKey }: ArtifactListProps) {
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [downloadingId, setDownloadingId] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

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
          const artifact = await uploadArtifact(taskId, file);
          setArtifacts((prev) => [...prev, artifact]);
        }
      } catch {
        // error toast could go here
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
      const files = e.target.files;
      if (!files?.length) return;
      e.target.value = "";
      await handleUploadFiles(Array.from(files));
    },
    [handleUploadFiles],
  );

  const handleDownload = async (artifactId: string) => {
    setDownloadingId(artifactId);
    try {
      const data = await api<{ url: string }>(
        `/api/v1/artifacts/${artifactId}/download`,
      );
      window.open(data.url, "_blank");
    } catch {
      // The download endpoint returns a redirect (307), so if the api
      // client follows redirects we might get the file directly.
      // As a fallback, open the endpoint URL directly in a new tab.
      const baseUrl = import.meta.env.VITE_API_URL || "";
      window.open(
        `${baseUrl}/api/v1/artifacts/${artifactId}/download`,
        "_blank",
      );
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

  // Upload zone (shared between empty and populated states)
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
      <Upload className="mb-2 h-6 w-6 text-muted-foreground" />
      <p className="text-sm text-muted-foreground">
        {uploading ? "Uploading..." : "Drop files here or"}
      </p>
      {!uploading && (
        <button
          type="button"
          className="mt-1 text-sm font-medium text-primary hover:underline"
          onClick={() => fileInputRef.current?.click()}
        >
          browse files
        </button>
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
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                onClick={() => void handleDownload(artifact.id)}
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
    </div>
  );
}
