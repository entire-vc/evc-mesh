-- +goose Up
-- +goose StatementBegin
-- Part C of #1eb4fd7d (backlog park-alarm gate, #559270cf). The park-alarm gate added
-- in #559270cf (ParkAlarmRequired, task_service.go MoveTask/Update) refuses NEW writes
-- that leave a kind:monitor/phase:verify card parked in backlog without a future
-- due_date — but it is not retroactive by design (existing cards must not suddenly
-- become uneditable, see park_alarm.go's alarmStateUnchanged doc comment). This
-- migration is the retroactive half: it gives every EXISTING such card the same
-- wake-up path a new one is now required to carry.
--
-- Measured on prod 2026-09-07 (Garfield, read-only, snapshot ~13:45Z, #559270cf
-- comment thread): 28 open cards carry kind:monitor or phase:verify with
-- due_date IS NULL. That count moves between the measurement and this migration
-- running — the WHERE clause re-derives the population at execution time rather than
-- operating off a fixed id list, so it backfills whatever the live count actually is.
--
-- due_date = now() + 7 days, matching the spec's own choice (#1eb4fd7d Part C) — long
-- enough that a card doing genuine passive-wait work isn't immediately re-swept, short
-- enough that a card nobody revisits surfaces again within a week rather than silently
-- riding a backlog park forever.
--
-- Explicitly scoped to OPEN tasks only (status category not done/cancelled) — a
-- done/cancelled card's due_date is inert (SweepDueBacklogTasks only reads
-- backlog-category cards; FindDueBacklogTasks's own query already excludes terminal
-- statuses), so touching one here would just be a needless write + a confusing comment
-- on a closed card.
--
-- Deliberately does NOT touch the three named already-overdue backlog cards
-- (64e84eb1, 9ed65c55, 2a53c5e2) flagged in the same audit as "must not promote" —
-- this migration's WHERE clause only ever matches due_date IS NULL, and all three
-- already carry a (past) due_date, so they are outside this migration's population by
-- construction, not by a special-cased exclusion. The negative control this migration's
-- acceptance criterion (#1eb4fd7d AC4) asks for is checking exactly that: none of the
-- three should appear in `updated_task_ids` below.
WITH targets AS (
    SELECT t.id
    FROM tasks t
    JOIN task_statuses s ON s.id = t.status_id
    WHERE t.deleted_at IS NULL
      AND s.category NOT IN ('done', 'cancelled')
      AND t.due_date IS NULL
      AND (t.labels && ARRAY['kind:monitor', 'phase:verify']::text[])
),
updated_task_ids AS (
    UPDATE tasks t
    SET due_date = now() + interval '7 days',
        updated_at = now()
    FROM targets
    WHERE t.id = targets.id
    RETURNING t.id
)
INSERT INTO comments (id, task_id, author_id, author_type, body, is_internal, created_at, updated_at)
SELECT
    gen_random_uuid(),
    updated_task_ids.id,
    '00000000-0000-0000-0000-000000000000'::uuid,
    'system',
    '⏰ Будильник проставлен задним числом: `due_date` = ' ||
    to_char(now() + interval '7 days', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') ||
    ' (+7 дней от бэкфилла). Причина: карточка несёт метку `kind:monitor`/`phase:verify` ' ||
    'и до сих пор не имела `due_date` — backlog не подаёт такие карточки в фид, ' ||
    'а промоушн-свип срабатывает только по прошедшему `due_date` (#559270cf, #1eb4fd7d). ' ||
    'Без даты у карточки не было пути пробуждения вообще. Поставь свою дату, если нужен ' ||
    'другой срок, или сними метку, если карточка уже готова к работе — новые карточки ' ||
    'без даты под этими метками сервер теперь отклоняет (422), но это одноразовая ' ||
    'починка для уже существующих.',
    false,
    now(),
    now()
FROM updated_task_ids;
-- +goose StatementEnd

-- +goose Down
-- no-op: intentionally irreversible. The NULL due_date this migration replaces was
-- itself the defect (#559270cf: a park with no wake-up path) — reverting would put
-- these cards back into the exact "nothing will ever feed this again" state the
-- migration exists to fix, not undo anything a rollback is supposed to undo.
