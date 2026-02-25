import {
  type FormEvent,
  useCallback,
  useEffect,
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

function ActorLabel({ type, name, id }: { type: ActorType; name?: string; id: string }) {
  const fallback = type === "agent" ? "Agent" : type === "system" ? "System" : "User";
  const displayName = name || `${fallback} (${id.slice(0, 8)})`;
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
  onEdit: (comment: Comment) => void;
  onDelete: (commentId: string) => void;
}

function CommentItem({
  comment,
  isReply,
  replies,
  onReply,
  onEdit,
  onDelete,
}: CommentItemProps) {
  const [hovering, setHovering] = useState(false);

  return (
    <div className={cn("group", isReply && "ml-8 border-l-2 border-border pl-4")}>
      <div
        className="rounded-lg p-3 hover:bg-muted/50"
        onMouseEnter={() => setHovering(true)}
        onMouseLeave={() => setHovering(false)}
      >
        <div className="flex items-start justify-between gap-2">
          <div className="flex items-center gap-2">
            <ActorLabel type={comment.author_type} name={comment.author_name} id={comment.author_id} />
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
          {hovering && (
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
                onClick={() => onEdit(comment)}
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
        <p className="mt-1.5 whitespace-pre-wrap text-sm">{comment.body}</p>
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
              onEdit={onEdit}
              onDelete={onDelete}
            />
          ))}
        </div>
      )}
    </div>
  );
}

export function CommentList({ taskId }: CommentListProps) {
  const [comments, setComments] = useState<Comment[]>([]);
  const [loading, setLoading] = useState(true);
  const [body, setBody] = useState("");
  const [isInternal, setIsInternal] = useState(false);
  const [replyTo, setReplyTo] = useState<string | null>(null);
  const [editingComment, setEditingComment] = useState<Comment | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

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
      if (editingComment) {
        const updated = await api<Comment>(
          `/api/v1/comments/${editingComment.id}`,
          { method: "PATCH", body: { body: body.trim() } },
        );
        setComments((prev) =>
          prev.map((c) => (c.id === updated.id ? updated : c)),
        );
        setEditingComment(null);
      } else {
        const req: CreateCommentRequest = {
          body: body.trim(),
          is_internal: isInternal || undefined,
          parent_comment_id: replyTo ?? undefined,
        };
        const created = await api<Comment>(
          `/api/v1/tasks/${taskId}/comments`,
          { method: "POST", body: req },
        );
        setComments((prev) => [...prev, created]);
        setReplyTo(null);
      }
      setBody("");
      setIsInternal(false);
    } catch {
      // error handled by api layer
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (commentId: string) => {
    try {
      await api(`/api/v1/comments/${commentId}`, { method: "DELETE" });
      setComments((prev) => prev.filter((c) => c.id !== commentId));
    } catch {
      // error handled by api layer
    }
  };

  const handleEdit = (comment: Comment) => {
    setEditingComment(comment);
    setBody(comment.body);
    setReplyTo(null);
    textareaRef.current?.focus();
  };

  const handleReply = (parentId: string) => {
    setReplyTo(parentId);
    setEditingComment(null);
    setBody("");
    textareaRef.current?.focus();
  };

  const handleCancel = () => {
    setEditingComment(null);
    setReplyTo(null);
    setBody("");
  };

  if (loading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
      </div>
    );
  }

  // Separate top-level comments and replies
  const topLevel = comments.filter((c) => !c.parent_comment_id);
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

  const replyToComment = replyTo
    ? comments.find((c) => c.id === replyTo)
    : null;

  return (
    <div className="space-y-4">
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
            onEdit={handleEdit}
            onDelete={handleDelete}
          />
        ))}
      </div>

      <form onSubmit={handleSubmit} className="space-y-3 border-t border-border pt-4">
        {replyToComment && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Reply className="h-3 w-3" />
            Replying to{" "}
            {replyToComment.author_name || `${replyToComment.author_type} (${replyToComment.author_id.slice(0, 8)})`}
            <button
              type="button"
              className="ml-1 text-primary hover:underline"
              onClick={handleCancel}
            >
              Cancel
            </button>
          </div>
        )}
        {editingComment && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Edit2 className="h-3 w-3" />
            Editing comment
            <button
              type="button"
              className="ml-1 text-primary hover:underline"
              onClick={handleCancel}
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
            {editingComment ? "Save" : "Comment"}
          </Button>
        </div>
      </form>
    </div>
  );
}
