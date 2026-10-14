import { useTranslation } from "react-i18next";
import { formatDate } from "@/i18n/format";
import { ChevronLeft, ChevronRight, Filter, RefreshCw, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

interface CalendarToolbarProps {
  currentMonth: Date;
  onPrevMonth: () => void;
  onNextMonth: () => void;
  onToday: () => void;
  unscheduledCount: number;
  showUnscheduled: boolean;
  onToggleUnscheduled: () => void;
  searchQuery: string;
  onSearchChange: (query: string) => void;
  onNewRecurring?: () => void;
}

export function CalendarToolbar({
  currentMonth,
  onPrevMonth,
  onNextMonth,
  onToday,
  unscheduledCount,
  showUnscheduled,
  onToggleUnscheduled,
  searchQuery,
  onSearchChange,
  onNewRecurring,
}: CalendarToolbarProps) {
  const { t } = useTranslation("settings");
  return (
    <div className="flex items-center gap-3">
      <Button variant="outline" size="sm" onClick={onToday}>
        {t("calendar.toolbar.today")}
      </Button>

      <div className="flex items-center gap-1">
        <Button variant="ghost" size="icon" className="h-7 w-7" onClick={onPrevMonth}>
          <ChevronLeft className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-7 w-7" onClick={onNextMonth}>
          <ChevronRight className="h-4 w-4" />
        </Button>
      </div>

      <h2 className="text-lg font-semibold">
        {formatDate(currentMonth, { month: "long", year: "numeric" })}
      </h2>

      <div className="flex-1" />

      <Button
        variant={showUnscheduled ? "secondary" : "outline"}
        size="sm"
        onClick={onToggleUnscheduled}
      >
        <Filter className="mr-1.5 h-3.5 w-3.5" />
        {t("calendar.toolbar.unscheduled", { count: unscheduledCount })}
      </Button>

      {onNewRecurring && (
        <Button variant="outline" size="sm" onClick={onNewRecurring} title={t("calendar.toolbar.newRecurringTitle")}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
          {t("calendar.toolbar.recurring")}
        </Button>
      )}

      <div className="relative w-48">
        <Search className="absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          placeholder={t("calendar.toolbar.filterPlaceholder")}
          value={searchQuery}
          onChange={(e) => onSearchChange(e.target.value)}
          className="h-8 pl-7 text-sm"
        />
      </div>
    </div>
  );
}
