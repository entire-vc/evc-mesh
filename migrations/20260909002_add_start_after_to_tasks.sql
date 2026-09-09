-- +goose Up

-- Part 1/7 of #e9ce6b91 (task-splitter, critical path sub1→sub2→sub6→sub7),
-- source #7e6fea59: due_date currently serves two incompatible fleet
-- readings — a backlog wake-alarm (mesh-intake-sweep.py / monitor_promotion.go
-- promote a backlog card once due_date has passed) and a "not-before" gate on
-- todo/in_progress (fiddler.py / dispatcher skip feeding a card while
-- due_date is in the future). A due_date-based backfill for one reading once
-- froze live todo work relying on the other (3 live incidents, one direct
-- Pavel instruction).
--
-- start_after gives the "not-before" reading its own field. Nullable,
-- additive: due_date is untouched here and keeps meaning "backlog wake-alarm
-- deadline" for mesh-intake-sweep.py/monitor_promotion.go. sub2/sub3 (fiddler/
-- dispatcher) switch their skip-logic to read start_after instead; sub6
-- backfills the currently-affected cards only after those readers switch.
ALTER TABLE tasks ADD COLUMN start_after TIMESTAMPTZ;

-- +goose Down
ALTER TABLE tasks DROP COLUMN IF EXISTS start_after;
