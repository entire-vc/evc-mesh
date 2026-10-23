import { type FormEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Trash2, Plus, Settings2 } from "lucide-react";
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
import { Select } from "@/components/ui/select";
import { useCustomFieldStore } from "@/stores/custom-field";
import { slugifyFieldKey } from "@/lib/utils";
import type { CustomFieldDefinition, FieldType } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";

interface CustomFieldDialogProps {
  open: boolean;
  onClose: () => void;
  projectId: string;
  field?: CustomFieldDefinition;
}

const fieldTypeValues: FieldType[] = [
  "text",
  "number",
  "date",
  "datetime",
  "select",
  "multiselect",
  "url",
  "email",
  "checkbox",
  "user_ref",
  "agent_ref",
  "json",
];

// Represents a single choice with optional label and color.
interface ChoiceEntry {
  value: string;
  label: string;
  color: string;
}

// Detect if existing choices use advanced format (label differs from value, or has color).
function detectAdvancedMode(rawChoices: unknown[]): boolean {
  if (!Array.isArray(rawChoices)) return false;
  return rawChoices.some((item) => {
    if (typeof item !== "object" || item === null) return false;
    const obj = item as Record<string, unknown>;
    const hasDistinctLabel =
      obj.label && obj.value && String(obj.label) !== String(obj.value);
    const hasColor = !!obj.color;
    return hasDistinctLabel || hasColor;
  });
}

// Parse existing choices into ChoiceEntry[], handling both string[] and object[] formats.
function parseChoices(raw: unknown): ChoiceEntry[] {
  if (!Array.isArray(raw) || raw.length === 0) return [{ value: "", label: "", color: "" }];
  return raw.map((item) => {
    if (typeof item === "string") {
      return { value: item, label: item, color: "" };
    }
    if (typeof item === "object" && item !== null) {
      const obj = item as Record<string, unknown>;
      return {
        value: String(obj.value ?? obj.label ?? ""),
        label: String(obj.label ?? obj.value ?? ""),
        color: obj.color ? String(obj.color) : "",
      };
    }
    return { value: String(item), label: String(item), color: "" };
  });
}

export function CustomFieldDialog({
  open,
  onClose,
  projectId,
  field,
}: CustomFieldDialogProps) {
  const { t } = useTranslation("settings");
  const { createField, updateField } = useCustomFieldStore();

  const isEdit = !!field;

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [fieldType, setFieldType] = useState<FieldType>("text");
  const [description, setDescription] = useState("");
  const [isRequired, setIsRequired] = useState(false);
  const [isVisibleToAgents, setIsVisibleToAgents] = useState(false);

  // Options state (dynamic based on field type)
  const [choices, setChoices] = useState<ChoiceEntry[]>([{ value: "", label: "", color: "" }]);
  const [advancedMode, setAdvancedMode] = useState(false);
  const [numberMin, setNumberMin] = useState("");
  const [numberMax, setNumberMax] = useState("");
  const [textRegex, setTextRegex] = useState("");
  const [textMaxLength, setTextMaxLength] = useState("");

  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Populate form when opening / when field changes
  useEffect(() => {
    if (open) {
      if (field) {
        setName(field.name);
        setFieldType(field.field_type);
        setDescription(field.description || "");
        setIsRequired(field.is_required);
        setIsVisibleToAgents(field.is_visible_to_agents);

        // Parse options
        const opts = field.options || {};
        if (
          field.field_type === "select" ||
          field.field_type === "multiselect"
        ) {
          const raw = opts.choices as unknown[] | undefined;
          const parsed = parseChoices(raw);
          setChoices(parsed);
          setAdvancedMode(detectAdvancedMode(raw ?? []));
        }
        if (field.field_type === "number") {
          setNumberMin(opts.min !== undefined ? String(opts.min) : "");
          setNumberMax(opts.max !== undefined ? String(opts.max) : "");
        }
        if (field.field_type === "text") {
          setTextRegex((opts.regex as string) || "");
          setTextMaxLength(
            opts.max_length !== undefined ? String(opts.max_length) : "",
          );
        }
      } else {
        setName("");
        setSlug("");
        setFieldType("text");
        setDescription("");
        setIsRequired(false);
        setIsVisibleToAgents(false);
        setChoices([{ value: "", label: "", color: "" }]);
        setAdvancedMode(false);
        setNumberMin("");
        setNumberMax("");
        setTextRegex("");
        setTextMaxLength("");
      }
      setError(null);
    }
  }, [open, field]);

  const handleNameChange = (value: string) => {
    setName(value);
    if (!isEdit) {
      setSlug(slugifyFieldKey(value));
    }
  };

  const buildOptions = (): Record<string, unknown> | undefined => {
    if (fieldType === "select" || fieldType === "multiselect") {
      const filtered = choices.filter((c) => c.value.trim() !== "");
      if (filtered.length === 0) return undefined;

      // Determine if we need advanced format
      const needsAdvanced = filtered.some(
        (c) => (c.label && c.label !== c.value) || c.color,
      );

      if (needsAdvanced) {
        return {
          choices: filtered.map((c) => ({
            value: c.value.trim(),
            label: (c.label || c.value).trim(),
            ...(c.color ? { color: c.color.trim() } : {}),
          })),
        };
      }
      // Simple string[] format
      return { choices: filtered.map((c) => c.value.trim()) };
    }
    if (fieldType === "number") {
      const opts: Record<string, unknown> = {};
      if (numberMin !== "") opts.min = Number(numberMin);
      if (numberMax !== "") opts.max = Number(numberMax);
      return Object.keys(opts).length > 0 ? opts : undefined;
    }
    if (fieldType === "text") {
      const opts: Record<string, unknown> = {};
      if (textRegex.trim()) opts.regex = textRegex.trim();
      if (textMaxLength !== "") opts.max_length = Number(textMaxLength);
      return Object.keys(opts).length > 0 ? opts : undefined;
    }
    return undefined;
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();

    if (!name.trim()) {
      setError(t("customField.errors.nameRequired"));
      return;
    }

    if (!isEdit && !slug.trim()) {
      setError(t("customField.errors.slugRequired"));
      return;
    }

    if (
      (fieldType === "select" || fieldType === "multiselect") &&
      choices.filter((c) => c.value.trim() !== "").length === 0
    ) {
      setError(t("customField.errors.choiceRequired"));
      return;
    }

    setIsSubmitting(true);
    setError(null);

    try {
      const options = buildOptions();

      if (isEdit && field) {
        await updateField(field.id, {
          name: name.trim(),
          field_type: fieldType,
          description: description.trim() || undefined,
          options,
          is_required: isRequired,
          is_visible_to_agents: isVisibleToAgents,
        });
      } else {
        await createField(projectId, {
          name: name.trim(),
          slug: slug.trim(),
          field_type: fieldType,
          description: description.trim() || undefined,
          options,
          is_required: isRequired,
          is_visible_to_agents: isVisibleToAgents,
        });
      }

      onClose();
    } catch (err) {
      setError(
        apiErrorMessage(err, t("customField.errors.saveFailed")),
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

  const handleAddChoice = () => {
    setChoices((prev) => [...prev, { value: "", label: "", color: "" }]);
  };

  const handleRemoveChoice = (index: number) => {
    setChoices((prev) => prev.filter((_, i) => i !== index));
  };

  const handleChoiceValueChange = (index: number, val: string) => {
    setChoices((prev) =>
      prev.map((c, i) => {
        if (i !== index) return c;
        // In simple mode, keep label in sync with value
        if (!advancedMode) {
          return { ...c, value: val, label: val };
        }
        return { ...c, value: val };
      }),
    );
  };

  const handleChoiceLabelChange = (index: number, label: string) => {
    setChoices((prev) =>
      prev.map((c, i) => (i === index ? { ...c, label } : c)),
    );
  };

  const handleChoiceColorChange = (index: number, color: string) => {
    setChoices((prev) =>
      prev.map((c, i) => (i === index ? { ...c, color } : c)),
    );
  };

  const showChoicesSection =
    fieldType === "select" || fieldType === "multiselect";
  const showNumberSection = fieldType === "number";
  const showTextSection = fieldType === "text";

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent onClose={onClose}>
        <DialogHeader>
          <DialogTitle>
            {isEdit ? t("customField.title.edit") : t("customField.title.create")}
          </DialogTitle>
          <DialogDescription>
            {isEdit
              ? t("customField.description.edit")
              : t("customField.description.create")}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          <div className="space-y-1.5">
            <label htmlFor="cf-name" className="text-sm font-medium">
              {t("customField.fields.name")} <span className="text-destructive">*</span>
            </label>
            <Input
              id="cf-name"
              placeholder={t("customField.fields.namePlaceholder")}
              value={name}
              onChange={(e) => handleNameChange(e.target.value)}
              autoFocus
            />
          </div>

          {!isEdit && (
            <div className="space-y-1.5">
              <label htmlFor="cf-slug" className="text-sm font-medium">
                {t("customField.fields.slug")} <span className="text-destructive">*</span>
              </label>
              <Input
                id="cf-slug"
                placeholder="story_points"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
              />
            </div>
          )}

          <div className="space-y-1.5">
            <label htmlFor="cf-type" className="text-sm font-medium">
              {t("customField.fields.fieldType")} <span className="text-destructive">*</span>
            </label>
            <Select
              id="cf-type"
              value={fieldType}
              onChange={(e) => setFieldType(e.target.value as FieldType)}
            >
              {fieldTypeValues.map((value) => (
                <option key={value} value={value}>
                  {t(`customField.types.${value}`)}
                </option>
              ))}
            </Select>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="cf-description" className="text-sm font-medium">
              {t("customField.fields.description")}
            </label>
            <Textarea
              id="cf-description"
              placeholder={t("customField.fields.descriptionPlaceholder")}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
            />
          </div>

          {/* Dynamic options section based on field type */}

          {showChoicesSection && (
            <div className="space-y-1.5">
              <div className="flex items-center justify-between">
                <label className="text-sm font-medium">{t("customField.choices.title")}</label>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-6 gap-1 px-2 text-xs text-muted-foreground"
                  onClick={() => setAdvancedMode((m) => !m)}
                >
                  <Settings2 className="h-3 w-3" />
                  {advancedMode ? t("customField.choices.simpleMode") : t("customField.choices.advancedMode")}
                </Button>
              </div>
              <div className="space-y-2">
                {choices.map((choice, index) => (
                  <div key={index} className="flex items-center gap-2">
                    <Input
                      placeholder={t("customField.choices.valuePlaceholder", { n: index + 1 })}
                      value={choice.value}
                      onChange={(e) => handleChoiceValueChange(index, e.target.value)}
                      className={advancedMode ? "flex-1" : ""}
                    />
                    {advancedMode && (
                      <>
                        <Input
                          placeholder={t("customField.choices.labelPlaceholder")}
                          value={choice.label !== choice.value ? choice.label : ""}
                          onChange={(e) => handleChoiceLabelChange(index, e.target.value || choice.value)}
                          className="flex-1"
                        />
                        <Input
                          type="color"
                          value={choice.color || "#6b7280" /* color-ok: <input type=color> needs hex */}
                          onChange={(e) => handleChoiceColorChange(index, e.target.value)}
                          className="h-8 w-10 shrink-0 cursor-pointer p-0.5"
                          title={t("customField.choices.colorTitle")}
                        />
                      </>
                    )}
                    {choices.length > 1 && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 shrink-0 text-muted-foreground hover:text-destructive"
                        onClick={() => handleRemoveChoice(index)}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    )}
                  </div>
                ))}
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={handleAddChoice}
                className="mt-1"
              >
                <Plus className="mr-1 h-3.5 w-3.5" />
                {t("customField.choices.add")}
              </Button>
            </div>
          )}

          {showNumberSection && (
            <div className="space-y-1.5">
              <label className="text-sm font-medium">
                {t("customField.number.constraints")}
              </label>
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1">
                  <label htmlFor="cf-min" className="text-xs text-muted-foreground">
                    {t("customField.number.min")}
                  </label>
                  <Input
                    id="cf-min"
                    type="number"
                    placeholder={t("customField.number.min")}
                    value={numberMin}
                    onChange={(e) => setNumberMin(e.target.value)}
                  />
                </div>
                <div className="space-y-1">
                  <label htmlFor="cf-max" className="text-xs text-muted-foreground">
                    {t("customField.number.max")}
                  </label>
                  <Input
                    id="cf-max"
                    type="number"
                    placeholder={t("customField.number.max")}
                    value={numberMax}
                    onChange={(e) => setNumberMax(e.target.value)}
                  />
                </div>
              </div>
            </div>
          )}

          {showTextSection && (
            <div className="space-y-1.5">
              <label className="text-sm font-medium">
                {t("customField.text.constraints")}
              </label>
              <div className="space-y-3">
                <div className="space-y-1">
                  <label
                    htmlFor="cf-regex"
                    className="text-xs text-muted-foreground"
                  >
                    {t("customField.text.regex")}
                  </label>
                  <Input
                    id="cf-regex"
                    placeholder={t("customField.text.regexPlaceholder")}
                    value={textRegex}
                    onChange={(e) => setTextRegex(e.target.value)}
                    className="font-mono text-sm"
                  />
                </div>
                <div className="space-y-1">
                  <label
                    htmlFor="cf-maxlen"
                    className="text-xs text-muted-foreground"
                  >
                    {t("customField.text.maxLength")}
                  </label>
                  <Input
                    id="cf-maxlen"
                    type="number"
                    placeholder={t("customField.text.maxLengthPlaceholder")}
                    value={textMaxLength}
                    onChange={(e) => setTextMaxLength(e.target.value)}
                    min={1}
                  />
                </div>
              </div>
            </div>
          )}

          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <input
                id="cf-required"
                type="checkbox"
                checked={isRequired}
                onChange={(e) => setIsRequired(e.target.checked)}
                className="h-4 w-4 rounded border-input"
              />
              <label htmlFor="cf-required" className="text-sm font-medium">
                {t("customField.required")}
              </label>
            </div>

            <div className="flex items-center gap-2">
              <input
                id="cf-visible-agents"
                type="checkbox"
                checked={isVisibleToAgents}
                onChange={(e) => setIsVisibleToAgents(e.target.checked)}
                className="h-4 w-4 rounded border-input"
              />
              <label
                htmlFor="cf-visible-agents"
                className="text-sm font-medium"
              >
                {t("customField.visibleToAgents")}
              </label>
              {isVisibleToAgents && (
                <span className="text-xs text-muted-foreground">
                  {t("customField.visibleToAgentsHint")}
                </span>
              )}
            </div>
          </div>

          {error && <p className="text-sm text-destructive">{error}</p>}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={onClose}
              disabled={isSubmitting}
            >
              {t("customField.cancel")}
            </Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting
                ? t("customField.saving")
                : isEdit
                  ? t("customField.saveChanges")
                  : t("customField.createField")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
