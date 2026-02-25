import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  CustomFieldDefinition,
  CreateCustomFieldRequest,
} from "@/types";

interface CustomFieldState {
  fields: CustomFieldDefinition[];
  isLoading: boolean;

  fetchFields: (projectId: string) => Promise<void>;
  createField: (
    projectId: string,
    req: CreateCustomFieldRequest,
  ) => Promise<CustomFieldDefinition>;
  updateField: (
    fieldId: string,
    req: Partial<CreateCustomFieldRequest>,
  ) => Promise<void>;
  deleteField: (fieldId: string) => Promise<void>;
  reorderFields: (projectId: string, fieldIds: string[]) => Promise<void>;
}

export const useCustomFieldStore = create<CustomFieldState>((set) => ({
  fields: [],
  isLoading: false,

  fetchFields: async (projectId: string) => {
    set({ isLoading: true });
    try {
      const fields = await api<CustomFieldDefinition[]>(
        `/api/v1/projects/${projectId}/custom-fields`,
      );
      set({ fields: fields ?? [], isLoading: false });
    } catch {
      set({ isLoading: false });
    }
  },

  createField: async (
    projectId: string,
    req: CreateCustomFieldRequest,
  ): Promise<CustomFieldDefinition> => {
    const field = await api<CustomFieldDefinition>(
      `/api/v1/projects/${projectId}/custom-fields`,
      { method: "POST", body: req },
    );
    set((state) => ({ fields: [...state.fields, field] }));
    return field;
  },

  updateField: async (
    fieldId: string,
    req: Partial<CreateCustomFieldRequest>,
  ) => {
    const updated = await api<CustomFieldDefinition>(
      `/api/v1/custom-fields/${fieldId}`,
      { method: "PATCH", body: req },
    );
    set((state) => ({
      fields: state.fields.map((f) => (f.id === fieldId ? updated : f)),
    }));
  },

  deleteField: async (fieldId: string) => {
    await api(`/api/v1/custom-fields/${fieldId}`, { method: "DELETE" });
    set((state) => ({
      fields: state.fields.filter((f) => f.id !== fieldId),
    }));
  },

  reorderFields: async (projectId: string, fieldIds: string[]) => {
    const fields = await api<CustomFieldDefinition[]>(
      `/api/v1/projects/${projectId}/custom-fields/reorder`,
      { method: "PUT", body: { field_ids: fieldIds } },
    );
    set({ fields });
  },
}));
