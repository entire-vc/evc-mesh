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
      toast("View saved successfully");
      setSaveDialogOpen(false);
      setSaveName("");
      setSaveIsShared(false);
    } catch {
      toast("Failed to save view");
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
  ]);

  const handleToggleShare = useCallback(
    async (view: SavedView) => {
      try {
        await updateView(view.id, { is_shared: !view.is_shared });
        toast(view.is_shared ? "View made private" : "View shared with team");
      } catch {
        toast("Failed to update view");
      }
    },
    [updateView],
  );

  const handleDelete = useCallback(
    async (view: SavedView) => {
      try {
        await deleteView(view.id);
        toast("View deleted");
      } catch {
        toast("Failed to delete view");
      }
    },
    [deleteView],
  );

  const handleApply = useCallback(
    (view: SavedView) => {
      if (onApplyView) {
        onApplyView(view);
        toast(`Applied view: ${view.name}`);
      }
    },
    [onApplyView],
  );

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" size="sm" className="h-8 gap-1 px-2.5" title="Saved Views">
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
            Save current view
          </DropdownMenuItem>

          {(personalViews.length > 0 || sharedViews.length > 0) && (
            <DropdownMenuSeparator />
          )}

          {personalViews.length > 0 && (
            <>
              <DropdownMenuLabel className="text-xs text-muted-foreground">
                My views
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
                Shared views
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
              No saved views yet
            </div>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={saveDialogOpen} onOpenChange={setSaveDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Save current view</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 p-4">
            <div className="space-y-1.5">
              <label className="text-sm font-medium">View name</label>
              <Input
                value={saveName}
                onChange={(e) => setSaveName(e.target.value)}
                placeholder="e.g. My open tasks"
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
              <span className="text-sm">Share with team members</span>
            </label>

            <div className="flex justify-end gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setSaveDialogOpen(false)}
              >
                Cancel
              </Button>
              <Button
                size="sm"
                onClick={handleSave}
                disabled={!saveName.trim() || isSaving}
              >
                {isSaving ? "Saving..." : "Save view"}
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
          title={view.is_shared ? "Make private" : "Share with team"}
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
          title="Delete view"
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
