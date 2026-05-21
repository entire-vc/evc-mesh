import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { AtSign, MessageSquare } from "lucide-react";
import { cn } from "@/lib/cn";
import { api } from "@/lib/api";
import { useProjectStore } from "@/stores/project";
import { MarkdownRenderer } from "@/components/markdown-renderer";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import type { Mention } from "@/types";

function formatRelative(isoStr: string): string {
  const diff = Date.now() - new Date(isoStr).getTime();
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return `${Math.floor(diff / 86_400_000)}d ago`;
}

type Tab = "mentions" | "my-comments" | "all-recent";

const TABS: { id: Tab; label: string }[] = [
  { id: "mentions", label: "Mentions" },
  { id: "my-comments", label: "My comments" },
  { id: "all-recent", label: "All recent" },
];

// Grace period after Mentions tab opens before last_visit is bumped — gives the
// user enough time to actually see the «Новое» label on the items they came for.
const LAST_VISIT_DELAY_MS = 5_000;
const lastVisitKey = (ws: string | undefined) => `mesh:activity:last_visit:${ws ?? "global"}`;

export function ActivityPage() {
  const { wsSlug } = useParams();
  const navigate = useNavigate();
  const { projects } = useProjectStore();
  const [activeTab, setActiveTab] = useState<Tab>("mentions");
  const [mentions, setMentions] = useState<Mention[]>([]);
  const [loading, setLoading] = useState(false);
  const [lastVisit, setLastVisit] = useState<Date | null>(null);

  const fetchMentions = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api<Mention[]>("/api/v1/me/mentions", { params: { limit: 50 } });
      const sorted = [...(data ?? [])].sort((a, b) => {
        if (!a.seen_at && b.seen_at) return -1;
        if (a.seen_at && !b.seen_at) return 1;
        return new Date(b.extracted_at).getTime() - new Date(a.extracted_at).getTime();
      });
      setMentions(sorted);
    } catch {
      toast.error("Failed to load mentions");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (activeTab === "mentions") void fetchMentions();
  }, [activeTab, fetchMentions]);

  useEffect(() => {
    if (activeTab !== "mentions") return;
    const key = lastVisitKey(wsSlug);
    const stored = localStorage.getItem(key);
    setLastVisit(stored ? new Date(stored) : null);
    const timer = window.setTimeout(() => {
      localStorage.setItem(key, new Date().toISOString());
    }, LAST_VISIT_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [activeTab, wsSlug]);

  const handleMentionClick = useCallback(
    (mention: Mention) => {
      if (!mention.seen_at) {
        api(`/api/v1/me/mentions/${mention.comment_id}/seen`, { method: "POST" })
          .then(() => {
            setMentions((prev) =>
              prev.map((m) =>
                m.comment_id === mention.comment_id
                  ? { ...m, seen_at: new Date().toISOString() }
                  : m,
              ),
            );
            // invalidate sidebar badge cache so it re-fetches
            localStorage.removeItem("mesh_unseen_ts");
          })
          .catch(() => {});
      }
      const project = projects.find((p) => p.id === mention.project_id);
      if (wsSlug && project) {
        navigate(`/w/${wsSlug}/p/${project.slug}/t/${mention.task_id}`);
      }
    },
    [wsSlug, navigate, projects],
  );

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Activity</h1>

      <div className="flex gap-1 border-b border-border">
        {TABS.map((tab) => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors",
              activeTab === tab.id
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {activeTab === "mentions" && (
        <MentionsTab
          mentions={mentions}
          loading={loading}
          lastVisit={lastVisit}
          onMentionClick={handleMentionClick}
        />
      )}

      {(activeTab === "my-comments" || activeTab === "all-recent") && (
        <div className="flex flex-col items-center justify-center py-16 text-center text-muted-foreground">
          <MessageSquare className="mb-3 h-8 w-8 opacity-30" />
          <p className="text-sm">Backend endpoint pending — flagged in PR.</p>
        </div>
      )}
    </div>
  );
}

function MentionsTab({
  mentions,
  loading,
  lastVisit,
  onMentionClick,
}: {
  mentions: Mention[];
  loading: boolean;
  lastVisit: Date | null;
  onMentionClick: (m: Mention) => void;
}) {
  const { fresh, shown } = useMemo(() => splitByLastVisit(mentions, lastVisit), [mentions, lastVisit]);

  if (loading) {
    return (
      <div className="space-y-3">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-24 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  if (mentions.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-center text-muted-foreground">
        <AtSign className="mb-3 h-8 w-8 opacity-30" />
        <p className="text-sm">
          No mentions yet. When someone tags you with @username, it'll show up here.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {fresh.length > 0 && (
        <section className="space-y-2">
          <SectionHeader label="Новое" count={fresh.length} accent />
          {fresh.map((m) => (
            <MentionCard key={m.comment_id} mention={m} onClick={onMentionClick} />
          ))}
        </section>
      )}

      {shown.length > 0 && (
        <section className="space-y-2">
          {fresh.length > 0 && <SectionHeader label="Показано" count={shown.length} />}
          {shown.map((m) => (
            <MentionCard key={m.comment_id} mention={m} onClick={onMentionClick} dimmed />
          ))}
        </section>
      )}
    </div>
  );
}

function splitByLastVisit(
  mentions: Mention[],
  lastVisit: Date | null,
): { fresh: Mention[]; shown: Mention[] } {
  if (!lastVisit) return { fresh: [], shown: mentions };
  const cutoff = lastVisit.getTime();
  const fresh: Mention[] = [];
  const shown: Mention[] = [];
  for (const m of mentions) {
    if (new Date(m.extracted_at).getTime() > cutoff) fresh.push(m);
    else shown.push(m);
  }
  return { fresh, shown };
}

function SectionHeader({ label, count, accent }: { label: string; count: number; accent?: boolean }) {
  return (
    <div className="flex items-center gap-2 pb-1">
      <span
        className={cn(
          "text-xs font-semibold uppercase tracking-wide",
          accent ? "text-primary" : "text-muted-foreground",
        )}
      >
        {label}
      </span>
      <span className="text-xs text-muted-foreground">({count})</span>
      <div className={cn("h-px flex-1", accent ? "bg-primary/20" : "bg-border")} />
    </div>
  );
}

function MentionCard({
  mention,
  onClick,
  dimmed,
}: {
  mention: Mention;
  onClick: (m: Mention) => void;
  dimmed?: boolean;
}) {
  const isUnseen = !mention.seen_at;
  const hasQuestion = mention.comment_body.includes("❓");
  const preview =
    mention.comment_body.length > 200
      ? mention.comment_body.slice(0, 200) + "…"
      : mention.comment_body;

  return (
    <button
      onClick={() => onClick(mention)}
      className={cn(
        "w-full cursor-pointer rounded-lg border p-3 text-left transition-colors hover:bg-white/[0.02] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-blue-500/40",
        isUnseen && "border-primary/30 bg-primary/5",
        hasQuestion &&
          "border-l-4 border-l-amber-400 bg-amber-50/50 dark:bg-amber-950/20",
        dimmed && "opacity-70",
      )}
    >
      <div className="flex items-start gap-2">
        {isUnseen && (
          <span className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-red-500" />
        )}
        <div className={cn("min-w-0 flex-1", !isUnseen && "pl-4")}>
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate text-sm font-medium">{mention.task_title}</span>
            <span className="shrink-0 text-xs text-muted-foreground">
              {formatRelative(mention.extracted_at)}
            </span>
          </div>
          <p className="mb-1 text-xs text-muted-foreground">{mention.author_name}</p>
          <div className="line-clamp-2 text-xs text-muted-foreground">
            <MarkdownRenderer content={preview} className="text-xs" />
          </div>
        </div>
      </div>
    </button>
  );
}
