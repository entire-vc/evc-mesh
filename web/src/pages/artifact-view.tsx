import * as i18nFmt from "@/i18n/format";
import { useEffect, useMemo, useState } from "react";
import { Link, Navigate, useLocation, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { AlertCircle, Download, Loader2, RotateCcw } from "lucide-react";
import { api } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { MarkdownView } from "@/components/markdown-view";
import { Button } from "@/components/ui/button";
import { formatBytes } from "@/lib/utils";
import { downloadArtifact } from "@/lib/task-artifacts";
import {
  PREVIEW_MAX_CHARS,
  baseMimeType,
  previewKindFor,
  truncateForPreview,
  textViewFor,
} from "@/lib/artifact-preview";
import type { Artifact } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";

/**
 * An artifact's own page: `/a/<artifact_id>`.
 *
 * This is where "Open in new tab" and "Preview" land since the Team Relay
 * detour was removed: `metadata.tr_public_url` used to win over
 * everything and sent the reader to docs.entire.vc, where a private share
 * answers 401 and a public one shows Team Relay's chrome instead of ours.
 * The bytes are ours — the artifact lives in our storage — so Mesh renders
 * them itself, under the reader's own session, and TR's page stops being the
 * arbiter of who may read our files. When a "published to Team Relay" flag
 * arrives from the backend, a separate button will offer that page for
 * confirmed publications; until then nothing here references TR at all.
 *
 * The rendering is the preview modal's, promoted to a page (that modal was
 * deleted with this route's arrival): markdown through MarkdownView, text in
 * a `<pre>`, JSON pretty-printed, CSV as a table, and the browser-native
 * kinds (pdf/images) embedded inline through the presigned URL — the same
 * `?disposition=inline` download endpoint the modal used.
 */

type LoadState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; artifact: Artifact };

/** The presigned URL for browser-native kinds — nothing is fetched as text. */
type NativeState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; url: string };

type TextState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; text: string; truncated: boolean; omittedChars: number };

/** Rows shown before the CSV table stops and says so. DOM, not transfer. */
const MAX_CSV_ROWS = 1000;

export function ArtifactViewPage() {
  const { t } = useTranslation("integrations");
  const { artifactId } = useParams<{ artifactId: string }>();
  const { isAuthenticated } = useAuthStore();
  const location = useLocation();
  const [meta, setMeta] = useState<LoadState>({ status: "loading" });
  // Bumped by Retry. Retrying must re-run the whole effect — including
  // minting a *new* presigned URL — because the most likely reason a load
  // failed is that the old URL expired (same reasoning as the modal before it).
  const [attempt, setAttempt] = useState(0);
  const [downloading, setDownloading] = useState(false);

  useEffect(() => {
    if (!isAuthenticated || !artifactId) return;
    let cancelled = false;
    setMeta({ status: "loading" });
    void (async () => {
      try {
        const artifact = await api<Artifact>(`/api/v1/artifacts/${artifactId}`);
        if (!cancelled) setMeta({ status: "ready", artifact });
      } catch (err) {
        if (cancelled) return;
        setMeta({
          status: "error",
          message: apiErrorMessage(err, t("artifactView.loadFailed")),
        });
      }
    })();
    return () => {
      cancelled = true;
    };
    // `attempt` is deliberately absent: metadata doesn't expire, only the
    // presigned URL does — Retry re-mints below without re-fetching this.
  }, [artifactId, isAuthenticated, t]);

  if (!isAuthenticated) {
    return (
      <Navigate
        to={`/login?redirect=${encodeURIComponent(location.pathname + location.search)}`}
        replace
      />
    );
  }

  if (meta.status === "loading") {
    return (
      <div className="flex items-center gap-2 p-8 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        <span>{t("artifactView.loadingArtifact")}</span>
      </div>
    );
  }

  if (meta.status === "error") {
    return (
      <div className="mx-auto max-w-md p-8 text-center">
        <h1 className="text-lg font-semibold">{t("artifactView.notFound.title")}</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          {t("artifactView.notFound.hint")}
        </p>
        <p className="mt-1 text-xs text-muted-foreground">{meta.message}</p>
      </div>
    );
  }

  const artifact = meta.artifact;
  const kind = previewKindFor(artifact);

  const handleDownload = async () => {
    setDownloading(true);
    try {
      await downloadArtifact(artifact.id, artifact.name);
    } catch {
      // Errors already surface through the api layer's toasts.
    } finally {
      setDownloading(false);
    }
  };

  return (
    <div className="mx-auto max-w-4xl space-y-4 px-4 py-6">
      <header className="space-y-1">
        <h1 className="truncate text-lg font-semibold">{artifact.name}</h1>
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span>{formatBytes(artifact.size_bytes)}</span>
          <span>&middot;</span>
          <span className="truncate">{artifact.mime_type}</span>
          <span>&middot;</span>
          <Link
            to={`/t/${artifact.task_id}`}
            className="underline-offset-2 hover:underline"
          >
            {t("artifactView.openTask")}
          </Link>
        </div>
        <div>
          <Button variant="outline" size="sm" onClick={() => void handleDownload()} disabled={downloading}>
            <Download className="mr-1.5 h-3.5 w-3.5" />
            {t("artifactView.download")}
          </Button>
        </div>
      </header>

      {kind === "markdown" || kind === "text" ? (
        <TextView artifact={artifact} attempt={attempt} onRetry={() => setAttempt((n) => n + 1)} />
      ) : kind === "external" ? (
        <NativeView artifact={artifact} attempt={attempt} onRetry={() => setAttempt((n) => n + 1)} />
      ) : (
        <div className="rounded-md border border-border bg-muted/50 px-4 py-6 text-sm text-muted-foreground">
          {t("artifactView.notPreviewable")}
        </div>
      )}
    </div>
  );
}

/**
 * Fetches the bytes as text and renders them through our own pipeline.
 *
 * Why fetched rather than framed: the artifact bucket is proxied on the app's
 * own origin, so an `<iframe>` pointed at an uploaded `text/html` file would
 * run its script against the reader's session. Bytes through `fetch`, out
 * through escaped React nodes — uploaded content stays content (inherited
 * from the modal this page replaces).
 */
function TextView({
  artifact,
  attempt,
  onRetry,
}: {
  artifact: Artifact;
  attempt: number;
  onRetry: () => void;
}) {
  const { t } = useTranslation("integrations");
  const [state, setState] = useState<TextState>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();
    let cancelled = false;
    setState({ status: "loading" });

    void (async () => {
      try {
        const { url } = await api<{ url: string }>(
          `/api/v1/artifacts/${artifact.id}/download?disposition=inline`,
        );
        const resp = await fetch(url, { signal: controller.signal });
        if (!resp.ok) {
          throw new Error(t("artifactView.storageError", { status: resp.status }));
        }
        const body = await resp.text();
        if (cancelled) return;
        const { text, truncated, omittedChars } = truncateForPreview(
          body,
          PREVIEW_MAX_CHARS,
        );
        setState({ status: "ready", text, truncated, omittedChars });
      } catch (err) {
        // An abort is a navigation away, not a failure to report.
        if (cancelled || controller.signal.aborted) return;
        setState({
          status: "error",
          message: apiErrorMessage(err, t("artifactView.fileLoadFailed")),
        });
      }
    })();

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [artifact.id, attempt, t]);

  if (state.status === "loading") {
    return (
      <div className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        <span>{t("artifactView.loadingPreview")}</span>
      </div>
    );
  }

  if (state.status === "error") {
    return (
      <div className="flex flex-col items-start gap-3 py-8">
        <div className="flex items-start gap-2 text-sm text-destructive">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <p className="font-medium">{t("artifactView.previewError")}</p>
            <p className="text-muted-foreground">
              {t("artifactView.linkExpired")} {state.message}
            </p>
          </div>
        </div>
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
          {t("artifactView.tryAgain")}
        </Button>
      </div>
    );
  }

  const view = textViewFor(artifact);

  return (
    <div className="space-y-4">
      {state.truncated && (
        <div className="rounded-md border border-border bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
          {t("artifactView.truncated", {
            size: formatBytes(PREVIEW_MAX_CHARS),
            omitted: i18nFmt.formatNumber(state.omittedChars),
          })}
        </div>
      )}
      {view === "markdown" ? (
        <MarkdownView content={state.text} />
      ) : view === "json" ? (
        <JsonView text={state.text} />
      ) : view === "csv" ? (
        <CsvView text={state.text} />
      ) : (
        <pre className="whitespace-pre-wrap break-words font-mono text-xs text-foreground">
          {state.text}
        </pre>
      )}
    </div>
  );
}

/**
 * PDF, images, audio, video: the browser's own renderers win, so the page
 * embeds the freshly minted presigned URL instead of competing with them.
 * The URL is requested at mount (and on every Retry), never cached — it lives
 * an hour, and a stale one turns the page into a broken frame with no
 * explanation.
 */
function NativeView({
  artifact,
  attempt,
  onRetry,
}: {
  artifact: Artifact;
  attempt: number;
  onRetry: () => void;
}) {
  const { t } = useTranslation("integrations");
  const [state, setState] = useState<NativeState>({ status: "loading" });

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    void (async () => {
      try {
        const { url } = await api<{ url: string }>(
          `/api/v1/artifacts/${artifact.id}/download?disposition=inline`,
        );
        if (!cancelled) setState({ status: "ready", url });
      } catch (err) {
        if (cancelled) return;
        setState({
          status: "error",
          message: apiErrorMessage(err, t("artifactView.fileLoadFailed")),
        });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [artifact.id, attempt, t]);

  if (state.status === "loading") {
    return (
      <div className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        <span>{t("artifactView.loadingFile")}</span>
      </div>
    );
  }

  if (state.status === "error") {
    return (
      <div className="flex flex-col items-start gap-3 py-8">
        <div className="flex items-start gap-2 text-sm text-destructive">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <p className="font-medium">{t("artifactView.fileError")}</p>
            <p className="text-muted-foreground">
              {t("artifactView.linkExpired")} {state.message}
            </p>
          </div>
        </div>
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
          {t("artifactView.tryAgain")}
        </Button>
      </div>
    );
  }

  const mime = baseMimeType(artifact.mime_type);
  if (mime === "application/pdf") {
    return (
      <iframe
        title={artifact.name}
        src={state.url}
        className="h-[80vh] w-full rounded-md border border-border"
      />
    );
  }
  if (mime.startsWith("image/")) {
    return (
      <img
        src={state.url}
        alt={artifact.name}
        className="max-h-[80vh] max-w-full rounded-md border border-border"
      />
    );
  }
  if (mime.startsWith("video/")) {
    return <video controls src={state.url} className="max-w-full rounded-md" />;
  }
  if (mime.startsWith("audio/")) {
    return <audio controls src={state.url} className="w-full" />;
  }
  return (
    <a
      href={state.url}
      target="_blank"
      rel="noopener noreferrer"
      className="text-sm underline underline-offset-2"
    >
      {t("artifactView.openFile")}
    </a>
  );
}

/** Pretty-printed JSON. Unparseable JSON falls back to plain text, not red. */
function JsonView({ text }: { text: string }) {
  const pretty = useMemo(() => {
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      return null;
    }
  }, [text]);

  if (pretty === null) {
    return (
      <pre className="whitespace-pre-wrap break-words font-mono text-xs text-foreground">
        {text}
      </pre>
    );
  }
  return (
    <pre className="overflow-auto rounded-md border border-border bg-muted/30 p-4 font-mono text-xs leading-5">
      <JsonHighlighted text={pretty} />
    </pre>
  );
}

/**
 * JSON syntax colouring without a highlighter dependency. Shiki/Prism would
 * cost more bundle than this page is worth (the entry budget is gated); a
 * JSON tokenizer is one regex and cannot highlight the wrong language.
 */
const JSON_TOKEN =
  /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g;

const JSON_TOKEN_CLASSES = {
  key: "text-tone-sky-700 dark:text-tone-sky-300",
  string: "text-tone-positive-700 dark:text-tone-positive-300",
  number: "text-tone-warning-700 dark:text-tone-warning-300",
  literal: "text-tone-violet-700 dark:text-tone-violet-300",
} as const;

function JsonHighlighted({ text }: { text: string }) {
  const parts: Array<{ text: string; cls?: string }> = [];
  let last = 0;
  for (const m of text.matchAll(JSON_TOKEN)) {
    const at = m.index ?? 0;
    if (at > last) parts.push({ text: text.slice(last, at) });
    if (m[1] !== undefined && m[2] !== undefined) {
      // A string followed by a colon is a key — colour both halves.
      parts.push({ text: m[1], cls: JSON_TOKEN_CLASSES.key });
      parts.push({ text: m[2] });
    } else if (m[1] !== undefined) {
      parts.push({ text: m[1], cls: JSON_TOKEN_CLASSES.string });
    } else if (m[3] !== undefined) {
      parts.push({ text: m[3], cls: JSON_TOKEN_CLASSES.literal });
    } else if (m[4] !== undefined) {
      parts.push({ text: m[4], cls: JSON_TOKEN_CLASSES.number });
    }
    last = at + m[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last) });
  return (
    <>
      {parts.map((p, i) =>
        p.cls ? (
          <span key={i} className={p.cls}>
            {p.text}
          </span>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </>
  );
}

/**
 * Minimal RFC 4180 table: quoted fields, doubled quotes, commas and newlines
 * inside quotes. Not a spreadsheet — no type inference, every cell is the
 * string the file contains.
 */
function parseCsv(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let inQuotes = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]!;
    if (inQuotes) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          inQuotes = false;
        }
      } else {
        field += ch;
      }
      continue;
    }
    if (ch === '"') {
      inQuotes = true;
    } else if (ch === ",") {
      row.push(field);
      field = "";
    } else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && text[i + 1] === "\n") i++;
      row.push(field);
      rows.push(row);
      row = [];
      field = "";
    } else {
      field += ch;
    }
  }
  // A file without a trailing newline loses its last row unless flushed.
  if (field.length > 0 || row.length > 0) {
    row.push(field);
    rows.push(row);
  }
  return rows.filter((r) => !(r.length === 1 && r[0] === ""));
}

function CsvView({ text }: { text: string }) {
  const { t } = useTranslation("integrations");
  const rows = useMemo(() => parseCsv(text), [text]);
  if (rows.length === 0) {
    return (
      <p className="py-4 text-sm text-muted-foreground">{t("artifactView.csvEmpty")}</p>
    );
  }
  const header = rows[0]!;
  const body = rows.slice(1);
  const shown = body.slice(0, MAX_CSV_ROWS);
  return (
    <div className="space-y-2">
      <div className="overflow-auto rounded-md border border-border">
        <table className="w-full text-xs">
          <thead className="sticky top-0 bg-muted">
            <tr>
              {header.map((cell, i) => (
                <th
                  key={i}
                  className="whitespace-nowrap border-b border-border px-3 py-2 text-left font-medium"
                >
                  {cell}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.map((row, r) => (
              <tr key={r} className="border-b border-border/50 last:border-0">
                {row.map((cell, i) => (
                  <td key={i} className="whitespace-nowrap px-3 py-1.5 align-top">
                    {cell}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {body.length > shown.length && (
        <p className="text-xs text-muted-foreground">
          {t("artifactView.csvTruncated", {
            shown: i18nFmt.formatNumber(shown.length),
            total: i18nFmt.formatNumber(body.length),
          })}
        </p>
      )}
    </div>
  );
}
