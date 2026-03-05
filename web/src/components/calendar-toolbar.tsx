import { ChevronLeft, ChevronRight, Filter, RefreshCw, Search } from "lucide-react";
import { format } from "date-fns";
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
  return (
    <div className="flex items-center gap-3">
      <Button variant="outline" size="sm" onClick={onToday}>
        Today
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
        {format(currentMonth, "MMMM yyyy")}
      </h2>

      <div className="flex-1" />

      <Button
        variant={showUnscheduled ? "secondary" : "outline"}
        size="sm"
        onClick={onToggleUnscheduled}
      >
        <Filter className="mr-1.5 h-3.5 w-3.5" />
        {unscheduledCount} Unscheduled
      </Button>

      {onNewRecurring && (
        <Button variant="outline" size="sm" onClick={onNewRecurring} title="New Recurring Task">
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
          Recurring
        </Button>
      )}

      <div className="relative w-48">
        <Search className="absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          placeholder="Filter tasks..."
          value={searchQuery}
          onChange={(e) => onSearchChange(e.target.value)}
          className="h-8 pl-7 text-sm"
        />
      </div>
    </div>
  );
}
