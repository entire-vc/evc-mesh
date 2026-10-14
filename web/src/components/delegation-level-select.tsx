import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import { Select } from "@/components/ui/select";
import type { DelegationLevel } from "@/types";
import { delegationLevelConfig } from "@/lib/utils";

const DELEGATION_LEVELS: DelegationLevel[] = ["review", "auto", "supervised"];

export interface DelegationLevelSelectProps {
  value?: DelegationLevel | null;
  onChange: (level: DelegationLevel) => void;
  className?: string;
  disabled?: boolean;
}

export function DelegationLevelSelect({
  value,
  onChange,
  className,
  disabled,
}: DelegationLevelSelectProps) {
  const { t } = useTranslation("settings");
  const effective: DelegationLevel = value ?? "review";
  return (
    <div className={cn("space-y-0.5", className)}>
      <Select
        value={effective}
        onChange={(e) => onChange(e.target.value as DelegationLevel)}
        className="h-7 text-xs"
        disabled={disabled}
      >
        {DELEGATION_LEVELS.map((lvl) => (
          <option key={lvl} value={lvl} title={t(`delegation.${lvl}.description`)}>
            {t(`delegation.${lvl}.label`)}
          </option>
        ))}
      </Select>
      <p className="text-[10px] text-muted-foreground leading-tight">
        {t(`delegation.${effective}.description`)}
      </p>
    </div>
  );
}

export interface DelegationLevelBadgeProps {
  value?: DelegationLevel | null;
  className?: string;
}

export function DelegationLevelBadge({ value, className }: DelegationLevelBadgeProps) {
  const { t } = useTranslation("settings");
  const effective: DelegationLevel = value ?? "review";
  const cfg = delegationLevelConfig[effective];
  return (
    <span
      className={cn("text-[10px] font-medium", cfg.color, className)}
      title={t(`delegation.${effective}.description`)}
    >
      {t(`delegation.${effective}.label`)}
    </span>
  );
}
