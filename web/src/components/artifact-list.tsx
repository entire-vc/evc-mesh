import { useCallback, useEffect, useState } from "react";
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
} from "lucide-react";
import { api } from "@/lib/api";
import { formatBytes, formatRelative } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import type { Artifact, ArtifactType, PaginatedResponse } from "@/types";

interface ArtifactListProps {
  taskId: string;
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

export function ArtifactList({ taskId }: ArtifactListProps) {
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [downloadingId, setDownloadingId] = useState<string | null>(null);

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
  }, [fetchArtifacts]);

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

  if (artifacts.length === 0) {
    return (
      <div className="flex flex-col items-center py-8 text-muted-foreground">
        <Package className="mb-2 h-8 w-8" />
        <p className="text-sm">No artifacts uploaded yet.</p>
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
    </div>
  );
}
