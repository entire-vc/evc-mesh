import { type FormEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
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
import { apiErrorMessage } from "@/lib/api-error";

interface StatusDialogProps {
  open: boolean;
  onClose: () => void;
  projectId: string;
  status?: TaskStatus;
}

const categoryValues = Object.keys(statusCategoryConfig) as StatusCategory[];

export function StatusDialog({
  open,
  onClose,
  projectId,
  status,
}: StatusDialogProps) {
  const { t } = useTranslation("settings");
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
      setError(t("statusDialog.errors.nameRequired"));
      return;
    }
    if (!slug.trim()) {
      setError(t("statusDialog.errors.slugRequired"));
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
        apiErrorMessage(err, t("statusDialog.errors.saveFailed")),
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
          <DialogTitle>{isEdit ? t("statusDialog.title.edit") : t("statusDialog.title.create")}</DialogTitle>
          <DialogDescription>
            {isEdit
              ? t("statusDialog.description.edit")
              : t("statusDialog.description.create")}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          <div className="space-y-1.5">
            <label htmlFor="sd-name" className="text-sm font-medium">
              {t("statusDialog.fields.name")} <span className="text-destructive">*</span>
            </label>
            <Input
              id="sd-name"
              placeholder={t("statusDialog.fields.namePlaceholder")}
              value={name}
              onChange={(e) => handleNameChange(e.target.value)}
              autoFocus
            />
          </div>

          <div className="space-y-1.5">
            <label htmlFor="sd-slug" className="text-sm font-medium">
              {t("statusDialog.fields.slug")} <span className="text-destructive">*</span>
            </label>
            <Input
              id="sd-slug"
              placeholder={t("statusDialog.fields.slugPlaceholder")}
              value={slug}
              onChange={(e) => handleSlugChange(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              {t("statusDialog.fields.slugHint")}
            </p>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="sd-color" className="text-sm font-medium">
              {t("statusDialog.fields.color")}
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
              {t("statusDialog.fields.category")}
            </label>
            <Select
              id="sd-category"
              value={category}
              onChange={(e) => setCategory(e.target.value as StatusCategory)}
            >
              {categoryValues.map((value) => (
                <option key={value} value={value}>
                  {t(`statusDialog.categories.${value}`)}
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
              {t("statusDialog.fields.default")}
            </label>
            {isDefault && (
              <span className="text-xs text-muted-foreground">
                {t("statusDialog.fields.defaultHint")}
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
              {t("statusDialog.cancel")}
            </Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting
                ? t("statusDialog.saving")
                : isEdit
                  ? t("statusDialog.saveChanges")
                  : t("statusDialog.createStatus")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
