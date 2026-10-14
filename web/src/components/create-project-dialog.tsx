import { type FormEvent, useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router";
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
import { Textarea } from "@/components/ui/textarea";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { apiErrorMessage } from "@/lib/api-error";

interface CreateProjectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CreateProjectDialog({
  open,
  onOpenChange,
}: CreateProjectDialogProps) {
  const { t } = useTranslation("settings");
  const navigate = useNavigate();
  const { createProject } = useProjectStore();
  const { currentWorkspace } = useWorkspaceStore();

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const resetForm = () => {
    setName("");
    setSlug("");
    setDescription("");
    setError(null);
  };

  useEffect(() => {
    if (open) {
      resetForm();
    }
  }, [open]);

  const handleNameChange = useCallback((value: string) => {
    setName(value);
    setSlug(
      value
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, ""),
    );
  }, []);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim() || !slug.trim()) {
      setError(t("createProject.nameSlugRequired"));
      return;
    }
    if (!currentWorkspace) {
      setError(t("createProject.noWorkspace"));
      return;
    }

    setIsSubmitting(true);
    setError(null);

    try {
      const project = await createProject(currentWorkspace.id, {
        name: name.trim(),
        slug: slug.trim(),
        description: description.trim() || undefined,
      });
      resetForm();
      onOpenChange(false);
      void navigate(`/w/${currentWorkspace.slug}/p/${project.slug}`);
    } catch (err) {
      setError(apiErrorMessage(err, t("createProject.failed")));
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent onClose={() => onOpenChange(false)}>
        <DialogHeader>
          <DialogTitle>{t("createProject.title")}</DialogTitle>
          <DialogDescription>
            {t("createProject.description", { workspace: currentWorkspace?.name ?? t("createProject.yourWorkspace") })}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          <div className="space-y-1.5">
            <label htmlFor="cp-name" className="text-sm font-medium">
              {t("createProject.name")} <span className="text-destructive">*</span>
            </label>
            <Input
              id="cp-name"
              placeholder={t("createProject.namePlaceholder")}
              value={name}
              onChange={(e) => handleNameChange(e.target.value)}
              autoFocus
              required
            />
          </div>

          <div className="space-y-1.5">
            <label htmlFor="cp-slug" className="text-sm font-medium">
              {t("createProject.slug")} <span className="text-destructive">*</span>
            </label>
            <Input
              id="cp-slug"
              placeholder="my-project"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              required
            />
          </div>

          <div className="space-y-1.5">
            <label htmlFor="cp-desc" className="text-sm font-medium">
              {t("createProject.descriptionLabel")}
            </label>
            <Textarea
              id="cp-desc"
              placeholder={t("createProject.descriptionPlaceholder")}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
            />
          </div>

          {error && <p className="text-sm text-destructive">{error}</p>}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={isSubmitting}
            >
              {t("createProject.cancel")}
            </Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting ? t("createProject.creating") : t("createProject.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
