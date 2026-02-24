import { type FormEvent, useEffect, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { useProjectStore } from "@/stores/project";
import { slugify, statusCategoryConfig } from "@/lib/utils";
import type { StatusCategory, TaskStatus } from "@/types";

interface StatusDialogProps {
  open: boolean;
  onClose: () => void;
  projectId: string;
  status?: TaskStatus;
}

const categoryOptions: { value: StatusCategory; label: string }[] = (
  Object.entries(statusCategoryConfig) as [StatusCategory, { label: string }][]
).map(([value, config]) => ({
  value,
  label: config.label,
}));

export function StatusDialog({
  open,
  onClose,
  projectId,
  status,
}: StatusDialogProps) {
  const { createStatus, updateStatus } = useProjectStore();

  const isEdit = !!status;

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [color, setColor] = useState("#6366f1");
  const [category, setCategory] = useState<StatusCategory>("todo");
  const [isDefault, setIsDefault] = useState(false);
  const [slugManuallyEdited, setSlugManuallyEdited] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Populate form when opening / when status changes
  useEffect(() => {
    if (open) {
      if (status) {
        setName(status.name);
        setSlug(status.slug);
        setColor(status.color);
        setCategory(status.category);
        setIsDefault(status.is_default);
        setSlugManuallyEdited(true);
      } else {
        setName("");
        setSlug("");
        setColor("#6366f1");
        setCategory("todo");
        setIsDefault(false);
        setSlugManuallyEdited(false);
      }
      setError(null);
    }
  }, [open, status]);

  const handleNameChange = (value: string) => {
    setName(value);
    if (!slugManuallyEdited) {
      setSlug(slugify(value));
    }
  };

  const handleSlugChange = (value: string) => {
    setSlugManuallyEdited(true);
    setSlug(slugify(value));
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();

    if (!name.trim()) {
      setError("Name is required");
      return;
    }
    if (!slug.trim()) {
      setError("Slug is required");
      return;
    }

    setIsSubmitting(true);
    setError(null);

    try {
      const req = {
        name: name.trim(),
        slug: slug.trim(),
        color,
        category,
        is_default: isDefault,
      };

      if (isEdit && status) {
        await updateStatus(projectId, status.id, req);
      } else {
        await createStatus(projectId, req);
      }

      onClose();
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to save status",
      );
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      onClose();
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent onClose={onClose}>
        <DialogHeader>
          <DialogTitle>{isEdit ? "Edit Status" : "Create Status"}</DialogTitle>
          <DialogDescription>
            {isEdit
              ? "Update the task status configuration."
              : "Add a new task status to your project."}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          <div className="space-y-1.5">
            <label htmlFor="sd-name" className="text-sm font-medium">
              Name <span className="text-destructive">*</span>
            </label>
            <Input
              id="sd-name"
              placeholder="e.g. In Review"
              value={name}
              onChange={(e) => handleNameChange(e.target.value)}
              autoFocus
            />
          </div>

          <div className="space-y-1.5">
            <label htmlFor="sd-slug" className="text-sm font-medium">
              Slug <span className="text-destructive">*</span>
            </label>
            <Input
              id="sd-slug"
              placeholder="e.g. in-review"
              value={slug}
              onChange={(e) => handleSlugChange(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Auto-generated from name. You can edit it manually.
            </p>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="sd-color" className="text-sm font-medium">
              Color
            </label>
            <div className="flex items-center gap-3">
              <input
                id="sd-color"
                type="color"
                value={color}
                onChange={(e) => setColor(e.target.value)}
                className="h-9 w-12 cursor-pointer rounded-lg border border-input bg-background p-1"
              />
              <Input
                value={color}
                onChange={(e) => setColor(e.target.value)}
                className="w-28 font-mono text-sm"
                maxLength={7}
                placeholder="#000000"
              />
              <div
                className="h-6 w-6 rounded-full border border-border"
                style={{ backgroundColor: color }}
              />
            </div>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="sd-category" className="text-sm font-medium">
              Category
            </label>
            <Select
              id="sd-category"
              value={category}
              onChange={(e) => setCategory(e.target.value as StatusCategory)}
            >
              {categoryOptions.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </Select>
          </div>

          <div className="flex items-center gap-2">
            <input
              id="sd-default"
              type="checkbox"
              checked={isDefault}
              onChange={(e) => setIsDefault(e.target.checked)}
              className="h-4 w-4 rounded border-input"
            />
            <label htmlFor="sd-default" className="text-sm font-medium">
              Default status
            </label>
            {isDefault && (
              <span className="text-xs text-muted-foreground">
                (new tasks will be assigned this status)
              </span>
            )}
          </div>

          {error && <p className="text-sm text-destructive">{error}</p>}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={onClose}
              disabled={isSubmitting}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting
                ? "Saving..."
                : isEdit
                  ? "Save Changes"
                  : "Create Status"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
