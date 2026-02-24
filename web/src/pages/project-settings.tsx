import { type FormEvent, useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  ArrowDown,
  ArrowUp,
  Eye,
  GripVertical,
  Pencil,
  Plus,
  Settings,
  Trash2,
} from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useCustomFieldStore } from "@/stores/custom-field";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { StatusDialog } from "@/components/status-dialog";
import { CustomFieldDialog } from "@/components/custom-field-dialog";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { statusCategoryConfig } from "@/lib/utils";
import { cn } from "@/lib/cn";
import type { CustomFieldDefinition, TaskStatus } from "@/types";

const fieldTypeLabels: Record<string, string> = {
  text: "Text",
  number: "Number",
  date: "Date",
  datetime: "Date & Time",
  select: "Select",
  multiselect: "Multi-Select",
  url: "URL",
  email: "Email",
  checkbox: "Checkbox",
  user_ref: "User Ref",
  agent_ref: "Agent Ref",
  json: "JSON",
};

export function ProjectSettingsPage() {
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const {
    currentProject,
    statuses,
    fetchStatuses,
    updateProject,
    deleteProject,
    reorderStatuses,
    deleteStatus,
  } = useProjectStore();

  const {
    fields: customFields,
    fetchFields,
    deleteField,
    reorderFields,
  } = useCustomFieldStore();

  // --- General info form state ---
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [icon, setIcon] = useState("");
  const [isSavingGeneral, setIsSavingGeneral] = useState(false);
  const [generalFeedback, setGeneralFeedback] = useState<{
    type: "success" | "error";
    message: string;
  } | null>(null);

  // --- Status management state ---
  const [statusDialogOpen, setStatusDialogOpen] = useState(false);
  const [editingStatus, setEditingStatus] = useState<TaskStatus | undefined>(
    undefined,
  );
  const [deleteStatusDialogOpen, setDeleteStatusDialogOpen] = useState(false);
  const [statusToDelete, setStatusToDelete] = useState<TaskStatus | null>(null);
  const [isDeletingStatus, setIsDeletingStatus] = useState(false);
  const [statusError, setStatusError] = useState<string | null>(null);

  // --- Custom field management state ---
  const [fieldDialogOpen, setFieldDialogOpen] = useState(false);
  const [editingField, setEditingField] = useState<
    CustomFieldDefinition | undefined
  >(undefined);
  const [deleteFieldDialogOpen, setDeleteFieldDialogOpen] = useState(false);
  const [fieldToDelete, setFieldToDelete] =
    useState<CustomFieldDefinition | null>(null);
  const [isDeletingField, setIsDeletingField] = useState(false);
  const [fieldError, setFieldError] = useState<string | null>(null);

  // --- Danger zone state ---
  const [archiveDialogOpen, setArchiveDialogOpen] = useState(false);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [isArchiving, setIsArchiving] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);

  // Populate general form when project changes
  useEffect(() => {
    if (currentProject) {
      setName(currentProject.name);
      setSlug(currentProject.slug);
      setDescription(currentProject.description || "");
      setIcon(currentProject.icon || "");
    }
  }, [currentProject]);

  // Fetch statuses and custom fields on mount
  useEffect(() => {
    if (currentProject) {
      fetchStatuses(currentProject.id);
      fetchFields(currentProject.id);
    }
  }, [currentProject, fetchStatuses, fetchFields]);

  // --- General info handlers ---
  const handleSaveGeneral = async (e: FormEvent) => {
    e.preventDefault();
    if (!currentProject) return;

    if (!name.trim()) {
      setGeneralFeedback({ type: "error", message: "Name is required" });
      return;
    }

    setIsSavingGeneral(true);
    setGeneralFeedback(null);

    try {
      await updateProject(currentProject.id, {
        name: name.trim(),
        slug: slug.trim(),
        description: description.trim() || undefined,
        icon: icon.trim() || undefined,
      });
      setGeneralFeedback({
        type: "success",
        message: "Project settings saved successfully.",
      });
    } catch (err) {
      setGeneralFeedback({
        type: "error",
        message:
          err instanceof Error ? err.message : "Failed to save settings",
      });
    } finally {
      setIsSavingGeneral(false);
    }
  };

  // --- Status handlers ---
  const sortedStatuses = [...statuses].sort(
    (a, b) => a.position - b.position,
  );

  const handleOpenCreateStatus = () => {
    setEditingStatus(undefined);
    setStatusDialogOpen(true);
  };

  const handleOpenEditStatus = (status: TaskStatus) => {
    setEditingStatus(status);
    setStatusDialogOpen(true);
  };

  const handleCloseStatusDialog = () => {
    setStatusDialogOpen(false);
    setEditingStatus(undefined);
  };

  const handleOpenDeleteStatus = (status: TaskStatus) => {
    setStatusToDelete(status);
    setDeleteStatusDialogOpen(true);
    setStatusError(null);
  };

  const handleConfirmDeleteStatus = async () => {
    if (!currentProject || !statusToDelete) return;

    setIsDeletingStatus(true);
    setStatusError(null);

    try {
      await deleteStatus(currentProject.id, statusToDelete.id);
      setDeleteStatusDialogOpen(false);
      setStatusToDelete(null);
    } catch (err) {
      setStatusError(
        err instanceof Error
          ? err.message
          : "Failed to delete status. It may still have tasks assigned.",
      );
    } finally {
      setIsDeletingStatus(false);
    }
  };

  const handleMoveStatus = useCallback(
    async (index: number, direction: "up" | "down") => {
      if (!currentProject) return;

      const newStatuses = [...sortedStatuses];
      const swapIndex = direction === "up" ? index - 1 : index + 1;
      if (swapIndex < 0 || swapIndex >= newStatuses.length) return;

      const temp = newStatuses[index];
      const swapItem = newStatuses[swapIndex];
      if (!temp || !swapItem) return;

      newStatuses[index] = swapItem;
      newStatuses[swapIndex] = temp;

      const newOrderedIds = newStatuses.map((s) => s.id);
      await reorderStatuses(currentProject.id, newOrderedIds);
    },
    [currentProject, sortedStatuses, reorderStatuses],
  );

  // --- Custom field handlers ---
  const sortedFields = [...customFields].sort(
    (a, b) => a.position - b.position,
  );

  const handleOpenCreateField = () => {
    setEditingField(undefined);
    setFieldDialogOpen(true);
  };

  const handleOpenEditField = (field: CustomFieldDefinition) => {
    setEditingField(field);
    setFieldDialogOpen(true);
  };

  const handleCloseFieldDialog = () => {
    setFieldDialogOpen(false);
    setEditingField(undefined);
    // Refresh fields after dialog closes (in case of create/edit)
    if (currentProject) {
      fetchFields(currentProject.id);
    }
  };

  const handleOpenDeleteField = (field: CustomFieldDefinition) => {
    setFieldToDelete(field);
    setDeleteFieldDialogOpen(true);
    setFieldError(null);
  };

  const handleConfirmDeleteField = async () => {
    if (!fieldToDelete) return;

    setIsDeletingField(true);
    setFieldError(null);

    try {
      await deleteField(fieldToDelete.id);
      setDeleteFieldDialogOpen(false);
      setFieldToDelete(null);
    } catch (err) {
      setFieldError(
        err instanceof Error ? err.message : "Failed to delete custom field.",
      );
    } finally {
      setIsDeletingField(false);
    }
  };

  const handleMoveField = useCallback(
    async (index: number, direction: "up" | "down") => {
      if (!currentProject) return;

      const newFields = [...sortedFields];
      const swapIndex = direction === "up" ? index - 1 : index + 1;
      if (swapIndex < 0 || swapIndex >= newFields.length) return;

      const temp = newFields[index];
      const swapItem = newFields[swapIndex];
      if (!temp || !swapItem) return;

      newFields[index] = swapItem;
      newFields[swapIndex] = temp;

      const newOrderedIds = newFields.map((f) => f.id);
      await reorderFields(currentProject.id, newOrderedIds);
    },
    [currentProject, sortedFields, reorderFields],
  );

  // --- Danger zone handlers ---
  const handleArchiveProject = async () => {
    if (!currentProject) return;

    setIsArchiving(true);
    try {
      await updateProject(currentProject.id, { is_archived: true } as Parameters<typeof updateProject>[1]);
      setArchiveDialogOpen(false);
      navigate(`/w/${wsSlug}`);
    } catch {
      // Error is handled by keeping the dialog open
      setIsArchiving(false);
    }
  };

  const handleDeleteProject = async () => {
    if (!currentProject) return;

    setIsDeleting(true);
    try {
      await deleteProject(currentProject.id);
      setDeleteDialogOpen(false);
      navigate(`/w/${wsSlug}`);
    } catch {
      // Error is handled by keeping the dialog open
      setIsDeleting(false);
    }
  };

  if (!currentProject) {
    return (
      <div className="mx-auto max-w-2xl space-y-6">
        <div className="flex items-center gap-3">
          <Settings className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-bold tracking-tight">
            Project Settings
          </h1>
        </div>
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            Project &quot;{projectSlug}&quot; not found
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div className="flex items-center gap-3">
        <Settings className="h-5 w-5 text-muted-foreground" />
        <h1 className="text-2xl font-bold tracking-tight">Project Settings</h1>
      </div>

      {/* Section 1: General Information */}
      <Card>
        <CardHeader>
          <CardTitle>General Information</CardTitle>
          <CardDescription>
            Basic project information and configuration
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSaveGeneral} className="space-y-4">
            <div className="space-y-1.5">
              <label htmlFor="ps-name" className="text-sm font-medium">
                Name <span className="text-destructive">*</span>
              </label>
              <Input
                id="ps-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Project name"
              />
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ps-slug" className="text-sm font-medium">
                Slug
              </label>
              <Input
                id="ps-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                placeholder="project-slug"
              />
              <p className="text-xs text-muted-foreground">
                Used in URLs. Changing this will affect existing links.
              </p>
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ps-description" className="text-sm font-medium">
                Description
              </label>
              <Textarea
                id="ps-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Optional project description..."
                rows={3}
              />
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ps-icon" className="text-sm font-medium">
                Icon
              </label>
              <Input
                id="ps-icon"
                value={icon}
                onChange={(e) => setIcon(e.target.value)}
                placeholder="Emoji or short text (e.g. \uD83D\uDE80)"
                maxLength={8}
              />
            </div>

            {generalFeedback && (
              <p
                className={cn(
                  "text-sm",
                  generalFeedback.type === "success"
                    ? "text-green-600"
                    : "text-destructive",
                )}
              >
                {generalFeedback.message}
              </p>
            )}

            <div className="flex justify-end">
              <Button type="submit" disabled={isSavingGeneral}>
                {isSavingGeneral ? "Saving..." : "Save Changes"}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      {/* Section 2: Task Statuses */}
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1.5">
              <CardTitle>Task Statuses</CardTitle>
              <CardDescription>
                Configure status columns for your project board
              </CardDescription>
            </div>
            <Button size="sm" onClick={handleOpenCreateStatus}>
              <Plus className="h-4 w-4" />
              Add Status
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {sortedStatuses.length === 0 ? (
            <div className="py-8 text-center">
              <p className="text-sm text-muted-foreground">
                No statuses configured yet. Add your first status to get
                started.
              </p>
            </div>
          ) : (
            <div className="space-y-1">
              {sortedStatuses.map((status, index) => {
                const categoryConf =
                  statusCategoryConfig[status.category];
                return (
                  <div
                    key={status.id}
                    className="flex items-center gap-3 rounded-lg border border-border px-3 py-2.5"
                  >
                    {/* Grip icon (visual only) */}
                    <GripVertical className="h-4 w-4 shrink-0 text-muted-foreground" />

                    {/* Color dot */}
                    <div
                      className="h-3 w-3 shrink-0 rounded-full"
                      style={{ backgroundColor: status.color }}
                    />

                    {/* Name */}
                    <span className="flex-1 text-sm font-medium">
                      {status.name}
                    </span>

                    {/* Category badge */}
                    {categoryConf && (
                      <Badge variant="secondary" className="text-xs">
                        <span
                          className={cn(
                            "mr-1.5 inline-block h-2 w-2 rounded-full",
                            categoryConf.color,
                          )}
                        />
                        {categoryConf.label}
                      </Badge>
                    )}

                    {/* Default badge */}
                    {status.is_default && (
                      <Badge variant="outline" className="text-xs">
                        Default
                      </Badge>
                    )}

                    {/* Move up/down buttons */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      disabled={index === 0}
                      onClick={() => handleMoveStatus(index, "up")}
                      title="Move up"
                    >
                      <ArrowUp className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      disabled={index === sortedStatuses.length - 1}
                      onClick={() => handleMoveStatus(index, "down")}
                      title="Move down"
                    >
                      <ArrowDown className="h-3.5 w-3.5" />
                    </Button>

                    {/* Edit button */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() => handleOpenEditStatus(status)}
                      title="Edit status"
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>

                    {/* Delete button */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-muted-foreground hover:text-destructive"
                      onClick={() => handleOpenDeleteStatus(status)}
                      title="Delete status"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                );
              })}
            </div>
          )}

          {statusError && (
            <p className="mt-3 text-sm text-destructive">{statusError}</p>
          )}
        </CardContent>
      </Card>

      {/* Section 3: Custom Fields */}
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1.5">
              <CardTitle>Custom Fields</CardTitle>
              <CardDescription>
                Define custom data fields for tasks in this project
              </CardDescription>
            </div>
            <Button size="sm" onClick={handleOpenCreateField}>
              <Plus className="h-4 w-4" />
              Add Field
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {sortedFields.length === 0 ? (
            <div className="py-8 text-center">
              <p className="text-sm text-muted-foreground">
                No custom fields configured yet. Add fields to capture
                additional data on tasks.
              </p>
            </div>
          ) : (
            <div className="space-y-1">
              {sortedFields.map((field, index) => (
                <div
                  key={field.id}
                  className="flex items-center gap-3 rounded-lg border border-border px-3 py-2.5"
                >
                  {/* Grip icon (visual only) */}
                  <GripVertical className="h-4 w-4 shrink-0 text-muted-foreground" />

                  {/* Name and description */}
                  <div className="flex-1 min-w-0">
                    <span className="text-sm font-medium">{field.name}</span>
                    {field.description && (
                      <p className="truncate text-xs text-muted-foreground">
                        {field.description}
                      </p>
                    )}
                  </div>

                  {/* Field type badge */}
                  <Badge variant="secondary" className="text-xs">
                    {fieldTypeLabels[field.field_type] || field.field_type}
                  </Badge>

                  {/* Required badge */}
                  {field.is_required && (
                    <Badge variant="outline" className="text-xs">
                      Required
                    </Badge>
                  )}

                  {/* Agent-visible badge */}
                  {field.is_visible_to_agents && (
                    <Badge variant="outline" className="text-xs">
                      <Eye className="mr-1 h-3 w-3" />
                      Agents
                    </Badge>
                  )}

                  {/* Move up/down buttons */}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    disabled={index === 0}
                    onClick={() => handleMoveField(index, "up")}
                    title="Move up"
                  >
                    <ArrowUp className="h-3.5 w-3.5" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    disabled={index === sortedFields.length - 1}
                    onClick={() => handleMoveField(index, "down")}
                    title="Move down"
                  >
                    <ArrowDown className="h-3.5 w-3.5" />
                  </Button>

                  {/* Edit button */}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => handleOpenEditField(field)}
                    title="Edit field"
                  >
                    <Pencil className="h-3.5 w-3.5" />
                  </Button>

                  {/* Delete button */}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive"
                    onClick={() => handleOpenDeleteField(field)}
                    title="Delete field"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              ))}
            </div>
          )}

          {fieldError && (
            <p className="mt-3 text-sm text-destructive">{fieldError}</p>
          )}
        </CardContent>
      </Card>

      {/* Section 4: Danger Zone */}
      <Card className="border-destructive/50">
        <CardHeader>
          <CardTitle className="text-destructive">Danger Zone</CardTitle>
          <CardDescription>
            Irreversible actions for this project
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-center justify-between rounded-lg border border-border p-4">
            <div>
              <p className="text-sm font-medium">Archive Project</p>
              <p className="text-sm text-muted-foreground">
                Archive this project. It will be hidden from the sidebar and
                board.
              </p>
            </div>
            <Button
              variant="outline"
              className="border-destructive/50 text-destructive hover:bg-destructive/10"
              onClick={() => setArchiveDialogOpen(true)}
            >
              Archive
            </Button>
          </div>

          <Separator />

          <div className="flex items-center justify-between rounded-lg border border-destructive/30 p-4">
            <div>
              <p className="text-sm font-medium">Delete Project</p>
              <p className="text-sm text-muted-foreground">
                Permanently delete this project and all its data. This action
                cannot be undone.
              </p>
            </div>
            <Button
              variant="destructive"
              onClick={() => setDeleteDialogOpen(true)}
            >
              Delete
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* --- Dialogs --- */}

      {/* Status Create/Edit Dialog */}
      <StatusDialog
        open={statusDialogOpen}
        onClose={handleCloseStatusDialog}
        projectId={currentProject.id}
        status={editingStatus}
      />

      {/* Custom Field Create/Edit Dialog */}
      <CustomFieldDialog
        open={fieldDialogOpen}
        onClose={handleCloseFieldDialog}
        projectId={currentProject.id}
        field={editingField}
      />

      {/* Delete Status Confirmation */}
      <ConfirmDialog
        open={deleteStatusDialogOpen}
        onClose={() => {
          setDeleteStatusDialogOpen(false);
          setStatusToDelete(null);
        }}
        onConfirm={handleConfirmDeleteStatus}
        title="Delete Status"
        description={`Are you sure you want to delete the "${statusToDelete?.name ?? ""}" status? This will fail if any tasks are currently using this status.`}
        confirmText="Delete Status"
        variant="destructive"
        isLoading={isDeletingStatus}
      />

      {/* Delete Custom Field Confirmation */}
      <ConfirmDialog
        open={deleteFieldDialogOpen}
        onClose={() => {
          setDeleteFieldDialogOpen(false);
          setFieldToDelete(null);
        }}
        onConfirm={handleConfirmDeleteField}
        title="Delete Custom Field"
        description={`Are you sure you want to delete the "${fieldToDelete?.name ?? ""}" custom field? Any values stored in this field on tasks will be lost.`}
        confirmText="Delete Field"
        variant="destructive"
        isLoading={isDeletingField}
      />

      {/* Archive Project Confirmation */}
      <ConfirmDialog
        open={archiveDialogOpen}
        onClose={() => setArchiveDialogOpen(false)}
        onConfirm={handleArchiveProject}
        title="Archive Project"
        description={`Are you sure you want to archive "${currentProject.name}"? The project will be hidden but can be restored later.`}
        confirmText="Archive Project"
        variant="destructive"
        isLoading={isArchiving}
      />

      {/* Delete Project Confirmation */}
      <ConfirmDialog
        open={deleteDialogOpen}
        onClose={() => setDeleteDialogOpen(false)}
        onConfirm={handleDeleteProject}
        title="Delete Project"
        description={`This will permanently delete "${currentProject.name}" and all of its tasks, statuses, and data. This action cannot be undone.`}
        confirmText="Delete Project"
        variant="destructive"
        requireText={currentProject.name}
        isLoading={isDeleting}
      />
    </div>
  );
}
