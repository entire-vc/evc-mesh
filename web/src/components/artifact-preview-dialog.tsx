import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Download, Loader2 } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { MarkdownView } from "@/components/markdown-view";
import { formatBytes } from "@/lib/utils";
import {
  PREVIEW_MAX_CHARS,
  previewKindFor,
  truncateForPreview,
} from "@/lib/artifact-preview";
import type { Artifact } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";

interface ArtifactPreviewDialogProps {
  /** The artifact to show, or null when the viewer is closed. */
  artifact: Artifact | null;
  onClose: () => void;
  onDownload: (artifactId: string, name: string) => void;
}

type LoadState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; text: string; truncated: boolean; omittedChars: number };

/**
 * Reading a text artifact without leaving Mesh.
 *
 * ## Why the URL is fetched here and not passed in
 *
 * The download URL is presigned with `X-Amz-Expires=3600`. Handing this
 * component a URL minted when the task page loaded would mean a reader who
 * comes back after lunch opens a viewer that fetches a dead link — and the
 * failure looks like an empty document, not like an expired credential. So the
 * URL is requested at the moment of opening, every time. Nothing is cached.
 *
 * ## Why the bytes are fetched rather than framed
 *
 * The artifact bucket is proxied at `https://mesh.entire.host/s3/…`, the app's
 * own origin. That is convenient — `fetch` needs no CORS — and it is also the
 * reason an `<iframe>` or a new tab is the wrong answer for text: an uploaded
 * `text/html` artifact served from our origin runs its script against the
 * reader's session. Fetching the bytes and rendering them through our own
 * markdown pipeline (which turns raw HTML into escaped text, not markup) keeps
 * uploaded content as content.
 */
export function ArtifactPreviewDialog({
  artifact,
  onClose,
  onDownload,
}: ArtifactPreviewDialogProps) {
  const [state, setState] = useState<LoadState>({ status: "loading" });
  // Bumped by Retry. Retrying has to re-run the whole effect — including
  // minting a *new* presigned URL — because the most likely reason a load
  // failed is that the old URL expired.
  const [attempt, setAttempt] = useState(0);

  const artifactId = artifact?.id;

  useEffect(() => {
    if (!artifactId) return;

    const controller = new AbortController();
    let cancelled = false;

    setState({ status: "loading" });

    void (async () => {
      try {
        const { url } = await api<{ url: string }>(
          `/api/v1/artifacts/${artifactId}/download?disposition=inline`,
        );
        const resp = await fetch(url, { signal: controller.signal });
        if (!resp.ok) {
          throw new Error(`storage returned ${resp.status}`);
        }
        const body = await resp.text();
        if (cancelled) return;
        const { text, truncated, omittedChars } = truncateForPreview(
          body,
          PREVIEW_MAX_CHARS,
        );
        setState({ status: "ready", text, truncated, omittedChars });
      } catch (err) {
        // An abort is this component being closed or re-pointed, not a failure
        // to report. Reporting it would flash an error over a viewer the reader
        // has already dismissed.
        if (cancelled || controller.signal.aborted) return;
        setState({
          status: "error",
          message: apiErrorMessage(err, "could not load file"),
        });
      }
    })();

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [artifactId, attempt]);

  const handleRetry = useCallback(() => setAttempt((n) => n + 1), []);

  if (!artifact) return null;

  const kind = previewKindFor(artifact);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()} className="max-w-4xl">
      <DialogContent
        onClose={onClose}
        data-testid="artifact-preview"
        // No `w-full`: the Dialog wrapper is already full-width, and adding it here
        // makes the box 100% of the viewport PLUS the `mx-4` margins, which
        // overflows the right edge on a phone. Measured at 393px.
        className="flex max-h-[85vh] flex-col p-0"
      >
        <DialogHeader className="shrink-0 border-b border-border px-5 py-4 pr-12 text-left">
          <DialogTitle className="truncate text-base">{artifact.name}</DialogTitle>
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <span>{formatBytes(artifact.size_bytes)}</span>
            <span>&middot;</span>
            <span className="truncate">{artifact.mime_type}</span>
          </div>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-auto px-5 py-4">
          {state.status === "loading" && (
            <div className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              <span>Loading preview...</span>
            </div>
          )}

          {state.status === "error" && (
            <div className="flex flex-col items-start gap-3 py-8">
              <div className="flex items-start gap-2 text-sm text-destructive">
                <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
                <div>
                  <p className="font-medium">Could not load preview</p>
                  <p className="text-muted-foreground">
                    The link may have expired. {state.message}
                  </p>
                </div>
              </div>
              <div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={handleRetry}>
                  Try again
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => onDownload(artifact.id, artifact.name)}
                >
                  <Download className="mr-1.5 h-3.5 w-3.5" />
                  Download instead
                </Button>
              </div>
            </div>
          )}

          {state.status === "ready" && (
            <>
              {state.truncated && (
                <div className="mb-4 rounded-md border border-border bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
                  Showing the first {formatBytes(PREVIEW_MAX_CHARS)} of this file
                  &mdash; {state.omittedChars.toLocaleString()} more characters are
                  not shown. Download it to read the whole thing.
                </div>
              )}
              {kind === "markdown" ? (
                <MarkdownView content={state.text} />
              ) : (
                <pre className="whitespace-pre-wrap break-words font-mono text-xs text-foreground">
                  {state.text}
                </pre>
              )}
            </>
          )}
        </div>

        <div className="flex shrink-0 justify-end gap-2 border-t border-border px-5 py-3">
          <Button
            variant="outline"
            size="sm"
            onClick={() => onDownload(artifact.id, artifact.name)}
          >
            <Download className="mr-1.5 h-3.5 w-3.5" />
            Download
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
