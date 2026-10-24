import { useEffect, useState } from "react";
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

interface ConfirmDialogProps {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  description: string;
  confirmText?: string;
  variant?: "default" | "destructive";
  requireText?: string;
  isLoading?: boolean;
  /** Also cancel on Esc and a click on the backdrop. Off by default: other confirms keep their behaviour. */
  dismissible?: boolean;
}

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  description,
  confirmText,
  variant = "default",
  requireText,
  isLoading = false,
  dismissible = false,
}: ConfirmDialogProps) {
  const { t } = useTranslation("settings");
  const [typedText, setTypedText] = useState("");

  const canConfirm = requireText ? typedText === requireText : true;

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      setTypedText("");
      onClose();
    }
  };

  // The shared Dialog has no keyboard handling; Esc must cancel like the Cancel button.
  useEffect(() => {
    if (!open || !dismissible) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") handleOpenChange(false);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  });

  const handleConfirm = () => {
    if (!canConfirm) return;
    onConfirm();
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange} closeOnBackdrop={dismissible}>
      <DialogContent role="dialog" aria-modal="true" onClose={onClose}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        {requireText && (
          <div className="mt-4 space-y-2">
            <p className="text-sm text-muted-foreground">
              {t("confirm.typePrefix")}{" "}
              <span className="font-semibold text-foreground">
                {requireText}
              </span>{" "}
              {t("confirm.typeSuffix")}
            </p>
            <Input
              value={typedText}
              onChange={(e) => setTypedText(e.target.value)}
              placeholder={requireText}
            />
          </div>
        )}

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={onClose}
            disabled={isLoading}
          >
            {t("confirm.cancel")}
          </Button>
          <Button
            type="button"
            variant={variant}
            onClick={handleConfirm}
            disabled={!canConfirm || isLoading}
          >
            {isLoading ? t("confirm.processing") : (confirmText ?? t("confirm.confirm"))}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
