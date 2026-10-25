import {
  type ClipboardEvent,
  type DragEvent,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import { FileText, Loader2, Paperclip, Send, Square, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { apiErrorMessage } from "@/lib/api-error";
import { uploadChatAttachment } from "@/lib/chat/api";

const MAX_ROWS_PX = 192;

interface Pending {
  key: string;
  file: File;
  previewUrl: string | null;
  status: "uploading" | "ready" | "error";
  attachmentId?: string;
  error?: string;
}

export interface ChatComposerProps {
  projectId: string;
  threadId: string;
  // A turn is running: the send button turns into Stop.
  busy: boolean;
  disabled?: boolean;
  onSend: (body: string, attachmentIds: string[], files: File[]) => Promise<void> | void;
  onStop: () => void;
  stopping?: boolean;
}

function imageSize(file: File): Promise<{ width: number; height: number } | undefined> {
  if (!file.type.startsWith("image/")) return Promise.resolve(undefined);
  return new Promise((resolve) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    // Dimensions are a nicety: a decoder that never answers must not hold the upload.
    const giveUp = setTimeout(() => done(undefined), 2000);
    const done = (size: { width: number; height: number } | undefined) => {
      clearTimeout(giveUp);
      URL.revokeObjectURL(url);
      resolve(size);
    };
    img.onload = () => done({ width: img.naturalWidth, height: img.naturalHeight });
    img.onerror = () => done(undefined);
    img.src = url;
  });
}

export function ChatComposer({ projectId, threadId, busy, disabled, onSend, onStop, stopping }: ChatComposerProps) {
  const { t } = useTranslation("chat");
  const [text, setText] = useState("");
  const [pending, setPending] = useState<Pending[]>([]);
  const [dragging, setDragging] = useState(false);
  const [sending, setSending] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const seq = useRef(0);

  // The page mounts one composer per thread (key), so a draft, its uploads and a
  // send in flight belong to one thread and a late completion lands on nothing.
  // Previews are revoked when that composer goes away.
  const pendingRef = useRef(pending);
  pendingRef.current = pending;
  useEffect(
    () => () => pendingRef.current.forEach((p) => p.previewUrl && URL.revokeObjectURL(p.previewUrl)),
    [],
  );

  useEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, MAX_ROWS_PX)}px`;
  }, [text]);

  const addFiles = useCallback(
    (files: File[]) => {
      for (const file of files) {
        const key = `f${++seq.current}`;
        const previewUrl = file.type.startsWith("image/") ? URL.createObjectURL(file) : null;
        setPending((cur) => [...cur, { key, file, previewUrl, status: "uploading" }]);
        void (async () => {
          try {
            const id = await uploadChatAttachment(projectId, threadId, file, await imageSize(file));
            setPending((cur) => cur.map((p) => (p.key === key ? { ...p, status: "ready", attachmentId: id } : p)));
          } catch (err) {
            const error = apiErrorMessage(err, t("composer.uploadFailed"));
            setPending((cur) => cur.map((p) => (p.key === key ? { ...p, status: "error", error } : p)));
          }
        })();
      }
    },
    [projectId, threadId, t],
  );

  const remove = (key: string) =>
    setPending((cur) => {
      const gone = cur.find((p) => p.key === key);
      if (gone?.previewUrl) URL.revokeObjectURL(gone.previewUrl);
      return cur.filter((p) => p.key !== key);
    });

  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const files = Array.from(e.clipboardData.files).filter((f) => f.type.startsWith("image/"));
    if (files.length === 0) return;
    // A pasted screenshot has no name of its own ("image.png" for every one).
    e.preventDefault();
    addFiles(
      files.map((f, i) =>
        f.name && f.name !== "image.png"
          ? f
          : new File([f], `screenshot-${Date.now()}-${i + 1}.${f.type.split("/")[1] ?? "png"}`, { type: f.type }),
      ),
    );
  };

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    if (e.dataTransfer.files.length > 0) addFiles(Array.from(e.dataTransfer.files));
  };

  const uploading = pending.some((p) => p.status === "uploading");
  const ready = pending.filter((p) => p.status === "ready");
  // One turn at a time: the API refuses a send while a turn is unfinished (409).
  const canSend = !disabled && !busy && !sending && !uploading && (text.trim() !== "" || ready.length > 0);

  const submit = async () => {
    if (!canSend) return;
    const raw = text;
    const body = text.trim();
    const ids = ready.map((p) => p.attachmentId!);
    const files = ready.map((p) => p.file);
    const sentKeys = new Set(ready.map((p) => p.key));
    setSending(true);
    try {
      await onSend(body, ids, files);
      // Only what was submitted goes: text or files added while the request was in flight stay.
      setText((cur) => (cur.startsWith(raw) ? cur.slice(raw.length) : cur));
      setPending((cur) => {
        cur.forEach((p) => sentKeys.has(p.key) && p.previewUrl && URL.revokeObjectURL(p.previewUrl));
        return cur.filter((p) => !sentKeys.has(p.key));
      });
    } catch {
      // The page reports the failure; the draft stays so nothing is retyped.
    } finally {
      setSending(false);
      area.current?.focus();
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing) return;
    e.preventDefault();
    void submit();
  };

  return (
    <div
      data-testid="chat-composer"
      onDragOver={(e) => {
        e.preventDefault();
        setDragging(true);
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={onDrop}
      className={cn(
        "shrink-0 rounded-lg border bg-card p-2 transition-colors",
        dragging ? "border-primary bg-muted" : "border-border",
      )}
    >
      {pending.length > 0 && (
        <ul className="mb-2 flex flex-wrap gap-2" aria-label={t("composer.attachments")}>
          {pending.map((p) => (
            <li
              key={p.key}
              data-testid="chat-pending-attachment"
              data-status={p.status}
              title={p.error ?? p.file.name}
              className={cn(
                "relative flex h-16 items-center gap-2 rounded-md border bg-muted pr-6",
                p.status === "error" ? "border-destructive" : "border-border",
                p.previewUrl ? "w-auto" : "max-w-[10rem] px-2",
              )}
            >
              {p.previewUrl ? (
                <img src={p.previewUrl} alt={p.file.name} className="h-full w-16 rounded-md object-cover" />
              ) : (
                <>
                  <FileText className="h-5 w-5 shrink-0 text-muted-foreground" aria-hidden />
                  <span className="truncate text-xs">{p.file.name}</span>
                </>
              )}
              {p.status === "uploading" && (
                <Loader2 className="absolute bottom-1 right-1 h-3.5 w-3.5 animate-spin text-muted-foreground" aria-label={t("composer.uploading")} />
              )}
              <button
                type="button"
                onClick={() => remove(p.key)}
                aria-label={t("composer.removeAttachment", { name: p.file.name })}
                className="absolute right-0.5 top-0.5 flex h-5 w-5 items-center justify-center rounded-full bg-background/90 text-muted-foreground hover:text-foreground"
              >
                <X className="h-3 w-3" />
              </button>
              {p.status === "error" && (
                <span className="absolute inset-x-0 -bottom-4 truncate text-[10px] text-destructive">{p.error}</span>
              )}
            </li>
          ))}
        </ul>
      )}
      <div className="flex items-end gap-2">
        <input
          ref={fileInput}
          type="file"
          multiple
          className="hidden"
          data-testid="chat-file-input"
          onChange={(e) => {
            addFiles(Array.from(e.target.files ?? []));
            e.target.value = "";
          }}
        />
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={() => fileInput.current?.click()}
          disabled={disabled}
          aria-label={t("composer.attach")}
          title={t("composer.attach")}
        >
          <Paperclip className="h-4 w-4" />
        </Button>
        <textarea
          ref={area}
          value={text}
          rows={1}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          disabled={disabled}
          placeholder={t("composer.placeholder")}
          aria-label={t("composer.placeholder")}
          data-testid="chat-input"
          className="max-h-48 min-h-9 flex-1 resize-none bg-transparent px-1 py-2 text-sm leading-5 outline-none placeholder:text-muted-foreground disabled:opacity-60"
        />
        {busy && (
          <Button
            type="button"
            variant="outline"
            onClick={onStop}
            disabled={stopping}
            data-testid="chat-stop"
            aria-label={t("composer.stop")}
          >
            <Square className="mr-1.5 h-3.5 w-3.5 fill-current" />
            {t("composer.stop")}
          </Button>
        )}
        {!busy && (
        <Button
            type="button"
            onClick={() => void submit()}
            disabled={!canSend}
            data-testid="chat-send"
            aria-label={t("composer.send")}
          >
            <Send className="mr-1.5 h-3.5 w-3.5" />
            <span className="hidden sm:inline">{t("composer.send")}</span>
          </Button>
        )}
      </div>
      <p className="mt-1 hidden px-1 text-[11px] text-muted-foreground sm:block">{t("composer.hint")}</p>
    </div>
  );
}
