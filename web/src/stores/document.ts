import { create } from "zustand";
import { api } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import type {
  CreateDocumentRequest,
  PaginatedResponse,
  ProjectDocument,
  UpdateDocumentRequest,
} from "@/types";

function getErrorMessage(error: unknown): string {
  return apiErrorMessage(error, "Request failed");
}

// The list endpoint is paginated and the tree needs every node to be drawable —
// a document whose parent landed on page 2 has nowhere to hang. So the store
// walks the pages rather than showing a first page that renders as a broken
// forest. MaxPageSize on the API side is 200; PAGE_LIMIT caps the walk so a
// backend that always answers has_more cannot spin here forever.
const PAGE_SIZE = 200;
const PAGE_LIMIT = 25;

interface DocumentState {
  documents: ProjectDocument[];
  isLoading: boolean;
  error: string | null;

  fetchDocuments: (projectId: string) => Promise<void>;
  /** Single document *with* its markdown body. Deliberately not cached in `documents`. */
  getDocument: (docId: string) => Promise<ProjectDocument>;
  createDocument: (
    projectId: string,
    req: CreateDocumentRequest,
  ) => Promise<ProjectDocument>;
  updateDocument: (
    docId: string,
    req: UpdateDocumentRequest,
  ) => Promise<ProjectDocument>;
  deleteDocument: (docId: string) => Promise<void>;
  reset: () => void;
}

/** ids of `docId` and everything beneath it, for the cascading soft delete. */
function subtreeIds(documents: ProjectDocument[], docId: string): Set<string> {
  const ids = new Set<string>([docId]);
  // Repeat until nothing new is found: children can precede parents in the list,
  // so one pass over an arbitrarily ordered array is not enough.
  let grew = true;
  while (grew) {
    grew = false;
    for (const doc of documents) {
      if (doc.parent_id && ids.has(doc.parent_id) && !ids.has(doc.id)) {
        ids.add(doc.id);
        grew = true;
      }
    }
  }
  return ids;
}

export const useDocumentStore = create<DocumentState>((set, get) => ({
  documents: [],
  isLoading: false,
  error: null,

  fetchDocuments: async (projectId: string) => {
    set({ isLoading: true, error: null });
    try {
      const all: ProjectDocument[] = [];
      for (let page = 1; page <= PAGE_LIMIT; page++) {
        const res = await api<PaginatedResponse<ProjectDocument>>(
          `/api/v1/projects/${projectId}/documents`,
          { params: { page, page_size: PAGE_SIZE } },
        );
        all.push(...(res.items ?? []));
        if (!res.has_more) break;
      }
      set({ documents: all, isLoading: false, error: null });
    } catch (error) {
      set({ documents: [], isLoading: false, error: getErrorMessage(error) });
    }
  },

  getDocument: async (docId: string): Promise<ProjectDocument> => {
    return api<ProjectDocument>(`/api/v1/documents/${docId}`);
  },

  createDocument: async (
    projectId: string,
    req: CreateDocumentRequest,
  ): Promise<ProjectDocument> => {
    const doc = await api<ProjectDocument>(
      `/api/v1/projects/${projectId}/documents`,
      { method: "POST", body: req },
    );
    set((state) => ({ documents: [...state.documents, doc] }));
    return doc;
  },

  updateDocument: async (
    docId: string,
    req: UpdateDocumentRequest,
  ): Promise<ProjectDocument> => {
    const updated = await api<ProjectDocument>(`/api/v1/documents/${docId}`, {
      method: "PATCH",
      body: req,
    });
    set((state) => ({
      documents: state.documents.map((d) => (d.id === docId ? updated : d)),
    }));
    return updated;
  },

  deleteDocument: async (docId: string) => {
    await api(`/api/v1/documents/${docId}`, { method: "DELETE" });
    // The API's soft delete takes the descendants with it, so the local list
    // drops the subtree too — otherwise the children survive as roots on screen
    // and 404 the moment one is opened.
    const doomed = subtreeIds(get().documents, docId);
    set((state) => ({
      documents: state.documents.filter((d) => !doomed.has(d.id)),
    }));
  },

  reset: () => set({ documents: [], isLoading: false, error: null }),
}));
