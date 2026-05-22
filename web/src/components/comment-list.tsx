import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Bot, Edit2, Lock, Reply, Trash2, User } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { MarkdownRenderer, type MentionEntry } from "@/components/markdown-renderer";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type { ActorType, Comment, CreateCommentRequest, PaginatedResponse } from "@/types";

interface CommentListProps {
  taskId: string;
}

function ActorIcon({ type }: { type: ActorType }) {
  if (type === "agent") {
    return <Bot className="h-4 w-4 text-violet-500" />;
  }
  if (type === "system") {
    return <User className="h-4 w-4 text-muted-foreground" />;
  }
  return <User className="h-4 w-4 text-sky-500" />;
}

function ActorLabel({ type, name }: { type: ActorType; name?: string }) {
  const fallback = type === "agent" ? "Agent" : type === "system" ? "System" : "User";
  const displayName = name?.trim() || fallback;
  return (
    <span className="flex items-center gap-1.5 text-sm font-medium">
      <ActorIcon type={type} />
      {displayName}
    </span>
  );
}

interface CommentItemProps {
  comment: Comment;
  isReply?: boolean;
  replies: Comment[];
  onReply: (parentId: string) => void;
  onSave: (commentId: string, newBody: string) => Promise<void>;
  onDelete: (commentId: string) => void;
  mentionables?: Map<string, MentionEntry>;
  wsSlug?: string;
}

function CommentItem({
  comment,
  isReply,
  replies,
  onReply,
  onSave,
  onDelete,
  mentionables,
  wsSlug,
}: CommentItemProps) {
  const [hovering, setHovering] = useState(false);
  const [isEditing, setIsEditing] = useState(false);
  const [editBody, setEditBody] = useState(comment.body);
  const [saving, setSaving] = useState(false);
  const editRef = useRef<HTMLTextAreaElement>(null);

  const handleEditClick = () => {
    setEditBody(comment.body);
    setIsEditing(true);
    setTimeout(() => editRef.current?.focus(), 0);
  };

  const handleSave = async () => {
    if (!editBody.trim() || saving) return;
    setSaving(true);
    try {
      await onSave(comment.id, editBody.trim());
      setIsEditing(false);
    } finally {
      setSaving(false);
    }
  };

  const handleCancelEdit = () => {
    setIsEditing(false);
    setEditBody(comment.body);
  };

  return (
    <div className={cn("group", isReply && "ml-8 border-l-2 border-border pl-4")}>
      <div
        className="rounded-lg p-3 hover:bg-muted/50"
        onMouseEnter={() => setHovering(true)}
        onMouseLeave={() => setHovering(false)}
      >
        <div className="flex items-start justify-between gap-2">
          <div className="flex items-center gap-2">
            <ActorLabel type={comment.author_type} name={comment.author_name} />
            <span className="text-xs text-muted-foreground">
              {formatRelative(comment.created_at)}
            </span>
            {comment.is_internal && (
              <Badge variant="outline" className="gap-1 text-[10px] text-yellow-600">
                <Lock className="h-2.5 w-2.5" />
                Internal
              </Badge>
            )}
          </div>
          {hovering && !isEditing && (
            <div className="flex items-center gap-1">
              {!isReply && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-6 w-6"
                  onClick={() => onReply(comment.id)}
                  title="Reply"
                >
                  <Reply className="h-3 w-3" />
                </Button>
              )}
              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6"
                onClick={handleEditClick}
                title="Edit"
              >
                <Edit2 className="h-3 w-3" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6 text-destructive"
                onClick={() => onDelete(comment.id)}
                title="Delete"
              >
                <Trash2 className="h-3 w-3" />
              </Button>
            </div>
          )}
        </div>

        {isEditing ? (
          <div className="mt-1.5 space-y-2">
            <Textarea
              ref={editRef}
              value={editBody}
              onChange={(e) => setEditBody(e.target.value)}
              rows={3}
              className="text-sm"
            />
            <div className="flex items-center gap-2 justify-end">
              <Button
                variant="ghost"
                size="sm"
                onClick={handleCancelEdit}
                disabled={saving}
              >
                Cancel
              </Button>
              <Button
                size="sm"
                onClick={handleSave}
                disabled={!editBody.trim() || saving}
              >
                Save
              </Button>
            </div>
          </div>
        ) : (
          <MarkdownRenderer
            content={comment.body}
            className="mt-1.5"
            mentionables={mentionables}
            wsSlug={wsSlug}
          />
        )}
      </div>

      {replies.length > 0 && (
        <div className="mt-1 space-y-1">
          {replies.map((reply) => (
            <CommentItem
              key={reply.id}
              comment={reply}
              isReply
              replies={[]}
              onReply={onReply}
              onSave={onSave}
              onDelete={onDelete}
              mentionables={mentionables}
              wsSlug={wsSlug}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function byNewestFirst(a: Comment, b: Comment): number {
  return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
}

export function CommentList({ taskId }: CommentListProps) {
  const [comments, setComments] = useState<Comment[]>([]);
  const [loading, setLoading] = useState(true);
  const [body, setBody] = useState("");
  const [isInternal, setIsInternal] = useState(false);
  const [replyTo, setReplyTo] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const { currentWorkspace } = useWorkspaceStore();
  const { teamDirectory, fetchTeamDirectory } = useRulesStore();

  useEffect(() => {
    if (currentWorkspace && !teamDirectory) {
      void fetchTeamDirectory(currentWorkspace.id);
    }
  }, [currentWorkspace, teamDirectory, fetchTeamDirectory]);

  const mentionables = useMemo<Map<string, MentionEntry>>(() => {
    const map = new Map<string, MentionEntry>();
    if (!teamDirectory) return map;
    for (const agent of teamDirectory.agents) {
      if (agent.slug) map.set(agent.slug, { kind: "agent" });
    }
    return map;
  }, [teamDirectory]);

  const wsSlug = currentWorkspace?.slug;

  const fetchComments = useCallback(async () => {
    try {
      const data = await api<PaginatedResponse<Comment>>(
        `/api/v1/tasks/${taskId}/comments`,
      );
      setComments(data.items ?? []);
    } catch {
      // silently fail — will show empty list
    } finally {
      setLoading(false);
    }
  }, [taskId]);

  useEffect(() => {
    void fetchComments();
  }, [fetchComments]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!body.trim()) return;
    setSubmitting(true);

    try {
      const req: CreateCommentRequest = {
        body: body.trim(),
        is_internal: isInternal || undefined,
        parent_comment_id: replyTo ?? undefined,
      };
      const created = await api<Comment>(
        `/api/v1/tasks/${taskId}/comments`,
        { method: "POST", body: req },
      );
      setComments((prev) => [created, ...prev]);
      setReplyTo(null);
      setBody("");
      setIsInternal(false);
    } catch {
      // error handled by api layer
    } finally {
      setSubmitting(false);
    }
  };

  const handleSave = async (commentId: string, newBody: string) => {
    const updated = await api<Comment>(
      `/api/v1/comments/${commentId}`,
      { method: "PATCH", body: { body: newBody } },
    );
    setComments((prev) => prev.map((c) => (c.id === updated.id ? updated : c)));
  };

  const handleDelete = async (commentId: string) => {
    try {
      await api(`/api/v1/comments/${commentId}`, { method: "DELETE" });
      setComments((prev) => prev.filter((c) => c.id !== commentId));
    } catch {
      // error handled by api layer
    }
  };

  const handleReply = (parentId: string) => {
    setReplyTo(parentId);
    setBody("");
    textareaRef.current?.focus();
  };

  const handleCancelReply = () => {
    setReplyTo(null);
    setBody("");
  };

  if (loading) {
    return (
      <div className="flex-1 p-4 space-y-3">
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
      </div>
    );
  }

  const topLevel = comments
    .filter((c) => !c.parent_comment_id)
    .sort(byNewestFirst);

  const repliesByParent = comments.reduce<Record<string, Comment[]>>(
    (acc, c) => {
      if (c.parent_comment_id) {
        const existing = acc[c.parent_comment_id];
        if (existing) {
          existing.push(c);
        } else {
          acc[c.parent_comment_id] = [c];
        }
      }
      return acc;
    },
    {},
  );

  // Sort replies newest-first too
  for (const replies of Object.values(repliesByParent)) {
    replies.sort(byNewestFirst);
  }

  const replyToComment = replyTo
    ? comments.find((c) => c.id === replyTo)
    : null;

  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      {/* Scrollable comments list */}
      <div className="flex-1 overflow-y-auto p-4">
        {topLevel.length === 0 && (
          <p className="py-4 text-center text-sm text-muted-foreground">
            No comments yet. Be the first to comment.
          </p>
        )}

        <div className="space-y-1">
          {topLevel.map((comment) => (
            <CommentItem
              key={comment.id}
              comment={comment}
              replies={repliesByParent[comment.id] ?? []}
              onReply={handleReply}
              onSave={handleSave}
              onDelete={handleDelete}
              mentionables={mentionables}
              wsSlug={wsSlug}
            />
          ))}
        </div>
      </div>

      {/* Sticky comment form */}
      <div className="shrink-0 border-t border-border bg-background p-3">
        <form onSubmit={handleSubmit} className="space-y-2">
          {replyToComment && (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <Reply className="h-3 w-3" />
              Replying to{" "}
              {replyToComment.author_name?.trim() || (replyToComment.author_type === "agent" ? "Agent" : replyToComment.author_type === "system" ? "System" : "User")}
              <button
                type="button"
                className="ml-1 text-primary hover:underline"
                onClick={handleCancelReply}
              >
                Cancel
              </button>
            </div>
          )}
          <Textarea
            ref={textareaRef}
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder="Write a comment..."
            rows={3}
          />
          <div className="flex items-center justify-between">
            <label className="flex items-center gap-2 text-sm text-muted-foreground">
              <input
                type="checkbox"
                checked={isInternal}
                onChange={(e) => setIsInternal(e.target.checked)}
                className="rounded border-border"
              />
              <Lock className="h-3.5 w-3.5" />
              Internal note
            </label>
            <Button type="submit" size="sm" disabled={!body.trim() || submitting}>
              Comment
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
