# Scheduler UTF-8 Freeze Fix

**Feature:** Recurring scheduler hardening — rune-safe PrevSummary, fail-safe createInstance, single catch-up on unfreeze  
**Status:** Execute  
**Parent task:** `15e148a9` (root cause analysis by Riker, confirmed from prod logs)  
**This task:** `5b5df64d`  
**Branch:** `linus/scheduler-utf8-freeze-fix`

---

## Background

Three production recurring schedules (`260daf5c`, `93eb49d9`, `a89c7804`) froze after Riker stripped `{{.PrevSummary}}` as a mitigation. Root cause: the previous instance summary was byte-truncated mid-rune (at `[:500]`), producing an invalid UTF-8 sequence in the rendered description template, which Postgres then rejected with SQLSTATE 22021. The scheduler caught the error but left `next_run_at` unadvanced, causing the same poisoned INSERT to retry every 60 seconds in an infinite loop.

---

## Scope

Three code fixes + one prod data restore (Fix #4, coordinated separately):

| Fix | What | Where |
|-----|------|--------|
| #1 | Rune-safe truncation + ToValidUTF8 backstop | `recurring_service.go` |
| #2 | createInstance fail-safe: advance next_run_at on error, quarantine after 3 failures | `recurring_service.go` + new repo methods + migration |
| #3 | WARN log on catch-up (single instance, fast-forward to next future tick) | `recurring_service.go` |
| #4 | Restore `{{.PrevSummary}}` on 3 mitigated schedules | **Prod data — coordinate with Garfield/Bob before applying** |

---

## Fix #1 — Rune-safe PrevSummary injection

### Problem
`getPreviousInstanceSummary` truncates the last comment at a raw byte offset:
```go
truncated := (*summary.LastComment)[:500]  // BAD: splits mid-rune
```
If position 500 lands inside a multi-byte UTF-8 rune (e.g., a Cyrillic letter U+0430 = 0xD0 0xB0), the truncated string becomes invalid UTF-8. This string then flows into `renderTemplate` and the rendered description is inserted into Postgres, which rejects it with `invalid byte sequence for encoding "UTF8"` (SQLSTATE 22021).

### Fix
Replace the byte-slice truncation with a rune-count-aware truncation (via `[]rune` cast):
```go
func truncateRunes(s string, maxRunes int) string {
    runes := []rune(s)
    if len(runes) <= maxRunes {
        return s
    }
    return string(runes[:maxRunes])
}
```

### Defense in depth
Apply `strings.ToValidUTF8(rendered, "")` on the final rendered title and description immediately before `taskSvc.Create`. This backstop protects against any other upstream source of invalid bytes (template evaluation, custom field injection, etc.) regardless of future changes.

---

## Fix #2 — createInstance fail-safe

### Problem
When `createInstance` returns an error, `runOneSchedule` returns immediately without advancing `next_run_at`. On the next scheduler tick (60s), `FindDue` picks up the same schedule (unchanged `next_run_at <= NOW()`), `createInstance` fails again, and the cycle repeats forever.

### Fix

#### DB schema (new migration: `20260315045_scheduler_failure_tracking.sql`)
```sql
ALTER TABLE recurring_schedules
    ADD COLUMN consecutive_failures INT NOT NULL DEFAULT 0,
    ADD COLUMN quarantined_at       TIMESTAMPTZ,
    ADD COLUMN last_error           TEXT;
```

#### New repository methods
- `RecordFailure(ctx, id, nextRunAt, errMsg)` — atomically advances `next_run_at`, increments `consecutive_failures`, sets `last_error`.
- `Quarantine(ctx, id)` — sets `is_active = FALSE`, `quarantined_at = NOW()`. Quarantined schedules are excluded from `FindDue` (`WHERE is_active = TRUE`).
- `ResetConsecutiveFailures(ctx, id)` — resets `consecutive_failures = 0`, `last_error = NULL` on successful instance creation.

#### Service logic in `runOneSchedule`
On `createInstance` error:
1. Compute next valid cron tick from `time.Now()` and call `RecordFailure` to advance the schedule past the failed cycle.
2. If `consecutive_failures + 1 >= 3`, call `Quarantine` and fire the `quarantineNotifyFn` (Telegram/log alert with schedule ID + last pq error).
3. Return the error (no instance counted as created).

On success:
- If `ConsecutiveFailures > 0`, call `ResetConsecutiveFailures`.

#### Alert hook
`recurringService` gains an optional `quarantineNotifyFn func(scheduleID uuid.UUID, lastErr string)` field, injectable via `WithQuarantineNotify(fn)` option. If not set, falls back to `log.Printf`. This allows wiring to Telegram without changing the service interface.

---

## Fix #3 — Single catch-up + WARN log on recovery

### Current behavior (already correct for the main invariant)
`runOneSchedule` already computes `nextRun` from `time.Now()` (not from previous `next_run_at`), so at most one instance is created per tick regardless of how far in the past `next_run_at` is. The ≤1 instance/tick invariant holds.

### Addition
Detect recovery scenarios and emit a WARN log:
```
[recurring] WARN schedule <id> recovered: skipped N missed occurrences (<from> → <to>), created 1 catch-up instance
```
Where N = number of cron occurrences between original `next_run_at` and `time.Now()` minus 1 (the one being created). Counted via iterating the cron expression forward from `next_run_at`.

---

## Fix #4 — Restore continuity on 3 mitigated schedules

After Fix #1 lands and deploys, re-add `{{.PrevSummary}}` to the description templates of:
- `260daf5c` — Marketing SEO Drift
- `93eb49d9` — [Spark] Daily Analytics
- `a89c7804` — [Spark] Weekly Product Review

Verify each via `POST /recurring/{id}/trigger` → `201` with a real prev summary present.

**This is a prod data change. Do NOT self-merge. Coordinate with Garfield/Bob.**

---

## Acceptance Criteria

1. Unit test: prev summary with multibyte runes (Cyrillic, emoji, U+2014) capped at a length landing mid-rune → rendered description is valid UTF-8 (`utf8.ValidString == true`) and ends on a rune boundary.
2. Unit test: `strings.ToValidUTF8` backstop strips/replaces raw invalid byte sequences (`0xD0`, `0xD1 0x0A`, `0xE2 0x80 0x0A`) before `taskSvc.Create`.
3. Integration/repro test: schedule whose prior summary is byte-truncated mid-rune → `runOneSchedule` succeeds, INSERT does not raise SQLSTATE 22021, `next_run_at` advances.
4. Test: `createInstance` forced to error → `next_run_at` still advances; after 3 consecutive forced errors → schedule quarantined + alert fired.
5. Test: schedule with `next_run_at` 3 days in the past, hourly cron → exactly 1 instance created on the tick, `next_run_at` is the next future occurrence (not 72 instances). Assert the WARN log line.
6. Manual smoke on a non-prod schedule: freeze → unfreeze → exactly one catch-up run + normal cadence resumes.
7. `dev-docs/specs/scheduler-utf8-freeze-fix.md` committed; PR description links parent `15e148a9` + this subtask; rollback note included.
8. No regression: existing recurring scheduler tests stay green.

---

## Rollback

The migration adds nullable/defaulted columns only — rollback is safe via the goose Down step:
```sql
ALTER TABLE recurring_schedules
    DROP COLUMN IF EXISTS consecutive_failures,
    DROP COLUMN IF EXISTS quarantined_at,
    DROP COLUMN IF EXISTS last_error;
```
Service code changes are backward-compatible with the old schema via Go's zero-value defaults for the new struct fields.

---

## Out of scope

- Unquarantining schedules (manual via API or direct DB update for now).
- Backfill of historical `consecutive_failures` counts.
- Fix #4 (prod data restore) — separate coordination step.
