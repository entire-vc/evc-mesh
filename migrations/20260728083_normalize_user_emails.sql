-- +goose Up

-- Email was treated as case- and whitespace-sensitive: users_email_key is a
-- UNIQUE btree on the raw column, so "Carol@Example.COM" and
-- "carol@example.com" were two different accounts and neither could log in as
-- the other. This migration canonicalizes the stored addresses (trim +
-- lowercase) and enforces canonical uniqueness with an index on lower(email).
--
-- SAFETY: if an instance already holds two accounts that differ only by case
-- or padding, there is no safe automatic answer — merging accounts would move
-- task ownership, memberships and audit history between real people. We refuse
-- to guess: the migration aborts and names the offending addresses so the
-- operator can decide which account survives, then re-runs it.

-- +goose StatementBegin
DO $$
DECLARE
    collisions TEXT;
BEGIN
    SELECT string_agg(detail, E'\n  ' ORDER BY detail) INTO collisions
    FROM (
        SELECT lower(btrim(email)) || ' <- ' || string_agg(quote_literal(email), ', ' ORDER BY email) AS detail
        FROM users
        GROUP BY lower(btrim(email))
        HAVING count(*) > 1
    ) AS dupes;

    IF collisions IS NOT NULL THEN
        RAISE EXCEPTION E'cannot normalize users.email: % account(s) collide once case and whitespace are ignored.\n  %\n\nResolve them by hand (delete or re-address the duplicate accounts), then re-run this migration. This migration will NOT merge accounts automatically.',
            (SELECT count(*) FROM (
                SELECT 1 FROM users GROUP BY lower(btrim(email)) HAVING count(*) > 1
            ) AS c),
            collisions
            USING ERRCODE = 'unique_violation';
    END IF;
END;
$$;
-- +goose StatementEnd

-- Canonicalize the surviving rows. Safe now: the check above proved this
-- cannot violate users_email_key.
UPDATE users
   SET email = lower(btrim(email)),
       updated_at = NOW()
 WHERE email IS DISTINCT FROM lower(btrim(email));

-- Pending invites are matched against users.email on acceptance; canonicalize
-- them too so an invite issued to "Frank@Example.com" resolves to the same
-- account a normal login would. user_invites.email has no unique constraint,
-- so duplicates here are legitimate and harmless.
UPDATE user_invites
   SET email = lower(btrim(email))
 WHERE email IS DISTINCT FROM lower(btrim(email));

-- Enforce it going forward. users_email_key stays in place: it is now
-- subsumed by this index but dropping it would be an unrelated schema change.
CREATE UNIQUE INDEX ix_users_email_lower ON users (lower(email));

-- +goose Down

-- Reverses the schema change. The original mixed-case spellings are not
-- recoverable — that information is destroyed by design, since keeping it
-- would defeat the purpose of the normalization — so Down restores the
-- pre-migration constraint set only, not the pre-migration string values.
DROP INDEX IF EXISTS ix_users_email_lower;
