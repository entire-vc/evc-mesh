import { useTranslation } from "react-i18next";
import { useCallback, useEffect, useState } from "react";
import { BookmarkCheck, BookmarkPlus, Share2, Trash2, ChevronDown } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useSavedViewStore } from "@/stores/saved-view-store";
import { toast } from "@/components/ui/toast";
import type { SavedView, ViewType } from "@/types";

interface SavedViewsMenuProps {
  projectId: string;
  currentViewType: ViewType;
  // Current filter/sort state to save
  currentFilters?: Record<string, unknown>;
  currentSortBy?: string;
  currentSortOrder?: string;
  currentColumns?: string[];
  // Callback when user selects a saved view
  onApplyView?: (view: SavedView) => void;
}

export function SavedViewsMenu({
  projectId,
  currentViewType,
  currentFilters = {},
  currentSortBy,
  currentSortOrder,
  currentColumns,
  onApplyView,
}: SavedViewsMenuProps) {
  const { t } = useTranslation("settings");
  const { views, isLoading, fetchViews, createView, updateView, deleteView } =
    useSavedViewStore();

  const [saveDialogOpen, setSaveDialogOpen] = useState(false);
  const [saveName, setSaveName] = useState("");
  const [saveIsShared, setSaveIsShared] = useState(false);
  const [isSaving, setIsSaving] = useState(false);

  useEffect(() => {
    if (projectId) {
      fetchViews(projectId);
    }
  }, [projectId, fetchViews]);

  // Filter views by current view type
  const relevantViews = views.filter((v) => v.view_type === currentViewType);
  const personalViews = relevantViews.filter((v) => !v.is_shared);
  const sharedViews = relevantViews.filter((v) => v.is_shared);

  const handleSave = useCallback(async () => {
    if (!saveName.trim()) return;
    setIsSaving(true);
    try {
      await createView(projectId, {
        name: saveName.trim(),
        view_type: currentViewType,
        filters: currentFilters,
        sort_by: currentSortBy,
        sort_order: currentSortOrder,
        columns: currentColumns,
        is_shared: saveIsShared,
      });
      toast(t("savedViews.toast.saved"));
      setSaveDialogOpen(false);
      setSaveName("");
      setSaveIsShared(false);
    } catch {
      toast(t("savedViews.toast.saveFailed"));
    } finally {
      setIsSaving(false);
    }
  }, [
    projectId,
    saveName,
    saveIsShared,
    currentViewType,
    currentFilters,
    currentSortBy,
    currentSortOrder,
    currentColumns,
    createView,
    t,
  ]);

  const handleToggleShare = useCallback(
    async (view: SavedView) => {
      try {
        await updateView(view.id, { is_shared: !view.is_shared });
        toast(view.is_shared ? t("savedViews.toast.madePrivate") : t("savedViews.toast.sharedWithTeam"));
      } catch {
        toast(t("savedViews.toast.updateFailed"));
      }
    },
    [updateView, t],
  );

  const handleDelete = useCallback(
    async (view: SavedView) => {
      try {
        await deleteView(view.id);
        toast(t("savedViews.toast.deleted"));
      } catch {
        toast(t("savedViews.toast.deleteFailed"));
      }
    },
    [deleteView, t],
  );

  const handleApply = useCallback(
    (view: SavedView) => {
      if (onApplyView) {
        onApplyView(view);
        toast(t("savedViews.toast.applied", { name: view.name }));
      }
    },
    [onApplyView, t],
  );

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" size="sm" className="h-8 gap-1 px-2.5" title={t("savedViews.title")}>
            <BookmarkCheck className="h-4 w-4" />
            <ChevronDown className="h-3 w-3 opacity-60" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-60">
          <DropdownMenuItem
            onClick={() => setSaveDialogOpen(true)}
            className="gap-2"
          >
            <BookmarkPlus className="h-4 w-4" />
            {t("savedViews.saveCurrent")}
          </DropdownMenuItem>

          {(personalViews.length > 0 || sharedViews.length > 0) && (
            <DropdownMenuSeparator />
          )}

          {personalViews.length > 0 && (
            <>
              <DropdownMenuLabel className="text-xs text-muted-foreground">
                {t("savedViews.myViews")}
              </DropdownMenuLabel>
              {personalViews.map((view) => (
                <SavedViewItem
                  key={view.id}
                  view={view}
                  onApply={() => handleApply(view)}
                  onToggleShare={() => handleToggleShare(view)}
                  onDelete={() => handleDelete(view)}
                />
              ))}
            </>
          )}

          {sharedViews.length > 0 && (
            <>
              <DropdownMenuLabel className="text-xs text-muted-foreground">
                {t("savedViews.sharedViews")}
              </DropdownMenuLabel>
              {sharedViews.map((view) => (
                <SavedViewItem
                  key={view.id}
                  view={view}
                  onApply={() => handleApply(view)}
                  onToggleShare={() => handleToggleShare(view)}
                  onDelete={() => handleDelete(view)}
                />
              ))}
            </>
          )}

          {!isLoading && relevantViews.length === 0 && (
            <div className="px-2 py-3 text-center text-xs text-muted-foreground">
              {t("savedViews.noSavedViews")}
            </div>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={saveDialogOpen} onOpenChange={setSaveDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("savedViews.dialog.title")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 p-4">
            <div className="space-y-1.5">
              <label className="text-sm font-medium">{t("savedViews.dialog.viewName")}</label>
              <Input
                value={saveName}
                onChange={(e) => setSaveName(e.target.value)}
                placeholder={t("savedViews.dialog.placeholder")}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && saveName.trim()) {
                    handleSave();
                  }
                }}
                autoFocus
              />
            </div>

            <label className="flex cursor-pointer items-center gap-2">
              <input
                type="checkbox"
                className="h-4 w-4 rounded border-input"
                checked={saveIsShared}
                onChange={(e) => setSaveIsShared(e.target.checked)}
              />
              <span className="text-sm">{t("savedViews.dialog.share")}</span>
            </label>

            <div className="flex justify-end gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setSaveDialogOpen(false)}
              >
                {t("savedViews.dialog.cancel")}
              </Button>
              <Button
                size="sm"
                onClick={handleSave}
                disabled={!saveName.trim() || isSaving}
              >
                {isSaving ? t("savedViews.dialog.saving") : t("savedViews.dialog.save")}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}

// ---------------------------------------------------------------------------
// Single saved view row in the dropdown
// ---------------------------------------------------------------------------

interface SavedViewItemProps {
  view: SavedView;
  onApply: () => void;
  onToggleShare: () => void;
  onDelete: () => void;
}

function SavedViewItem({
  view,
  onApply,
  onToggleShare,
  onDelete,
}: SavedViewItemProps) {
  const { t } = useTranslation("settings");
  return (
    <div className="group flex items-center justify-between rounded px-2 py-1 hover:bg-accent">
      <button
        className="flex-1 truncate text-left text-sm"
        onClick={onApply}
        title={view.name}
      >
        {view.name}
      </button>
      <div className="hidden shrink-0 items-center gap-0.5 group-hover:flex">
        <button
          className="rounded p-0.5 hover:bg-muted"
          title={view.is_shared ? t("savedViews.row.makePrivate") : t("savedViews.row.shareWithTeam")}
          onClick={(e) => {
            e.stopPropagation();
            onToggleShare();
          }}
        >
          <Share2
            className={
              "h-3.5 w-3.5 " +
              (view.is_shared ? "text-primary" : "text-muted-foreground")
            }
          />
        </button>
        <button
          className="rounded p-0.5 hover:bg-muted"
          title={t("savedViews.row.deleteView")}
          onClick={(e) => {
            e.stopPropagation();
            onDelete();
          }}
        >
          <Trash2 className="h-3.5 w-3.5 text-muted-foreground hover:text-destructive" />
        </button>
      </div>
    </div>
  );
}
