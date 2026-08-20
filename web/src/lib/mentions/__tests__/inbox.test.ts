import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DocumentMention, Mention } from "@/types";

const apiMock = vi.fn();
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => apiMock(...args),
}));

const {
  fetchMentionInbox,
  fetchUnseenMentionCount,
  markMentionSeen,
  mentionHref,
  sortMentionInbox,
  toDocumentMentionItem,
  toTaskMentionItem,
} = await import("@/lib/mentions/inbox");

const TASK_MENTION: Mention = {
  comment_id: "c-task-1",
  mentioned_id: "u1",
  mentioned_kind: "user",
  mentioned_slug: "pavel",
  extracted_at: "2026-08-19T10:00:00Z",
  seen_at: null,
  task_id: "t-1",
  task_title: "Ship the migration",
  project_id: "p-1",
  comment_body: "@pavel take a look",
  author_id: "a-1",
  author_name: "Ann Author",
};

const DOC_MENTION: DocumentMention = {
  comment_id: "c-doc-1",
  mentioned_id: "u1",
  mentioned_kind: "user",
  mentioned_slug: "pavel",
  extracted_at: "2026-08-19T12:00:00Z",
  seen_at: null,
  document_id: "d-1",
  document_title: "Rollback Plan",
  document_slug: "rollback-plan",
  project_id: "p-1",
  comment_body: "@pavel this section",
  author_id: "a-1",
  author_name: "Ann Author",
};

function resolveByPath(map: Record<string, unknown>) {
  return (path: string) => {
    for (const [key, value] of Object.entries(map)) {
      if (path === key) {
        return value instanceof Error ? Promise.reject(value) : Promise.resolve(value);
      }
    }
    return Promise.reject(new Error(`unexpected path ${path}`));
  };
}

beforeEach(() => {
  apiMock.mockReset();
});

describe("fetchMentionInbox", () => {
  it("returns mentions from BOTH inboxes — the bug was reading only one", async () => {
    apiMock.mockImplementation(
      resolveByPath({
        "/api/v1/me/mentions": [TASK_MENTION],
        "/api/v1/me/document-mentions": [DOC_MENTION],
      }),
    );

    const { items, failed } = await fetchMentionInbox();

    expect(failed).toEqual([]);
    expect(items).toHaveLength(2);
    expect(items.map((i) => i.source)).toContain("document");
    expect(items.map((i) => i.title)).toContain("Rollback Plan");
  });

  it("names a source that failed instead of reporting an empty inbox", async () => {
    apiMock.mockImplementation(
      resolveByPath({
        "/api/v1/me/mentions": [TASK_MENTION],
        "/api/v1/me/document-mentions": new Error("500"),
      }),
    );

    const { items, failed } = await fetchMentionInbox();

    // The task mentions that did load are still shown …
    expect(items).toHaveLength(1);
    expect(items[0]?.source).toBe("task");
    // … and the caller is told the list is incomplete, so it cannot render
    // "no mentions yet" as if it had looked everywhere.
    expect(failed).toEqual(["document"]);
  });

  it("keeps task mentions working when the document inbox is missing entirely", async () => {
    apiMock.mockImplementation(
      resolveByPath({
        "/api/v1/me/mentions": [TASK_MENTION],
        "/api/v1/me/document-mentions": new Error("404 not found"),
      }),
    );

    const { items } = await fetchMentionInbox();
    expect(items[0]).toMatchObject({ source: "task", task_id: "t-1" });
  });

  it("reports both sources failed when neither answers", async () => {
    apiMock.mockImplementation(
      resolveByPath({
        "/api/v1/me/mentions": new Error("boom"),
        "/api/v1/me/document-mentions": new Error("boom"),
      }),
    );

    const { items, failed } = await fetchMentionInbox();
    expect(items).toEqual([]);
    expect(failed).toEqual(["task", "document"]);
  });

  it("tolerates a null body from either endpoint", async () => {
    apiMock.mockImplementation(
      resolveByPath({
        "/api/v1/me/mentions": null,
        "/api/v1/me/document-mentions": null,
      }),
    );

    const { items, failed } = await fetchMentionInbox();
    expect(items).toEqual([]);
    expect(failed).toEqual([]);
  });
});

describe("sortMentionInbox", () => {
  it("puts unseen first, then newest first, mixing the two sources by time", () => {
    const oldUnseenTask = toTaskMentionItem({
      ...TASK_MENTION,
      comment_id: "old-unseen",
      extracted_at: "2026-08-01T00:00:00Z",
    });
    const newUnseenDoc = toDocumentMentionItem({
      ...DOC_MENTION,
      comment_id: "new-unseen",
      extracted_at: "2026-08-19T00:00:00Z",
    });
    const seenDoc = toDocumentMentionItem({
      ...DOC_MENTION,
      comment_id: "seen",
      extracted_at: "2026-08-20T00:00:00Z",
      seen_at: "2026-08-20T01:00:00Z",
    });

    const sorted = sortMentionInbox([seenDoc, oldUnseenTask, newUnseenDoc]);

    expect(sorted.map((i) => i.comment_id)).toEqual([
      "new-unseen",
      "old-unseen",
      "seen",
    ]);
  });

  it("does not mutate its argument", () => {
    const items = [toTaskMentionItem(TASK_MENTION), toDocumentMentionItem(DOC_MENTION)];
    const snapshot = [...items];
    sortMentionInbox(items);
    expect(items).toEqual(snapshot);
  });
});

describe("markMentionSeen", () => {
  it("posts to the inbox that owns the row", async () => {
    apiMock.mockResolvedValue(undefined);

    await markMentionSeen(toTaskMentionItem(TASK_MENTION));
    expect(apiMock).toHaveBeenLastCalledWith("/api/v1/me/mentions/c-task-1/seen", {
      method: "POST",
    });

    await markMentionSeen(toDocumentMentionItem(DOC_MENTION));
    expect(apiMock).toHaveBeenLastCalledWith(
      "/api/v1/me/document-mentions/c-doc-1/seen",
      { method: "POST" },
    );
  });
});

describe("mentionHref", () => {
  it("opens the task for a task mention", () => {
    expect(mentionHref(toTaskMentionItem(TASK_MENTION), "acme", "core")).toBe(
      "/w/acme/p/core/t/t-1",
    );
  });

  it("opens the document AND names the comment, so the thread can be focused", () => {
    expect(mentionHref(toDocumentMentionItem(DOC_MENTION), "acme", "core")).toBe(
      "/w/acme/p/core/docs/d-1?comment=c-doc-1",
    );
  });

  it("returns null rather than a guessed route when the project is unknown", () => {
    expect(mentionHref(toDocumentMentionItem(DOC_MENTION), "acme", undefined)).toBeNull();
    expect(mentionHref(toTaskMentionItem(TASK_MENTION), undefined, "core")).toBeNull();
  });
});

describe("fetchUnseenMentionCount", () => {
  it("sums both inboxes so the badge cannot disagree with the tab", async () => {
    apiMock.mockImplementation(
      resolveByPath({
        "/api/v1/me/mentions/unseen_count": { count: 2 },
        "/api/v1/me/document-mentions/unseen_count": { count: 3 },
      }),
    );
    await expect(fetchUnseenMentionCount()).resolves.toBe(5);
  });

  it("still counts what it could read when one endpoint fails", async () => {
    apiMock.mockImplementation(
      resolveByPath({
        "/api/v1/me/mentions/unseen_count": { count: 2 },
        "/api/v1/me/document-mentions/unseen_count": new Error("500"),
      }),
    );
    await expect(fetchUnseenMentionCount()).resolves.toBe(2);
  });
});
