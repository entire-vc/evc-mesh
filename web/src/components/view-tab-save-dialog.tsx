import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

// Mounted only once someone chooses "Save current view", so the login bundle
// does not carry it.
export default function SaveViewDialog({
  t,
  onOpenChange,
  name,
  onNameChange,
  isShared,
  onSharedChange,
  isSaving,
  onSave,
}: {
  t: (key: string) => string;
  onOpenChange: (open: boolean) => void;
  name: string;
  onNameChange: (name: string) => void;
  isShared: boolean;
  onSharedChange: (shared: boolean) => void;
  isSaving: boolean;
  onSave: () => void;
}) {
  return (
    <Dialog open onOpenChange={onOpenChange}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{t("viewTabs.dialog.title")}</DialogTitle>
            </DialogHeader>
            <div className="space-y-4 p-4">
              <div className="space-y-1.5">
                <label className="text-sm font-medium">{t("viewTabs.dialog.viewName")}</label>
                <Input
                  value={name}
                  onChange={(e) => onNameChange(e.target.value)}
                  placeholder={t("viewTabs.dialog.placeholder")}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && name.trim()) {
                      onSave();
                    }
                  }}
                  autoFocus
                />
              </div>

              <label className="flex cursor-pointer items-center gap-2">
                <input
                  type="checkbox"
                  className="h-4 w-4 rounded border-input"
                  checked={isShared}
                  onChange={(e) => onSharedChange(e.target.checked)}
                />
                <span className="text-sm">{t("viewTabs.dialog.share")}</span>
              </label>

              <div className="flex justify-end gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => onOpenChange(false)}
                >
                  {t("viewTabs.dialog.cancel")}
                </Button>
                <Button
                  size="sm"
                  onClick={() => onSave()}
                  disabled={!name.trim() || isSaving}
                >
                  {isSaving ? t("viewTabs.dialog.saving") : t("viewTabs.dialog.save")}
                </Button>
              </div>
            </div>
          </DialogContent>
        </Dialog>
  );
}
