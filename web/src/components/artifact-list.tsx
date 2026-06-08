import { type DragEvent, useCallback, useEffect, useRef, useState } from "react";
import {
  BookOpen,
  Database,
  Download,
  ExternalLink,
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
import { useProjectTrIntegration } from "@/hooks/useProjectTrIntegration";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { RelayDocPicker } from "@/components/RelayDocPicker";
import { uploadArtifact } from "@/components/markdown-editor";
import type { Artifact, ArtifactType, PaginatedResponse } from "@/types";

interface ArtifactListProps {
  taskId: string;
  /** Increment this counter from parent to trigger a re-fetch */
  refreshKey?: number;
  projId?: string;
  projectSettings?: Record<string, unknown>;
  onRelayDocSelect?: (relayUrl: string) => void;
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

export function ArtifactList({ taskId, refreshKey, projId, onRelayDocSelect }: ArtifactListProps) {
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [downloadingId, setDownloadingId] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const { enabled: hasTrIntegration } = useProjectTrIntegration(projId);

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
      // Copy FileList into array BEFORE resetting input — resetting clears the FileList
      const fileList = Array.from(e.target.files ?? []);
      if (!fileList.length) return;
      e.target.value = "";
      await handleUploadFiles(fileList);
    },
    [handleUploadFiles],
  );

  // Open = for TR private-share artifacts, build the authenticated docs URL
  // directly from tr_public_url + ?agent_key= (both stored in metadata at upload
  // time). For older artifacts without tr_agent_key in metadata, fall back to the
  // preview-url endpoint which resolves the key server-side. For non-TR artifacts,
  // use the S3 presigned URL.
  const handleOpen = async (artifactId: string, trPublicUrl?: string, trAgentKey?: string) => {
    if (trPublicUrl) {
      if (trAgentKey) {
        // Fast path: agent key is in metadata — build the authenticated URL directly.
        const u = new URL(trPublicUrl);
        u.searchParams.set("agent_key", trAgentKey);
        window.open(u.toString(), "_blank");
        return;
      }
      // Fallback for older artifacts (no tr_agent_key) or public shares: try the
      // preview-url endpoint which appends the key server-side.
      if (projId) {
        try {
          const relayUrl =
            "relay://" + new URL(trPublicUrl).pathname.replace(/^\/+/, "");
          const prev = await api<{ available: boolean; iframe_src?: string }>(
            `/api/v1/projects/${projId}/tr/preview-url?relay_url=${encodeURIComponent(relayUrl)}`,
          );
          if (prev.available && prev.iframe_src) {
            window.open(prev.iframe_src, "_blank");
            return;
          }
        } catch {
          // fall through
        }
      }
      // Public share or preview-url unavailable — open bare URL.
      window.open(trPublicUrl, "_blank");
      return;
    }
    try {
      const data = await api<{ url: string }>(
        `/api/v1/artifacts/${artifactId}/download`,
      );
      window.open(data.url, "_blank");
    } catch {
      const baseUrl = import.meta.env.VITE_API_URL || "";
      window.open(`${baseUrl}/api/v1/artifacts/${artifactId}/download`, "_blank");
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
      const baseUrl = import.meta.env.VITE_API_URL || "";
      window.open(`${baseUrl}/api/v1/artifacts/${artifactId}/download`, "_blank");
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
      {hasTrIntegration && onRelayDocSelect && !uploading && (
        <button
          type="button"
          className="mt-1 flex items-center gap-1 text-sm font-medium text-primary hover:underline"
          onClick={() => setPickerOpen(true)}
        >
          <BookOpen className="h-3.5 w-3.5" />
          attach Obsidian doc
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
        {hasTrIntegration && onRelayDocSelect && (
          <RelayDocPicker
            projId={projId!}
            open={pickerOpen}
            onClose={() => setPickerOpen(false)}
            onSelect={(url) => { onRelayDocSelect(url); setPickerOpen(false); }}
          />
        )}
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
                onClick={() =>
                  void handleOpen(
                    artifact.id,
                    typeof artifact.metadata?.tr_public_url === "string"
                      ? artifact.metadata.tr_public_url
                      : undefined,
                    typeof artifact.metadata?.tr_agent_key === "string"
                      ? artifact.metadata.tr_agent_key
                      : undefined,
                  )
                }
                title="Open in new tab"
              >
                <ExternalLink className="h-4 w-4" />
              </Button>
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
      {hasTrIntegration && onRelayDocSelect && (
        <RelayDocPicker
          projId={projId!}
          open={pickerOpen}
          onClose={() => setPickerOpen(false)}
          onSelect={(url) => { onRelayDocSelect(url); setPickerOpen(false); }}
        />
      )}
    </div>
  );
}
